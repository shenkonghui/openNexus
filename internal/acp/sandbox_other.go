//go:build !darwin && !linux

package acp

// sandboxWrapPlatform 其他平台（Windows 等）无 OS 沙箱实现：直通降级。
// 协议层（permission 名单）与执行层（TerminalBridge deny）防线仍然生效。
func sandboxWrapPlatform(argv []string, p SandboxProfile) ([]string, bool) {
	return argv, true
}
