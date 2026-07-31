package acp

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// stopGracePeriod 是 Stop() 发送 SIGTERM 后等待进程自行退出的宽限时间，
// 超过后升级为 SIGKILL（强制终止进程组）。
const stopGracePeriod = 2 * time.Second

// stderrCaptureLimit 是 InspectFailure 返回的 stderr 尾部最大字节数。
// 取尾部是因为启动错误（如缺少 API Key、版本不兼容）通常打印在最后。
const stderrCaptureLimit = 4 * 1024

// procState 表示 InspectFailure 探测到的子进程状态，由平台相关函数 probeProcessState 返回。
type procState int

const (
	procStateRunning procState = iota // 进程仍在运行（可能正常，可能无响应）
	procStateExited                   // 进程已终止
)

// Process 管理 agent 子进程的生命周期。
type Process struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderrBuf *ringBuffer // 捕获 stderr 尾部，握手失败时用于诊断
	backend   Backend
}

// resolveAgentCommand 定位可执行文件。
// 对带路径的占位命令（如 ./dist-package/cursor-agent），取最后一段在 PATH 中查找。
func resolveAgentCommand(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err == nil {
		return path, nil
	}
	base := filepath.Base(command)
	if base == "" || base == "." || base == command {
		return "", err
	}
	return exec.LookPath(base)
}

// NewProcess 启动指定后端的 agent 子进程。
// workDir 非空时设为子进程工作目录，使 agent 工具调用与 ACP session cwd 一致。
func NewProcess(backend Backend, workDir string) (*Process, error) {
	command, args := backend.Command(), backend.Args()
	slog.Debug("启动 agent 子进程",
		"agent", backend.Name(),
		"command", command,
		"args", args,
		"cwd", workDir,
	)

	// 预检命令是否在 PATH 中，给出比 exec 的 "executable file not found" 更明确的提示
	resolved, lookErr := resolveAgentCommand(command)
	if lookErr != nil {
		lookName := filepath.Base(command)
		if lookName == "" || lookName == "." {
			lookName = command
		}
		slog.Error("agent 命令不在 PATH 中",
			"agent", backend.Name(),
			"command", command,
			"look", lookName,
			"args", args,
			"err", lookErr)
		return nil, fmt.Errorf("启动 agent 进程 %s：命令 %q 不在 PATH 中（%w）；请检查命令是否安装或配置是否正确",
			backend.Name(), lookName, lookErr)
	}
	command = resolved

	// 沙箱包裹：与 agent 类型无关，stdio 原样透传（ACP 协议无感知）。
	// enforce 模式下沙箱不可用则拒绝启动；auto 模式降级直通并告警。
	argv := append([]string{command}, args...)
	sandboxed := false
	if sb := CurrentSandboxSettings(); sb.Enabled {
		wrapped, degraded := SandboxWrap(argv, BuildSandboxProfile(backend, workDir))
		switch {
		case !degraded:
			argv = wrapped
			sandboxed = true
		case sb.Mode == SandboxModeEnforce:
			return nil, fmt.Errorf("启动 agent 进程 %s：沙箱模式为 enforce 但当前平台沙箱不可用（缺少 sandbox-exec/bwrap）", backend.Name())
		default:
			logSandboxDegraded(backend.Name())
		}
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	// 沙箱开启时剥离凭证类环境变量（DOCKER_/AWS_/KUBECONFIG 等），
	// backend.Env() 声明的 API Key 等在净化后追加，显式透传。
	baseEnv := cmd.Environ()
	if sandboxed {
		baseEnv = SanitizeEnvForSandbox(baseEnv)
	}
	cmd.Env = append(baseEnv, backend.Env()...)
	// 设置独立进程组，便于 Stop() 时按 PGID 一并终止孙进程
	// （npm exec / cursor-agent / codebuddy 等会派生子进程，否则会变成孤儿）。
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdin 管道: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道: %w", err)
	}
	// 捕获 stderr 尾部用于握手失败诊断，同时保留实时输出到当前进程 stderr 便于开发排查。
	// agent 的启动错误（缺少 API Key、版本不兼容等）会打到这里。
	stderrBuf := newRingBuffer(stderrCaptureLimit)
	cmd.Stderr = io.MultiWriter(os.Stderr, stderrBuf)

	if err := cmd.Start(); err != nil {
		slog.Error("启动 agent 进程失败",
			"agent", backend.Name(),
			"command", command,
			"args", args,
			"cwd", workDir,
			"err", err)
		return nil, fmt.Errorf("启动 agent 进程 %s: %w", backend.Name(), err)
	}

	return &Process{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    stdout,
		stderrBuf: stderrBuf,
		backend:   backend,
	}, nil
}

