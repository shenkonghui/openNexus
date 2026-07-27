package workspacemeta

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDirFor_NoRootFallsBackToCwd 验证 root 未设置时回退返回 cwd 本身。
func TestDirFor_NoRootFallsBackToCwd(t *testing.T) {
	SetRoot("")
	if got := DirFor("/tmp/ws"); got != "/tmp/ws" {
		t.Errorf("DirFor = %q, 期望回退 /tmp/ws", got)
	}
	if got := UploadsDirFor("/tmp/ws"); got != filepath.Join("/tmp/ws", ".uploads") {
		t.Errorf("UploadsDirFor = %q, 期望旧位置 /tmp/ws/.uploads", got)
	}
}

// TestDirFor_StableMapping 验证同一 cwd（含尾斜杠差异）映射稳定，不同 cwd 不冲突。
func TestDirFor_StableMapping(t *testing.T) {
	SetRoot("/data/workspaces")
	t.Cleanup(func() { SetRoot("") })

	a := DirFor("/home/u/repo")
	b := DirFor("/home/u/repo/")
	if a != b {
		t.Errorf("尾斜杠差异导致映射不同: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "/data/workspaces/repo-") {
		t.Errorf("DirFor = %q, 期望前缀 /data/workspaces/repo-", a)
	}
	other := DirFor("/home/u/other-repo")
	if other == a {
		t.Errorf("不同 cwd 映射冲突: %q", a)
	}
	// 同名不同路径不冲突
	sameBase := DirFor("/srv/u2/repo")
	if sameBase == a {
		t.Errorf("同 base 不同路径映射冲突: %q", a)
	}
}

// TestUploadsDirFor_WithRoot 验证 root 设置后 uploads 位于管理数据目录下。
func TestUploadsDirFor_WithRoot(t *testing.T) {
	SetRoot("/data/workspaces")
	t.Cleanup(func() { SetRoot("") })

	got := UploadsDirFor("/home/u/repo")
	if got != filepath.Join(DirFor("/home/u/repo"), "uploads") {
		t.Errorf("UploadsDirFor = %q, 期望位于管理数据目录下", got)
	}
}
