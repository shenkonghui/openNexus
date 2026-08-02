package acp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// agentTransport 抽象 Connection 底层的 agent 通信通道。
// 两种实现：
//   - *Process：直连模式，agent 是主 server 子进程，stdio 管道通信，随主 server 退出。
//   - *bridgeTransport：常驻模式，agent 挂在独立 bridge 守护进程下，UDS 通信，
//     主 server 重启后可重新拨号复用同一 agent（进程与内存上下文保留）。
type agentTransport interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	// Stop 彻底终止 agent（含 bridge，若有）。
	Stop() error
	// Detach 仅断开通信通道，agent 进程保持运行（直连模式等价 Stop）。
	Detach() error
	// Pid 返回可供 KillProcessGroup 使用的进程组 PGID（bridge 模式为 bridge PID）。
	Pid() int
	// Sandboxed 返回 agent 进程是否真正运行在 OS 沙箱内（非降级直通）。
	// 复用已存在 bridge 时无法确定，返回 false。
	Sandboxed() bool
	InspectFailure() string
}

// bridgeSpawnDialTimeout 是拉起 bridge 后等待其 socket 就绪的总时长。
// bridge 启动即监听（毫秒级），此值仅兜底磁盘/系统繁忙场景。
const bridgeSpawnDialTimeout = 5 * time.Second

// bridgeLogTailLimit 是 InspectFailure 读取 bridge 日志尾部的最大字节数。
const bridgeLogTailLimit = 4 * 1024

// ResolveBridgeSocketDir 返回 bridge socket 目录。
// UDS 路径有长度上限（macOS 约 104 字节），preferred 过长时回退系统临时目录。
func ResolveBridgeSocketDir(preferred string) string {
	dir := filepath.Join(preferred, "acp-sockets")
	if len(filepath.Join(dir, "acp-0123456789abcdef.sock")) < 100 {
		return dir
	}
	return filepath.Join(os.TempDir(), "opennexus-acp")
}

// bridgeSocketName 由连接池键生成确定性 socket 文件名，
// 使主 server 重启后能按同一 agentType+cwd 找回原 bridge。
func bridgeSocketName(poolKey string) string {
	sum := sha256.Sum256([]byte(poolKey))
	return "acp-" + hex.EncodeToString(sum[:8]) + ".sock"
}

// bridgeTransport 通过 UDS 连接常驻 acp-bridge 守护进程（agent 挂在 bridge 下）。
type bridgeTransport struct {
	conn       net.Conn
	pid        int // bridge 守护进程 PID（同时是进程组 PGID）
	socketPath string
	reused     bool // true=拨号复用已存在的 bridge（主 server 重启场景）
	agentName  string
	// sandboxed 标记本次新建的 bridge+agent 是否真正运行在 OS 沙箱内。
	// 复用已存在 bridge 时为 false（无法确定原 bridge 的沙箱状态）。
	sandboxed bool
}

// Sandboxed 返回 bridge+agent 是否真正运行在 OS 沙箱内。
func (b *bridgeTransport) Sandboxed() bool { return b.sandboxed }

