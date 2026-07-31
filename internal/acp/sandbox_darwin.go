//go:build darwin

package acp

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// sandboxWrapPlatform macOS 实现：用 sandbox-exec + 内联 SBPL profile 包裹 argv。
// 语义：默认放行（网络/读全放），仅文件写入整体拒绝后按 WriteDirs 白名单放行。
// sandbox-exec 虽被 Apple 标记 deprecated，但 macOS 15 仍可用（Chrome/Bazel 同款方案）。
func sandboxWrapPlatform(argv []string, p SandboxProfile) ([]string, bool) {
	exe, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return argv, true
	}
	wrapped := append([]string{exe, "-p", buildSBPLProfile(p)}, argv...)
	return wrapped, false
}

// buildSBPLProfile 生成 SBPL profile 文本。
// /dev 必须放行写入（PTY、/dev/null）；WriteDirs 同时包含原始路径与符号链接解析后的
// 真实路径（macOS 的 /tmp → /private/tmp、/var → /private/var，沙箱按真实路径裁决）。
func buildSBPLProfile(p SandboxProfile) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n")
	b.WriteString("(allow file-write*\n")
	b.WriteString("  (subpath \"/dev\")\n")
	seen := map[string]bool{"/dev": true}
	writeSubpath := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		fmt.Fprintf(&b, "  (subpath \"%s\")\n", escapeSBPLString(dir))
	}
	for _, dir := range p.WriteDirs {
		writeSubpath(dir)
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			writeSubpath(resolved)
		}
	}
	b.WriteString(")\n")
	return b.String()
}

// escapeSBPLString 转义 SBPL 字符串字面量中的反斜杠与双引号。
func escapeSBPLString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
