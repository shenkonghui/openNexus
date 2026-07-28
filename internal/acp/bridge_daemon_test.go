//go:build !windows

package acp

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// shortSocketPath 返回一个足够短的 UDS 路径（macOS 限制约 104 字节，t.TempDir() 常超长）。
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bt")
	if err != nil {
		t.Fatalf("创建短临时目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// dialBridgeWait 轮询拨号直到 bridge 就绪或超时。
func dialBridgeWait(t *testing.T, socketPath string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, bridgeDialProbeTimeout)
		if err == nil {
			return conn
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("拨号 bridge socket 超时: %s", socketPath)
	return nil
}

// TestRunBridgeDaemon_ReconnectReusesAgent 验证 bridge 的核心语义：
// 客户端断开重连后复用同一个 agent 进程（进程内状态保留）。
// 伪 agent 用 awk 按行回显并带递增行号：跨连接行号连续 ⇒ 同一进程。
func TestRunBridgeDaemon_ReconnectReusesAgent(t *testing.T) {
	socketPath := shortSocketPath(t, "b.sock")

	done := make(chan error, 1)
	go func() {
		done <- RunBridgeDaemon([]string{
			"--socket", socketPath, "--",
			"awk", `{print NR": "$0; fflush()}`,
		})
	}()

	roundTrip := func(conn net.Conn, send, want string) {
		t.Helper()
		if _, err := conn.Write([]byte(send + "\n")); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatalf("读取回显失败: %v", err)
		}
		if got := strings.TrimSuffix(line, "\n"); got != want {
			t.Fatalf("回显不符: got %q, want %q", got, want)
		}
	}

	// 第一个客户端
	conn1 := dialBridgeWait(t, socketPath)
	roundTrip(conn1, "hello", "1: hello")
	_ = conn1.Close()

	// 重连：行号从 2 继续 ⇒ 复用了同一个 awk 进程
	conn2 := dialBridgeWait(t, socketPath)
	roundTrip(conn2, "world", "2: world")

	// PID 文件应存在且非空（本测试进程内运行，即当前 pid）
	if data, err := os.ReadFile(BridgePIDFile(socketPath)); err != nil {
		t.Fatalf("读取 PID 文件失败: %v", err)
	} else if len(data) == 0 {
		t.Fatalf("PID 文件为空")
	}

	_ = conn2.Close()

	// SIGTERM 触发 stopAgent 并退出（signal.Notify 拦截，测试进程不受影响）
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("发送 SIGTERM 失败: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunBridgeDaemon 返回错误: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunBridgeDaemon 未在超时内退出")
	}

	// 退出后 socket 与 PID 文件应被清理
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Errorf("退出后 socket 文件未清理: %v", err)
	}
	if _, err := os.Stat(BridgePIDFile(socketPath)); !os.IsNotExist(err) {
		t.Errorf("退出后 PID 文件未清理: %v", err)
	}
}

// TestRunBridgeDaemon_RejectDuplicate 验证同一 socket 已有 bridge 时拒绝重复启动。
func TestRunBridgeDaemon_RejectDuplicate(t *testing.T) {
	socketPath := shortSocketPath(t, "dup.sock")

	done := make(chan error, 1)
	go func() {
		done <- RunBridgeDaemon([]string{"--socket", socketPath, "--", "cat"})
	}()
	conn := dialBridgeWait(t, socketPath)

	// 第二个实例应立即报错退出
	err := RunBridgeDaemon([]string{"--socket", socketPath, "--", "cat"})
	if err == nil || !strings.Contains(err.Error(), "已有 bridge 在运行") {
		t.Fatalf("重复启动应报错, got: %v", err)
	}

	// 先断开客户端再发信号：bridge 服务客户端期间主循环不处理信号，
	// 生产路径的强制清理用 KillProcessGroup，不依赖 SIGTERM 即时响应。
	_ = conn.Close()
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("bridge 未在超时内退出")
	}
}

func TestBridgeSocketName_Deterministic(t *testing.T) {
	a := bridgeSocketName("claude-code\x00/tmp/work")
	b := bridgeSocketName("claude-code\x00/tmp/work")
	c := bridgeSocketName("cursor\x00/tmp/work")
	if a != b {
		t.Errorf("同一 poolKey 应得到相同 socket 名: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("不同 poolKey 不应得到相同 socket 名: %q", a)
	}
	if !strings.HasPrefix(a, "acp-") || !strings.HasSuffix(a, ".sock") {
		t.Errorf("socket 名格式不符: %q", a)
	}
}

func TestResolveBridgeSocketDir_LongPathFallback(t *testing.T) {
	long := "/tmp/" + strings.Repeat("d", 120)
	dir := ResolveBridgeSocketDir(long)
	if strings.HasPrefix(dir, long) {
		t.Errorf("超长路径应回退到 TempDir: %q", dir)
	}
	short := t.TempDir()
	dir2 := ResolveBridgeSocketDir(short)
	if !strings.HasPrefix(dir2, short) && len(filepath.Join(short, "acp-sockets"))+30 <= 100 {
		t.Errorf("短路径应使用 preferred 目录: %q", dir2)
	}
}