// NewBridgeTransport 建立到 bridge 的 UDS 连接。
// 先尝试拨号已存在的 socket（复用存活 bridge 及其 agent）；
// 拨不通或 forceNew 时拉起全新 bridge 守护进程（Setsid 脱离主 server）。
func NewBridgeTransport(backend Backend, workDir, socketPath string, forceNew bool) (*bridgeTransport, error) {
	if !forceNew {
		if conn, err := net.DialTimeout("unix", socketPath, bridgeDialProbeTimeout); err == nil {
			pid := readBridgePID(socketPath)
			slog.Info("复用常驻 acp-bridge（agent 进程与上下文保留）",
				"agent", backend.Name(), "socket", socketPath, "bridge_pid", pid)
			return &bridgeTransport{
				conn: conn, pid: pid, socketPath: socketPath,
				reused: true, agentName: backend.Name(),
			}, nil
		}
	}

	pid, sandboxed, err := spawnBridgeDaemon(backend, workDir, socketPath)
	if err != nil {
		return nil, err
	}
	// 轮询拨号等待 bridge 监听就绪（bridge 启动即监听，通常首轮成功）
	deadline := time.Now().Add(bridgeSpawnDialTimeout)
	for {
		conn, dialErr := net.DialTimeout("unix", socketPath, bridgeDialProbeTimeout)
		if dialErr == nil {
			return &bridgeTransport{
				conn: conn, pid: pid, socketPath: socketPath,
				reused: false, agentName: backend.Name(), sandboxed: sandboxed,
			}, nil
		}
		if time.Now().After(deadline) {
			_ = KillProcessGroup(pid)
			return nil, fmt.Errorf("等待 acp-bridge socket 就绪超时: %w（日志: %s）",
				dialErr, BridgeLogFile(socketPath))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// spawnBridgeDaemon 以 Setsid 独立会话拉起 acp-bridge 守护进程，返回其 PID 与沙箱状态。
// bridge 与主 server 生命周期解耦（同 watchdog 机制），主 server 退出后由 init/launchd 接管。
// 返回的 sandboxed=true 表示 bridge+agent 真正运行在 OS 沙箱内（非降级直通）。
func spawnBridgeDaemon(backend Backend, workDir, socketPath string) (pid int, sandboxed bool, err error) {
	command := backend.Command()
	resolved, lookErr := resolveAgentCommand(command)
	if lookErr != nil {
		return 0, false, fmt.Errorf("启动 agent 进程 %s：命令 %q 不在 PATH 中（%w）；请检查命令是否安装或配置是否正确",
			backend.Name(), filepath.Base(command), lookErr)
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, false, fmt.Errorf("获取自身可执行文件路径: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return 0, false, fmt.Errorf("创建 bridge socket 目录: %w", err)
	}
	// 拨号已失败说明无人监听，残留 socket 文件可安全清理
	_ = os.Remove(socketPath)

	args := []string{"acp-bridge", "--socket", socketPath, "--cwd", workDir, "--", resolved}
	args = append(args, backend.Args()...)
	argv := append([]string{exe}, args...)
	// 沙箱包裹整条 bridge argv（bridge+agent 同沙箱）；socket 目录须可写
	sb := CurrentSandboxSettings()
	if sb.Enabled {
		profile := BuildSandboxProfile(backend, workDir, filepath.Dir(socketPath))
		wrapped, degraded := SandboxWrap(argv, profile)
		switch {
		case !degraded:
			argv = wrapped
			sandboxed = true
		case sb.Mode == SandboxModeEnforce:
			return 0, false, fmt.Errorf("沙箱模式为 enforce 但当前平台沙箱不可用，拒绝启动 agent %s", backend.Name())
		default:
			logSandboxDegraded(backend.Name())
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	baseEnv := cmd.Environ()
	if sandboxed {
		baseEnv = SanitizeEnvForSandbox(baseEnv)
	}
	cmd.Env = append(baseEnv, backend.Env()...)
	setProcessGroup(cmd) // Setsid：独立会话，脱离主 server 与控制终端
	if devNull, derr := os.OpenFile(os.DevNull, os.O_RDWR, 0); derr == nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	slog.Debug("拉起 acp-bridge 守护进程",
		"agent", backend.Name(), "socket", socketPath, "command", resolved, "cwd", workDir)
	if err := cmd.Start(); err != nil {
		return 0, false, fmt.Errorf("启动 acp-bridge 进程: %w", err)
	}
	pid = cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, sandboxed, nil
}

// readBridgePID 读取 bridge 的 PID 文件；失败返回 0（Stop 时退化为仅断开连接）。
func readBridgePID(socketPath string) int {
	data, err := os.ReadFile(BridgePIDFile(socketPath))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// bridgeStdin 把 UDS 连接的写方向适配为 io.WriteCloser。
// Close 只半关写方向（CloseWrite），bridge 收到 EOF 视为客户端离开、agent 保持运行。
type bridgeStdin struct{ conn net.Conn }

func (b *bridgeStdin) Write(p []byte) (int, error) { return b.conn.Write(p) }

func (b *bridgeStdin) Close() error {
	if uc, ok := b.conn.(*net.UnixConn); ok {
		return uc.CloseWrite()
	}
	return b.conn.Close()
}

// bridgeStdout 把 UDS 连接的读方向适配为 io.ReadCloser。
type bridgeStdout struct{ conn net.Conn }

func (b *bridgeStdout) Read(p []byte) (int, error) { return b.conn.Read(p) }
func (b *bridgeStdout) Close() error               { return b.conn.Close() }

func (t *bridgeTransport) Stdin() io.WriteCloser { return &bridgeStdin{conn: t.conn} }
func (t *bridgeTransport) Stdout() io.ReadCloser { return &bridgeStdout{conn: t.conn} }

// Pid 返回 bridge 守护进程 PID（进程组 PGID，覆盖 bridge + agent）。
func (t *bridgeTransport) Pid() int { return t.pid }

// Reused 返回是否复用了已存在的 bridge（主 server 重启后重连场景）。
func (t *bridgeTransport) Reused() bool { return t.reused }

// Detach 仅断开 UDS 连接：bridge 与 agent 保持运行，等待下次拨号复用。
func (t *bridgeTransport) Detach() error {
	slog.Debug("断开 acp-bridge 连接（agent 保持运行）",
		"agent", t.agentName, "socket", t.socketPath, "bridge_pid", t.pid)
	return t.conn.Close()
}

// Stop 彻底终止 bridge 与 agent（整个进程组）。
func (t *bridgeTransport) Stop() error {
	_ = t.conn.Close()
	if t.pid > 0 {
		if err := KillProcessGroup(t.pid); err != nil {
			return fmt.Errorf("停止 acp-bridge 进程组: %w", err)
		}
	}
	return nil
}

// InspectFailure 诊断 bridge 模式下的启动/握手失败，附 bridge 日志尾部（含 agent stderr）。
func (t *bridgeTransport) InspectFailure() string {
	alive := ProcessAlive(t.pid)
	tail := bridgeLogTail(t.socketPath)
	var state string
	switch {
	case t.pid == 0:
		state = "bridge PID 未知（PID 文件缺失）"
	case !alive:
		state = "bridge 进程已退出（agent 大概率启动失败）"
	case t.reused:
		state = "复用的常驻 agent 无响应（可能不支持重复 initialize，将销毁重建）"
	default:
		state = "bridge 存活但 agent 握手无响应（可能等待登录认证、下载依赖或网络问题）"
	}
	if tail == "" {
		return state
	}
	return fmt.Sprintf("%s，bridge 日志尾部:\n%s", state, tail)
}

// bridgeLogTail 读取 bridge 日志尾部（最多 bridgeLogTailLimit 字节）。
// 共享日志文件可能较大，用 Seek 避免全量读取。
func bridgeLogTail(socketPath string) string {
	f, err := os.Open(BridgeLogFile(socketPath))
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	size := fi.Size()
	skip := size - int64(bridgeLogTailLimit)
	if skip > 0 {
		if _, err := f.Seek(skip, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