// Stdin 返回子进程的 stdin 管道。
func (p *Process) Stdin() io.WriteCloser {
	return p.stdin
}

// Pid 返回直系子进程 PID。由于 setProcessGroup 使用 Setsid，PID 同时也是进程组 PGID，
// watchdog 可用 kill(-pid) 杀掉整个进程组。进程未启动时返回 0。
func (p *Process) Pid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Stdout 返回子进程的 stdout 管道。
func (p *Process) Stdout() io.ReadCloser {
	return p.stdout
}

// Detach 对直连进程无「仅断开」语义：agent 是本进程子进程，stdio 随连接关闭失效，
// 等价于 Stop。常驻语义由 bridgeTransport.Detach 提供。
func (p *Process) Detach() error {
	return p.Stop()
}

// Stop 停止子进程。
// 先关闭 stdin，再向进程组发送 SIGTERM 并等待 stopGracePeriod；
// 若进程仍存活则升级为 SIGKILL 强制终止整个进程组（含孙进程）。
// terminateProcessGroup 已回收直系子进程，此处不再重复 Wait。
func (p *Process) Stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	_ = p.stdin.Close()
	if err := terminateProcessGroup(p.cmd.Process, stopGracePeriod); err != nil {
		return fmt.Errorf("停止 agent 进程: %w", err)
	}
	return nil
}

// InspectFailure 在 agent 启动/握手失败后诊断子进程状态，返回人类可读的线索。
// 判定两类典型故障：
//  1. 进程已退出——可能因缺少 API Key、配置错误或依赖缺失导致启动失败
//  2. 进程仍在运行但无响应——可能正在等待登录认证、下载依赖、遇到网络问题，
//     或 agent 子进程被作业控制信号挂起（SIGTSTP）
//
// 必须在 Stop() 之前调用；调用后 Stop() 仍会正常回收进程。
// 不主动调用 Wait() 回收进程（避免影响后续 Stop 的 terminateProcessGroup）。
func (p *Process) InspectFailure() string {
	proc := p.cmd.Process
	if proc == nil {
		return "进程未启动"
	}

	if probeProcessState(proc.Pid) == procStateExited {
		return "进程已退出（可能因缺少 API Key、配置错误或依赖缺失导致启动失败）"
	}
	return p.unresponsiveDiagnosis()
}

// unresponsiveDiagnosis 组装「进程存活但无响应」的诊断，附 stderr 尾部。
func (p *Process) unresponsiveDiagnosis() string {
	tail := p.stderrTail()
	if tail == "" {
		return "进程仍在运行但握手无响应（可能正在等待登录认证、下载依赖、遇到网络问题，" +
			"或 agent 子进程被作业控制信号挂起）"
	}
	return fmt.Sprintf("进程仍在运行但握手无响应，stderr 尾部输出:\n%s", tail)
}

// stderrTail 返回捕获的 stderr 尾部（去首尾空白，截断到 stderrCaptureLimit）。
func (p *Process) stderrTail() string {
	if p.stderrBuf == nil {
		return ""
	}
	return strings.TrimSpace(p.stderrBuf.String())
}

// ringBuffer 是一个容量固定的字节缓冲区，只保留最后写入的 maxLen 字节。
// 用于捕获 agent stderr 尾部用于诊断，避免长期运行的进程导致缓冲区无限增长。
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newRingBuffer(max int) *ringBuffer {
	return &ringBuffer{max: max}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 复制一份避免外部修改
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return string(out)
}
