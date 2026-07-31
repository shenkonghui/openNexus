//go:build linux

package acp

import (
	"os"
	"os/exec"
	"path/filepath"
)

// sandboxWrapPlatform Linux 实现：用 bubblewrap（bwrap）包裹 argv。
// 语义：根文件系统整体只读绑定，WriteDirs 逐条读写绑定（路径不变，agent 无感知）；
// 不隔离网络（agent 需访问 LLM API）。bwrap 缺失时降级直通（Docker 镜像内已预装）。
func sandboxWrapPlatform(argv []string, p SandboxProfile) ([]string, bool) {
	exe, err := exec.LookPath("bwrap")
	if err != nil {
		return argv, true
	}
	args := []string{exe,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--die-with-parent",
	}
	seen := make(map[string]bool)
	for _, dir := range p.WriteDirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		// 目录不存在时跳过（bwrap 对缺失源路径直接报错退出）
		if st, statErr := os.Stat(dir); statErr != nil || !st.IsDir() {
			continue
		}
		args = append(args, "--bind", dir, dir)
		// 符号链接（如 /tmp 指向别处的发行版）额外绑定真实路径
		if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil && resolved != dir && !seen[resolved] {
			seen[resolved] = true
			args = append(args, "--bind", resolved, resolved)
		}
	}
	args = append(args, "--")
	return append(args, argv...), false
}
