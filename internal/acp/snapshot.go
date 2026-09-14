package acp

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 快照相关常量。
const (
	maxSnapshotFileSize = 512 * 1024 // 512KB：超过此大小的文件不捕获内容
	snapshotBinaryCheck = 8 * 1024   // 检测二进制时读取的前 N 字节
	// 内容捕获预算：超过预算的文件只记元数据（仍能报出变更，只是无 diff 内容/不可撤销）。
	// 防止 persistent workspace 指向大项目时每轮 prompt 全量读盘卡死请求路径。
	maxSnapshotContentFiles = 2000
	maxSnapshotContentBytes = 32 << 20 // 32MB
)

// snapshotIgnoreDirs 是快照遍历时跳过的目录名（与 filesystem_handler 一致）。
var snapshotIgnoreDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	".next": true, "__pycache__": true, ".venv": true, "vendor": true,
	".openNexus": true, ".claude": true,
}

// snapshotMeta 是单文件的元数据指纹（size+mtime），仅 stat 不读内容，遍历廉价。
type snapshotMeta struct {
	size  int64
	mtime time.Time
}

// dirSnapshot 是工作区快照：
//   - meta 覆盖全部文件：变更检测靠它，任意大的工作区都只花一次 stat 遍历；
//   - contents 仅在预算内捕获文本文件内容：供 diff 展示与撤销使用。
//     超预算的文件变更仍能报出（meta 不同），只是 OldText/NewText 为空。
type dirSnapshot struct {
	meta     map[string]snapshotMeta
	contents map[string]string
}

// takeSnapshot 递归遍历 cwd。所有文件记元数据；文本文件在预算内再读内容。
// cwd 为空或不存在时返回空快照。
func takeSnapshot(cwd string) *dirSnapshot {
	snap := &dirSnapshot{
		meta:     map[string]snapshotMeta{},
		contents: map[string]string{},
	}
	if cwd == "" {
		return snap
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return snap
	}

	contentFiles := 0
	contentBytes := 0
	_ = filepath.WalkDir(cwd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无法访问的路径
		}
		name := d.Name()

		// 跳过隐藏文件/目录
		if strings.HasPrefix(name, ".") && path != cwd {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if snapshotIgnoreDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		fi, e := d.Info()
		if e != nil {
			return nil
		}
		rel, e := filepath.Rel(cwd, path)
		if e != nil {
			return nil
		}
		snap.meta[rel] = snapshotMeta{size: fi.Size(), mtime: fi.ModTime()}

		// 内容捕获：预算内且为文本文件才读
		if fi.Size() > maxSnapshotFileSize ||
			contentFiles >= maxSnapshotContentFiles ||
			contentBytes+int(fi.Size()) > maxSnapshotContentBytes {
			return nil
		}
		if !isTextFile(path) {
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		snap.contents[rel] = string(data)
		contentFiles++
		contentBytes += len(data)
		return nil
	})
	return snap
}

// isTextFile 通过检测前 snapshotBinaryCheck 字节中是否含 NULL 字节来判断是否为文本文件。
func isTextFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, snapshotBinaryCheck)
	n, _ := f.Read(buf)
	if n == 0 {
		return true // 空文件视为文本
	}
	return !bytes.Contains(buf[:n], []byte{0})
}

// compareSnapshots 对比前后快照，返回变更文件的 FileWriteNotify 列表。
// 仅检测新增和修改的文件（删除暂不纳入展示）。
// 变更判定用元数据（全量覆盖）；OldText/NewText 仅当对应侧有内容捕获时填充——
// 超预算文件的变更会报出但没有 diff 内容（也无法按 oldText 撤销）。
// 返回的 Path 为相对 cwd 的路径。
func compareSnapshots(before, after *dirSnapshot) []FileWriteNotify {
	var diffs []FileWriteNotify
	for rel, newMeta := range after.meta {
		oldMeta, existed := before.meta[rel]
		if !existed {
			diffs = append(diffs, FileWriteNotify{
				Path:    rel,
				NewText: after.contents[rel],
				IsNew:   true,
			})
			continue
		}
		if oldMeta != newMeta {
			oldText, oldCaptured := before.contents[rel]
			diffs = append(diffs, FileWriteNotify{
				Path:          rel,
				OldText:       oldText,
				NewText:       after.contents[rel],
				NoOldSnapshot: !oldCaptured,
			})
		}
	}
	return diffs
}
