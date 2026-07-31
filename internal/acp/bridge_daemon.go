package acp

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// bridge 守护进程：`opennexus acp-bridge --socket <path> --cwd <dir> -- <agent command> [args...]`
//
// 设计目标：让 agent 进程的生命周期与主 server 解耦（主 server 重启不重启 agent）。
//
//	[主 server] ◄── Unix Domain Socket ──► [acp-bridge 守护进程] ◄── stdio 管道 ──► [agent 进程]
//
// bridge 由主 server 以 Setsid 独立会话拉起（与 watchdog 相同机制），agent 是 bridge 的
// 直接子进程（共享 bridge 的进程组），因此：
//   - 主 server 退出/崩溃只断开 socket，bridge 与 agent 完全不受影响；
//   - 主 server 重启后重新拨号同一 socket 即可瞬间复用原 agent（内存上下文保留）；
//   - watchdog 兜底：主 server 心跳失联超时后 KillProcessGroup(bridge pid) 一并清理 bridge + agent。
//
// 透传按 NDJSON 行对齐（ACP JSON-RPC 报文以 \n 分隔）：客户端断开时半行丢弃/暂存，
// 保证重连后的新客户端永远从完整报文边界开始读写。

// bridgeDialProbeTimeout 是探测 stale socket 时的拨号超时。
const bridgeDialProbeTimeout = 300 * time.Millisecond

// bridgeAgentStopGrace 是 bridge 收到退出信号后等待 agent 自行退出的宽限时间。
const bridgeAgentStopGrace = 2 * time.Second

// bridgeLineQueueSize 是 agent→客户端方向的行缓冲条数。
// 客户端断开期间最多暂存这么多行，超过后读取 goroutine 阻塞，
// 反压传导到 agent 的 stdout 管道（内核缓冲写满后 agent 阻塞输出）。
const bridgeLineQueueSize = 256

// BridgePIDFile 返回 bridge 守护进程的 PID 文件路径（与 socket 同目录）。
func BridgePIDFile(socketPath string) string { return socketPath + ".pid" }

// BridgeLogFile 返回所有 bridge 守护进程共享的日志文件路径（含 agent stderr，供握手失败诊断）。
// 所有 bridge 实例追加写入同一文件，通过日志前缀中的 socket 路径区分实例。
func BridgeLogFile(socketPath string) string { return filepath.Join(filepath.Dir(socketPath), "bridge.log") }

