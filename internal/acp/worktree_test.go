package acp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAndRemoveWorktree(t *testing.T) {
	// 创建一个临时 git 仓库
	repo := t.TempDir()
	git := func(args ...string) error {
		return runGit(repo, args...)
	}
	if err := git("init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := git("config", "user.email", "t@t.com"); err != nil {
		t.Fatalf("config email: %v", err)
	}
	if err := git("config", "user.name", "tester"); err != nil {
		t.Fatalf("config name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git("add", "."); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := git("commit", "-m", "init"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := EnsureWorktreesDir(repo); err != nil {
		t.Fatalf("EnsureWorktreesDir: %v", err)
	}
	wt := WorktreePath(repo, "t1")

	// 创建 worktree
	if err := CreateWorktree(repo, "task-t1", wt, ""); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if !IsGitRepo(wt) {
		t.Fatalf("worktree 应是 git 仓库")
	}
	// 重复创建应报错（分支已存在）
	if err := CreateWorktree(repo, "task-t1", wt+"2", ""); err == nil {
		t.Fatalf("重复分支应报错")
	}

	// GitRoot 从 worktree 能解析出主仓库根
	root, err := GitRoot(wt)
	if err != nil {
		t.Fatalf("GitRoot: %v", err)
	}
	// worktree 的 toplevel 指向自身，验证返回非空即可
	if root == "" {
		t.Fatalf("GitRoot 返回空")
	}

	// 移除 worktree
	if err := RemoveWorktree(repo, wt, "task-t1"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatalf("worktree 目录应已被移除")
	}
}

func TestCreateWorktree_Hardened(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) error { return runGit(repo, args...) }
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t.com"},
		{"config", "user.name", "tester"},
		{"remote", "add", "origin", "https://example.com/fake.git"},
	} {
		if err := git(args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git("add", "."); err != nil {
		t.Fatal(err)
	}
	if err := git("commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	wt := WorktreePath(repo, "h1")
	if err := CreateWorktree(repo, "task-h1", wt, ""); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	gitOut := func(dir string, args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			t.Fatalf("git -C %s %v: %v", dir, args, err)
		}
		return strings.TrimSpace(string(out))
	}
	// worktree 内 pushurl 被禁用
	if got := gitOut(wt, "config", "remote.origin.pushurl"); got != disabledPushURL {
		t.Fatalf("worktree pushurl 应为 %s, got=%q", disabledPushURL, got)
	}
	// pre-push hook 就位且 hooksPath 指向私有 git dir
	hooksPath := gitOut(wt, "config", "core.hooksPath")
	if _, err := os.Stat(filepath.Join(hooksPath, "pre-push")); err != nil {
		t.Fatalf("pre-push hook 缺失: %v", err)
	}
	// 主仓库配置不被污染：pushurl 仅 worktree 级
	if out, err := exec.Command("git", "-C", repo, "config", "remote.origin.pushurl").Output(); err == nil {
		t.Fatalf("主仓库不应有 pushurl: %q", string(out))
	}
	// git push 在 worktree 内应失败
	if err := runGit(wt, "push", "origin", "HEAD"); err == nil {
		t.Fatalf("worktree 内 git push 应失败")
	}
}
