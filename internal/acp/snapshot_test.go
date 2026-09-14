package acp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSnapFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCapturesContentWithinBudget(t *testing.T) {
	dir := t.TempDir()
	writeSnapFile(t, dir, "a.txt", "hello")
	writeSnapFile(t, dir, filepath.Join("sub", "b.txt"), "world")

	snap := takeSnapshot(dir)
	if len(snap.meta) != 2 {
		t.Fatalf("meta should cover all files, got %d", len(snap.meta))
	}
	if snap.contents["a.txt"] != "hello" || snap.contents[filepath.Join("sub", "b.txt")] != "world" {
		t.Fatalf("contents not captured: %v", snap.contents)
	}
}

func TestCompareSnapshotsNewAndModified(t *testing.T) {
	dir := t.TempDir()
	writeSnapFile(t, dir, "keep.txt", "same")
	writeSnapFile(t, dir, "mod.txt", "old")

	before := takeSnapshot(dir)

	// 修改 + 新增
	writeSnapFile(t, dir, "mod.txt", "new-content")
	writeSnapFile(t, dir, "added.txt", "fresh")

	after := takeSnapshot(dir)
	diffs := compareSnapshots(before, after)
	byPath := map[string]FileWriteNotify{}
	for _, d := range diffs {
		byPath[d.Path] = d
	}
	if len(diffs) != 2 {
		t.Fatalf("expect 2 diffs, got %v", diffs)
	}
	if d := byPath["added.txt"]; !d.IsNew || d.NewText != "fresh" {
		t.Fatalf("added.txt diff wrong: %+v", d)
	}
	if d := byPath["mod.txt"]; d.IsNew || d.OldText != "old" || d.NewText != "new-content" || d.NoOldSnapshot {
		t.Fatalf("mod.txt diff wrong: %+v", d)
	}
}

func TestCompareSnapshotsMetaOnlyChange(t *testing.T) {
	// 手工构造"有元数据但无内容捕获"的快照（模拟超内容预算场景）
	mk := func(size int64, content *string) *dirSnapshot {
		s := &dirSnapshot{meta: map[string]snapshotMeta{}, contents: map[string]string{}}
		s.meta["big.txt"] = snapshotMeta{size: size, mtime: time.Unix(1000+size, 0)}
		if content != nil {
			s.contents["big.txt"] = *content
		}
		return s
	}
	before := mk(100, nil)   // 旧文件存在但未捕获内容
	after := mk(200, nil)    // 修改后也未捕获
	diffs := compareSnapshots(before, after)
	if len(diffs) != 1 {
		t.Fatalf("expect 1 diff, got %v", diffs)
	}
	d := diffs[0]
	if d.IsNew || !d.NoOldSnapshot || d.OldText != "" || d.NewText != "" {
		t.Fatalf("meta-only diff wrong: %+v", d)
	}
}