// RunBridgeDaemon 是 acp-bridge 子命令入口，阻塞运行直到 agent 退出或收到退出信号。
func RunBridgeDaemon(argv []string) error {
	fs := flag.NewFlagSet("acp-bridge", flag.ExitOnError)
	socketPath := fs.String("socket", "", "UDS 监听路径（必填）")
	cwd := fs.String("cwd", "", "agent 工作目录（可选）")
	_ = fs.Parse(argv)
	agentArgv := fs.Args()

	if *socketPath == "" {
		return fmt.Errorf("acp-bridge: 缺少 --socket 参数")
	}
	if len(agentArgv) == 0 {
		return fmt.Errorf("acp-bridge: 缺少 agent 命令（在 -- 之后）")
	}

	// 日志写到 socket 同目录文件：bridge 以 Setsid 脱离终端运行，标准流不可用；
	// agent 的 stderr 也汇入此文件，供主 server 握手失败时读取尾部诊断。
	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o700); err != nil {
		return fmt.Errorf("acp-bridge: 创建 socket 目录: %w", err)
	}
	logFile, err := os.OpenFile(BridgeLogFile(*socketPath), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("acp-bridge: 打开日志文件: %w", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.SetPrefix("[acp-bridge] ")
	log.Printf("启动：socket=%s cwd=%s agent=%v", *socketPath, *cwd, agentArgv)

	// stale socket 处理：能拨通说明已有 bridge 在服务（拒绝重复启动）；拨不通则清理残留文件。
	if _, statErr := os.Stat(*socketPath); statErr == nil {
		if conn, dialErr := net.DialTimeout("unix", *socketPath, bridgeDialProbeTimeout); dialErr == nil {
			_ = conn.Close()
			return fmt.Errorf("acp-bridge: socket %s 已有 bridge 在运行", *socketPath)
		}
		_ = os.Remove(*socketPath)
	}

	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		return fmt.Errorf("acp-bridge: 监听 %s: %w", *socketPath, err)
	}
	_ = os.Chmod(*socketPath, 0o600)

	pidFile := BridgePIDFile(*socketPath)
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		log.Printf("写 PID 文件失败: %v", err)
	}
	cleanup := func() {
		_ = listener.Close()
		_ = os.Remove(*socketPath)
		_ = os.Remove(pidFile)
	}
	defer cleanup()

	// 拉起 agent 子进程：不另建会话，与 bridge 共享进程组，
	// 使 watchdog / 主 server 通过 KillProcessGroup(bridge pid) 能一并回收两者。
	agentCmd := exec.Command(agentArgv[0], agentArgv[1:]...)
	if *cwd != "" {
		agentCmd.Dir = *cwd
	}
	agentCmd.Env = os.Environ()
	agentCmd.Stderr = logFile
	agentIn, err := agentCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acp-bridge: 创建 agent stdin 管道: %w", err)
	}
	agentOut, err := agentCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acp-bridge: 创建 agent stdout 管道: %w", err)
	}
	if err := agentCmd.Start(); err != nil {
		return fmt.Errorf("acp-bridge: 启动 agent 进程: %w", err)
	}
	log.Printf("agent 进程已启动 pid=%d", agentCmd.Process.Pid)

	agentDone := make(chan struct{})
	go func() {
		err := agentCmd.Wait()
		log.Printf("agent 进程退出: %v", err)
		close(agentDone)
	}()

	// agent stdout 持续按行读入有界队列，与客户端连接生命周期解耦：
	// 客户端断开期间行暂存队列，重连后从完整报文边界继续下发。
	agentLines := make(chan []byte, bridgeLineQueueSize)
	go func() {
		defer close(agentLines)
		r := bufio.NewReader(agentOut)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				agentLines <- line
			}
			if err != nil {
				return // agent stdout EOF：进程退出
			}
		}
	}()

	// 接受连接（串行：同一时刻只服务一个客户端，新客户端在 backlog 排队）
	connCh := make(chan net.Conn)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // listener 已关闭
			}
			connCh <- conn
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	stopAgent := func() {
		_ = agentIn.Close()
		if agentCmd.Process != nil {
			_ = agentCmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-agentDone:
		case <-time.After(bridgeAgentStopGrace):
			_ = agentCmd.Process.Kill()
			<-agentDone
		}
	}

	// pending 保存上次客户端断开时未送达的完整行，重连后优先补发。
	var pending []byte
	for {
		select {
		case <-agentDone:
			log.Printf("agent 已退出，bridge 结束")
			return nil
		case sig := <-sigCh:
			log.Printf("收到信号 %v，终止 agent 并退出", sig)
			stopAgent()
			return nil
		case conn := <-connCh:
			log.Printf("客户端已连接")
			pending = serveBridgeClient(conn, agentIn, agentLines, pending)
			log.Printf("客户端已断开（agent 保持运行）")
		}
	}
}

// serveBridgeClient 服务单个客户端直到断开，返回未送达的暂存行（供下个客户端补发）。
// 双向均按行透传：
//   - 客户端→agent：只转发完整行，断开时的半行直接丢弃（请求未完整送达，客户端重连后重发）；
//   - agent→客户端：写失败的行暂存为 pending，保证报文不丢不裂。
func serveBridgeClient(conn net.Conn, agentIn io.Writer, agentLines <-chan []byte, pending []byte) []byte {
	defer conn.Close()

	// 客户端 → agent
	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return // 断开；半行（若有）丢弃
			}
			if _, err := agentIn.Write(line); err != nil {
				return // agent stdin 已关闭（agent 退出中）
			}
		}
	}()

	// 补发上次断连未送达的行
	if pending != nil {
		if _, err := conn.Write(pending); err != nil {
			return pending
		}
		pending = nil
	}

	// agent → 客户端
	for {
		select {
		case line, ok := <-agentLines:
			if !ok {
				return nil // agent 退出，主循环随 agentDone 结束
			}
			if _, err := conn.Write(line); err != nil {
				return line // 客户端断开：此行留待重连补发
			}
		case <-clientGone:
			return nil
		}
	}
}
