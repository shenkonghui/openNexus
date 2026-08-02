// Package sysutil 提供与运行环境相关的跨进程辅助逻辑。
//
// 典型用途：在启动早期扩充当前进程的 PATH，使 GUI/launchd 启动的后端
// 也能找到 nvm、Homebrew、pyenv 等安装的命令（如 npm/node）。
package sysutil

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// EnrichPath 扩充当前进程的 PATH，补齐登录 shell 与常见工具目录中的条目。
//
// 背景：在 macOS 上，由 launchd 或桌面客户端（Electron）启动的进程继承的是
// 最小化 PATH（约 /usr/bin:/bin:/usr/sbin:/sbin），不包含 /opt/homebrew/bin、
// ~/.nvm/.../bin、~/.local/bin 等。这会导致通过 npm exec 启动的 agent 子进程
// 报错 "npm: executable file not found in $PATH"，尽管终端里 npm 一切正常。
//
// 该函数在启动早期调用一次（早于任何 agent 进程拉起），仅做 best-effort：
// 失败仅 debug 级日志，绝不影响启动。对 Windows 无操作。
//
// 合并策略：新发现的目录前置到现有 PATH 之前（用户登录环境的版本优先），
// 已存在的条目去重后不重复添加。
func EnrichPath() {
	if runtime.GOOS == "windows" {
		return
	}
	current := os.Getenv("PATH")
	extra := discoverExtraPathEntries(current)
	if len(extra) == 0 {
		return
	}
	sep := string(filepath.ListSeparator)
	merged := strings.Join(extra, sep)
	if current == "" {
		_ = os.Setenv("PATH", merged)
	} else {
		_ = os.Setenv("PATH", merged+sep+current)
	}
	slog.Info("已扩充 PATH（来自登录 shell 与常见工具目录）",
		"added", extra, "count", len(extra))
}

// discoverExtraPathEntries 返回需要补充进 PATH 的目录（仅含真实存在的目录，
// 且对 current 已有条目去重）。顺序：先登录 shell 探测结果，再常见工具目录。
func discoverExtraPathEntries(current string) []string {
	currentEntries := splitPath(current)
	inCurrent := make(map[string]bool, len(currentEntries))
	for _, e := range currentEntries {
		inCurrent[e] = true
	}

	seen := make(map[string]bool)
	var result []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || dir == "." {
			return
		}
		// 展开 ~ / ~/...
		if dir == "~" || strings.HasPrefix(dir, "~/") {
			home, herr := os.UserHomeDir()
			if herr != nil {
				return
			}
			dir = filepath.Join(home, dir[1:])
		}
		dir = filepath.Clean(dir)
		if inCurrent[dir] || seen[dir] {
			return
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		seen[dir] = true
		result = append(result, dir)
	}

	// 1. 探测用户登录 shell 的 PATH —— 捕获 nvm / brew shellenv / pyenv 等动态注入。
	//    仅用 -l（login），它会 source ~/.zprofile / ~/.bash_profile；
	//    不用 -i 是为避免交互式 shell 挂起或触发提示符。
	//    仅在 .zshrc/.bashrc 中配置 PATH 的用户由下方硬编码兜底列表覆盖。
	for _, dir := range loginShellPath() {
		add(dir)
	}

	home, _ := os.UserHomeDir()

	// 2. 常见工具目录兜底（覆盖仅在 .zshrc 配置 PATH 的用户）。
	var common []string
	if runtime.GOOS == "darwin" {
		common = append(common,
			"/opt/homebrew/bin",
			"/opt/homebrew/sbin",
			"/usr/local/bin",
		)
	}
	common = append(common,
		filepath.Join(home, ".local/bin"),
		filepath.Join(home, ".npm-global/bin"),
		filepath.Join(home, ".yarn/bin"),
		filepath.Join(home, ".bun/bin"),
		filepath.Join(home, ".deno/bin"),
		filepath.Join(home, ".cargo/bin"),
		filepath.Join(home, "go/bin"),
	)
	// nvm：扫描 ~/.nvm/versions/node/<ver>/bin，取最新版本（目录名字典序最大）。
	nvmNodes := filepath.Join(home, ".nvm", "versions", "node")
	if entries, err := os.ReadDir(nvmNodes); err == nil {
		// 收集后按字典序降序，取第一个存在的
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		for i := len(names) - 1; i >= 0; i-- {
			bin := filepath.Join(nvmNodes, names[i], "bin")
			add(bin)
			break // 仅取最新一个
		}
	}
	for _, dir := range common {
		add(dir)
	}

	return result
}

// loginShellPath 以登录 shell 方式获取 $PATH。
// 使用 $SHELL（回退到 /bin/zsh、/bin/bash、/bin/sh）以 -l -c 运行，
// 3 秒超时防止 ~/.zprofile 挂起阻塞启动；任何失败返回 nil。
func loginShellPath() []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		for _, s := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
			if _, err := os.Stat(s); err == nil {
				shell = s
				break
			}
		}
	}
	if shell == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// printf %s "$PATH"：避免末尾换行，便于直接 split。
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", `printf %s "$PATH"`)
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("查询登录 shell PATH 失败（忽略，使用兜底目录）",
			"shell", shell, "err", err)
		return nil
	}
	return splitPath(string(out))
}

// splitPath 按 OS 列表分隔符切分 PATH，去掉空白与空串。
func splitPath(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	parts := strings.Split(p, string(filepath.ListSeparator))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
