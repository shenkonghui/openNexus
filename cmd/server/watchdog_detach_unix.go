//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// applyWatchdogDetach 让 watchdog 子进程独立成新进程组并脱离父进程，
// 使其生命周期与主 server 解耦（主 server 退出后 watchdog 由 init/launchd 接管）。
func applyWatchdogDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processAliveByPID 判断指定 PID 的进程是否存活（signal 0 探测）。
func processAliveByPID(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// killProcessByPID 向指定 PID 发送 SIGTERM，超时后 SIGKILL。
func killProcessByPID(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
