//go:build !windows

package acp

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// setProcessGroup 让子进程成为新会话的组长（PGID == SID == 子进程 PID），
// 以便 Stop() 时通过负 PID 向整个进程组发送信号，杀掉 agent 派生的孙进程。
// Setsid 创建新会话，使子进程脱离父进程的控制终端——否则像 codebuddy-code 这类
// 启动时会操作 TTY（交互式登录提示）的 agent，在 server 于前台终端运行时会被
// 作业控制信号（SIGTSTP）挂起，导致 ACP 握手永远得不到响应。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminateProcessGroup 先向进程组发 SIGTERM，等待直系子进程退出；
// 若 grace 超时仍未退出则升级为 SIGKILL 强制终止整个进程组（含孙进程）。
// 调用方不应再调用 proc.Wait()——本方法已回收直系子进程。
func terminateProcessGroup(proc *os.Process, grace time.Duration) error {
	pgid := proc.Pid

	// SIGTERM 先发，给 agent 自行清理子进程的机会。
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	// 监听直系子进程退出；超时则 SIGKILL 整组强制终止。
	waitCh := make(chan error, 1)
	go func() {
		_, err := proc.Wait()
		waitCh <- err
	}()
	select {
	case <-waitCh:
		return nil
	case <-time.After(grace):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waitCh
		return nil
	}
}

// probeProcessState 探测直系子进程（cmd.Process，通常是 npm exec 等 wrapper）的状态。
// 返回值：
//   - procStateExited：直系子进程已终止（agent 大概率也已退出）
//   - procStateRunning：直系子进程仍在运行（agent 可能正常、无响应或被信号停止）
//
// 注意：被信号停止（SIGTSTP）的通常是 wrapper 派生的孙进程（真正的 agent），
// 父进程无法用 Wait4 探测孙进程状态，故此处不区分 stopped。
// 「进程存活但握手无响应」的诊断由 unresponsiveDiagnosis 给出，其中会提示
// 可能被作业控制挂起等待用户检查。
func probeProcessState(pid int) procState {
	// signal 0 不实际发送信号，仅做存在性检查；返回 ESRCH 表示进程已退出。
	if err := syscall.Kill(pid, 0); err != nil {
		return procStateExited
	}
	return procStateRunning
}

// orphanGrace 是 KillProcessGroup 发送 SIGTERM 后等待进程退出的宽限时间，
// 超过后升级为 SIGKILL。
const orphanGrace = 2 * time.Second

// KillProcessGroup 杀掉指定进程组的全部进程（含孙进程）。
// pid 应为进程组 PGID；由于子进程用 Setsid 独立成会话，PGID 等于直系子进程 PID。
// pid <= 0 时视为无效，直接返回。
func KillProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	pgid := pid
	// 先 SIGTERM 给自行清理机会
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	// 轮询等待宽限期内退出
	deadline := time.Now().Add(orphanGrace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil {
			return nil // 进程组已不存在
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 超时强制 SIGKILL 整组
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return nil
}

// orphanACPKeywords 是识别 openNexus 拉起的 acp 孤儿进程的命令行特征。
// 覆盖三类后端：binary（.openNexus/binaries）、codebuddy、npx wrapper。
var orphanACPKeywords = []string{
	".openNexus/binaries",
	"codebuddy-code",
	"_npx",
}

// isOrphanACPCommandLine 判断命令行是否匹配 acp 孤儿特征（带 --acp 的 agent，
// 或 acp-bridge 守护进程——杀其进程组可一并回收挂在其下的 agent）。
// 沙箱模式下 wrapper（sandbox-exec/bwrap）的命令行包含完整内层 argv，
// 子串匹配不依赖 argv[0]，天然覆盖沙箱前缀场景。
func isOrphanACPCommandLine(cmdline string) bool {
	if strings.Contains(cmdline, "acp-bridge") && strings.Contains(cmdline, "--socket") {
		return true
	}
	if !strings.Contains(cmdline, "--acp") {
		return false
	}
	for _, kw := range orphanACPKeywords {
		if strings.Contains(cmdline, kw) {
			return true
		}
	}
	return false
}

// KillOrphanACPProcesses 扫描系统进程，杀掉命令行匹配 acp 特征的孤儿进程组。
// 用于主程序退出兜底与 watchdog 在主程序死亡时的全量清理。
// 只杀进程组（避免误杀当前自身进程）：跳过 PID 等于当前进程的条目。
func KillOrphanACPProcesses() (int, error) {
	self := os.Getpid()
	// ps 输出 pid + command，用足够长的宽度避免截断长命令行
	out, err := exec.Command("ps", "-eo", "pid=,command=").Output()
	if err != nil {
		return 0, fmt.Errorf("列出进程失败: %w", err)
	}
	killed := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 格式: "<pid> <command...>"
		sp := bytes.IndexByte([]byte(line), ' ')
		if sp < 0 {
			continue
		}
		pidStr := strings.TrimSpace(line[:sp])
		cmdline := strings.TrimSpace(line[sp+1:])
		pid, perr := strconv.Atoi(pidStr)
		if perr != nil || pid <= 0 || pid == self {
			continue
		}
		if !isOrphanACPCommandLine(cmdline) {
			continue
		}
		// 尝试取进程组 PGID（ps opgrpg），失败则退回用 pid 作为 pgid
		pgid := pid
		if g, gerr := getProcessGroupID(pid); gerr == nil && g > 0 {
			pgid = g
		}
		slog.Info("清理 acp 孤儿进程", "pid", pid, "pgid", pgid, "cmd", cmdline)
		if err := KillProcessGroup(pgid); err != nil {
			slog.Warn("杀进程组失败", "pgid", pgid, "err", err)
			continue
		}
		killed++
	}
	return killed, nil
}

// getProcessGroupID 返回指定 pid 的进程组 PGID。
func getProcessGroupID(pid int) (int, error) {
	out, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	g, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, err
	}
	return g, nil
}

// ProcessAlive 判断指定 PID 的进程是否仍在运行（signal 0 探测）。
// watchdog 在杀进程前调用，避免对已退出进程发信号。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
