// Package workspacemeta 计算工作区"管理数据目录"（meta dir）的存放位置。
//
// 管理数据（tasks.json、定时任务执行记录、上传文件等）与 agent 工作目录（cwd）分离，
// 统一落在 <root>/<base(cwd)>-<sha1(cwd)前8位>/ 下，避免污染用户代码仓库。
// root 由主程序启动时通过 SetRoot 注入（来自 config 的 agents.workspace.meta_dir）；
// 未设置 root 时 DirFor 回退返回 cwd 本身，保持旧行为（管理数据落在工作目录内），
// 便于单测与降级运行。
package workspacemeta

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"
)

var (
	mu   sync.RWMutex
	root string
)

// SetRoot 设置管理数据根目录（绝对路径）。传空串表示清除（回退旧行为）。
func SetRoot(dir string) {
	mu.Lock()
	defer mu.Unlock()
	root = dir
}

// Root 返回当前管理数据根目录，未设置时为空串。
func Root() string {
	mu.RLock()
	defer mu.RUnlock()
	return root
}

// DirFor 返回 cwd 对应的管理数据目录。
// root 未设置时返回 cwd 本身（旧行为）；否则按清洗后的绝对 cwd 计算稳定映射：
// <root>/<base(cwd)>-<sha1(cwd)前8位>，同一 cwd（含尾斜杠等表示差异）始终映射到同一目录。
func DirFor(cwd string) string {
	r := Root()
	if r == "" || cwd == "" {
		return cwd
	}
	key := normalize(cwd)
	sum := sha1.Sum([]byte(key))
	hash8 := hex.EncodeToString(sum[:])[:8]
	return filepath.Join(r, fmt.Sprintf("%s-%s", filepath.Base(key), hash8))
}

// UploadsDirFor 返回 cwd 对应管理数据目录下的上传文件目录。
// root 未设置时回退到旧位置 <cwd>/.uploads。
func UploadsDirFor(cwd string) string {
	dir := DirFor(cwd)
	if dir == cwd {
		return filepath.Join(cwd, ".uploads")
	}
	return filepath.Join(dir, "uploads")
}

// normalize 把 cwd 规整为稳定键：绝对化 + Clean，消除尾斜杠、相对段等表示差异。
func normalize(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return filepath.Clean(cwd)
	}
	return filepath.Clean(abs)
}
