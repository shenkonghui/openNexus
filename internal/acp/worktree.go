package acp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// WorktreesDir 是存放各任务 worktree 的目录名，位于仓库根下。
const WorktreesDir = ".worktrees"

// ErrNotGitRepo 表示给定路径不是一个 git 仓库。
var ErrNotGitRepo = errors.New("路径不是 git 仓库")

// IsGitRepo 检测 path 是否为 git 仓库（含 .git 目录或文件）。
func IsGitRepo(path string) bool {
	git := filepath.Join(path, ".git")
	if _, err := os.Stat(git); err == nil {
		return true
	}
	// git submodule / worktree 中 .git 可能是文件
	if info, err := os.Stat(git); err == nil && !info.IsDir() {
		return true
	}
	return false
}

// GitRoot 返回 path 所在 git 仓库的根目录；非仓库返回 ErrNotGitRepo。
// 优先使用 git rev-parse，能正确处理 worktree（公共 .git）的情况。
func GitRoot(path string) (string, error) {
	if !IsGitRepo(path) {
		// 尝试向上查找（path 可能本身是 worktree）
		cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
		if out, err := cmd.Output(); err == nil {
			return strings.TrimSpace(string(out)), nil
		}
		return "", ErrNotGitRepo
	}
	// 仍以 rev-parse 为准，能正确处理 worktree 指向公共 .git 的情况
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return path, nil
}

// runGit 在 repoPath 下执行 git 命令，返回合并后的 stderr 错误。
func runGit(repoPath string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// CreateWorktree 在 repoPath 仓库中创建一个新分支 branch 并检出到 destPath。
// 若 destPath 已存在则返回错误。base 为空时从当前 HEAD 创建。
func CreateWorktree(repoPath, branch, destPath, base string) error {
	root, err := GitRoot(repoPath)
	if err != nil {
		return err
	}
	if destPath == "" {
		return fmt.Errorf("destPath 不能为空")
	}
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("worktree 目录已存在: %s", destPath)
	}

	args := []string{"worktree", "add", "-b", branch, destPath}
	if base != "" {
		args = append(args, base)
	}
	if err := runGit(root, args...); err != nil {
		return err
	}
	// 清除 worktree 内由编排引擎管理的运行时状态文件。
	// git worktree add 会从 HEAD 检出被跟踪文件；若历史上 tasks.json 等曾被提交进仓库，
	// 每个 worktree 都会带上一份副本，导致主工作区的 tasks.json 与 worktree 副本分裂
	// （主列表看不到任务、状态不一致）。这里在创建后无条件移除，保证 worktree 干净。
	// 删除失败不阻断 worktree 创建（缺失这些文件不影响任务执行）。
	removeTaskManagerRuntimeFiles(destPath)
	return nil
}

// removeTaskManagerRuntimeFiles 删除 worktree 目录内编排引擎的运行时状态文件。
// 这些文件是工作区私有的，不应进入各任务 worktree。缺失即正常。
func removeTaskManagerRuntimeFiles(worktreeDir string) {
	// tasks.json 及其备份
	for _, name := range []string{"tasks.json", "tasks.json.bak"} {
		_ = os.Remove(filepath.Join(worktreeDir, name))
	}
	// 执行记录目录（.openNexus/scheduled-executions.jsonl 等）
	_ = os.RemoveAll(filepath.Join(worktreeDir, ".openNexus"))
}

// RemoveWorktree 移除 destPath 对应的 worktree，并删除其分支 branch（force）。
// 容错：worktree 已不存在时视为成功。
func RemoveWorktree(repoPath, destPath, branch string) error {
	root, err := GitRoot(repoPath)
	if err != nil {
		return err
	}
	// 移除 worktree（--force 以应对有未提交改动的情况）
	if err := runGit(root, "worktree", "remove", "--force", destPath); err != nil {
		// 若目录已被手动删除，prune 后忽略错误
		_ = runGit(root, "worktree", "prune")
		if _, statErr := os.Stat(destPath); statErr == nil {
			return err
		}
	}
	// 删除分支（可能不存在或为当前分支，忽略错误）
	if branch != "" {
		_ = runGit(root, "branch", "-D", branch)
	}
	return nil
}

// WorktreePath 返回仓库根下 .worktrees/<name> 的绝对路径。
func WorktreePath(repoRoot, name string) string {
	return filepath.Join(repoRoot, WorktreesDir, name)
}

// SanitizeWorktreeName 把任意文本（如 AI 输出或 prompt 首行）清洗为合法的
// git 分支名 / worktree 目录名：取首行、去引号，非法字符替换为 '-'，
// 保留 unicode 字母数字（中文可用），并截断到 40 个字符。清洗后为空返回 ""。
func SanitizeWorktreeName(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.Trim(s, "`\"'“”‘’《》【】")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	// git ref 不允许 ".."；折叠连续 '-'，去掉首尾 '-' 与 '.'
	out = strings.ReplaceAll(out, "..", "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-.")
	out = strings.TrimSuffix(out, ".lock")
	if r := []rune(out); len(r) > 40 {
		out = strings.Trim(string(r[:40]), "-.")
	}
	return out
}

