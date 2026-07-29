package acp

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// probeCacheFile 是探测结果持久化到磁盘的 JSON 格式。
type probeCacheFile struct {
	Agents    map[string][]acp.SessionConfigOption `json:"agents"`
	UpdatedAt time.Time                            `json:"updated_at"`
}

// loadProbeCacheFromFile 从磁盘加载已持久化的探测缓存。
// 文件不存在或解析失败时返回 nil（不影响正常启动）。
func loadProbeCacheFromFile(path string) map[string][]acp.SessionConfigOption {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f probeCacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("加载模型缓存文件失败", "path", path, "err", err)
		return nil
	}
	return f.Agents
}

// persistProbeCacheToFile 将指定 agent 类型的探测结果持久化到磁盘。
// 采用"读取-合并-写入"策略，仅更新该 agent 的条目，其他 agent 的缓存保持不变。
// 通过临时文件 + rename 实现原子写入，避免并发读写损坏。
func persistProbeCacheToFile(path, agentType string, opts []acp.SessionConfigOption) {
	if path == "" {
		return
	}
	// 读取现有文件并合并
	existing := loadProbeCacheFromFile(path)
	if existing == nil {
		existing = make(map[string][]acp.SessionConfigOption)
	}
	existing[agentType] = opts

	f := probeCacheFile{
		Agents:    existing,
		UpdatedAt: time.Now(),
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		slog.Debug("序列化模型缓存失败", "err", err)
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Debug("创建模型缓存目录失败", "err", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Debug("写入模型缓存临时文件失败", "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		slog.Debug("重命名模型缓存文件失败", "err", err)
	}
}
