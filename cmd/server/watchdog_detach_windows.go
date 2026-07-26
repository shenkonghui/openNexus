//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// applyWatchdogDetach 在 Windows 上暂为空操作（独立进程组概念不适用）。
func applyWatchdogDetach(cmd *exec.Cmd) {}

// processAliveByPID 在 Windows 上简化实现。
func processAliveByPID(pid int) bool { return pid > 0 }

// killProcessByPID 在 Windows 上用 taskkill 终止进程。
func killProcessByPID(pid int) {
	_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
}