// NormalizeBranchName 把任意文本（如 AI 输出）规范化为带 feat/ 或 fix/ 前缀的合法分支名：
// 识别并保留 feat|fix|feature|bugfix|hotfix 前缀（feature 归一为 feat、bugfix/hotfix 归一为 fix），
// 剩余部分经 SanitizeWorktreeName 清洗；无前缀时默认补 feat/。清洗后为空返回 ""。
func NormalizeBranchName(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.Trim(s, "`\"'“”‘’《》【】")
	prefix := "feat"
	lower := strings.ToLower(s)
	for _, p := range []struct{ raw, norm string }{
		{"feature", "feat"}, {"feat", "feat"},
		{"bugfix", "fix"}, {"hotfix", "fix"}, {"fix", "fix"},
	} {
		for _, sep := range []string{"/", "-", "_", ":", " "} {
			if strings.HasPrefix(lower, p.raw+sep) {
				prefix = p.norm
				s = s[len(p.raw)+len(sep):]
				goto matched
			}
		}
	}
matched:
	name := SanitizeWorktreeName(s)
	if name == "" {
		return ""
	}
	return prefix + "/" + name
}

// branchExists 报告仓库中是否已存在本地分支 branch。
func branchExists(repoRoot, branch string) bool {
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// CurrentBranch 返回 path（可为 worktree 目录）当前检出的分支名；
// 处于游离 HEAD 或非 git 目录时返回空串（best-effort，不报错）。
func CurrentBranch(path string) string {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" { // 游离 HEAD
		return ""
	}
	return branch
}

// UniqueWorktreeName 在 name 基础上生成不冲突的 worktree 名：
// 若 .worktrees/<name> 目录或同名分支已存在，则依次尝试 name-2、name-3…
func UniqueWorktreeName(repoRoot, name string) string {
	candidate := name
	for i := 2; ; i++ {
		_, statErr := os.Stat(WorktreePath(repoRoot, candidate))
		if os.IsNotExist(statErr) && !branchExists(repoRoot, candidate) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name, i)
	}
}

// WorktreeInfo 描述一个 git worktree（来自 `git worktree list --porcelain`）。
type WorktreeInfo struct {
	Path     string `json:"path"`      // worktree 绝对路径
	Branch   string `json:"branch"`    // 分支短名（如 feature-x）；detached/bare 时为空
	HEAD     string `json:"head"`      // 提交 SHA
	IsMain   bool   `json:"is_main"`   // 是否主 worktree（列表第一条）
	Detached bool   `json:"detached"`  // HEAD 游离（未在分支上）
	Bare     bool   `json:"bare"`      // 裸仓库
}

// ListWorktrees 返回 repoPath 所在仓库的全部 worktree。
// 解析 `git worktree list --porcelain` 输出；非 git 仓库返回 ErrNotGitRepo。
func ListWorktrees(repoPath string) ([]WorktreeInfo, error) {
	root, err := GitRoot(repoPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "-C", root, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktreeList(string(out)), nil
}

// parseWorktreeList 解析 porcelain 格式输出。
// 格式：每条记录以 "worktree <path>" 开头，后跟 HEAD/branch/detached/bare 属性，空行分隔。
func parseWorktreeList(out string) []WorktreeInfo {
	var list []WorktreeInfo
	var cur *WorktreeInfo
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
			// 记录外的属性行，忽略
		case strings.HasPrefix(line, "HEAD "):
			cur.HEAD = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			// refs/heads/feature-x -> feature-x
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		case line == "":
			flush()
		}
	}
	flush()
	// porcelain 输出第一条即主 worktree
	if len(list) > 0 {
		list[0].IsMain = true
	}
	return list
}

// EnsureWorktreesDir 确保仓库根下的 .worktrees 目录存在。
func EnsureWorktreesDir(repoRoot string) error {
	return os.MkdirAll(filepath.Join(repoRoot, WorktreesDir), 0o755)
}

// hasCommit 报告 path 所在仓库是否已有可用的 HEAD 提交。
// git worktree add 需要基于某个提交创建，空仓库（无提交）会失败，故初始化时需先建初始提交。
func hasCommit(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--verify", "HEAD")
	return cmd.Run() == nil
}

// ensureInitialCommit 若仓库尚无提交，则将现有文件全部暂存并创建初始提交。
// 附带最小 author 信息，避免宿主机未配置 user.name/user.email 时提交失败。
func ensureInitialCommit(path string) error {
	if hasCommit(path) {
		return nil
	}
	// 暂存现有文件（若目录本就有内容），使各任务 worktree 能包含项目文件；无内容时允许空提交。
	_ = runGit(path, "add", "-A")
	return runGit(path,
		"-c", "user.email=nexus@local",
		"-c", "user.name=NexusAgent",
		"commit", "--allow-empty", "-m", "chore: initialize repository for task manager",
	)
}

// GitInit 在 path 下初始化 git 仓库并确保存在初始提交（供 worktree 创建）。
// 若 path 已是 git 仓库则仅补齐初始提交（幂等）。
func GitInit(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path 不能为空")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("创建目录: %w", err)
	}
	if !IsGitRepo(path) {
		if err := runGit(path, "init"); err != nil {
			return err
		}
	}
	return ensureInitialCommit(path)
}
