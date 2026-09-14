package acp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
)

// FileWriteNotify 是 WriteTextFile 回调推送给 Prompt 流的文件改动事件。
type FileWriteNotify struct {
	SessionId acp.SessionId
	Path      string // 绝对路径
	OldText   string // 旧内容；IsNew=true 时为空
	NewText   string // 新内容
	IsNew     bool   // 是否为新建文件
	// NoOldSnapshot 标记"修改过但旧内容未捕获"（快照超内容预算）。
	// 撤销/展示必须区分它和新文件：两者 OldText 均为空，
	// 但按新文件处理会误删一个仍有内容的已修改文件。
	NoOldSnapshot bool
}

// fileRecorder 管理 ACP WriteTextFile 回调与 Prompt 流之间的文件改动事件桥接。
// 设计与 permissionBroker 一致：每个会话注册一个 buffered channel，
// WriteTextFile 写入文件后非阻塞推送事件，Prompt 流消费并产出 tool_call_update 消息。
type fileRecorder struct {
	mu      sync.Mutex
	waiters map[acp.SessionId]chan FileWriteNotify
}

func newFileRecorder() *fileRecorder {
	return &fileRecorder{
		waiters: make(map[acp.SessionId]chan FileWriteNotify),
	}
}

// RegisterFileWaiter 注册一个文件改动监听（Prompt 流期间调用），返回事件 channel。
func (r *fileRecorder) registerWaiter(sessionID acp.SessionId) chan FileWriteNotify {
	ch := make(chan FileWriteNotify, 64)
	r.mu.Lock()
	r.waiters[sessionID] = ch
	r.mu.Unlock()
	return ch
}

// unregisterWaiter 注销并关闭文件改动监听。
func (r *fileRecorder) unregisterWaiter(sessionID acp.SessionId) {
	r.mu.Lock()
	ch, ok := r.waiters[sessionID]
	if ok {
		delete(r.waiters, sessionID)
	}
	r.mu.Unlock()
	if ok {
		close(ch)
	}
}

// record 非阻塞推送文件改动事件给该会话的监听者。
// 无监听者（Prompt 流未激活）或 buffer 满时丢弃并记日志，避免阻塞 WriteTextFile 回调。
func (r *fileRecorder) record(notify FileWriteNotify) {
	r.mu.Lock()
	ch := r.waiters[notify.SessionId]
	r.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- notify:
	default:
		slog.Warn("fileRecorder 监听者 buffer 满，丢弃文件改动", "session", notify.SessionId, "path", notify.Path)
	}
}

// MapFileWrite 将文件改动事件映射为 models.Message。
// 产出一条 tool_call_update 消息，其 raw_json 含 content[{type:"diff",path,oldText,newText}]，
// 使前端 parseDiffsFromMessage 可直接解析。
func MapFileWrite(sessionID string, dbSessionID uint, seq int, notify FileWriteNotify) models.Message {
	base := filepath.Base(notify.Path)
	title := fmt.Sprintf("Edit %s", base)

	// 构造与 ACP SessionToolCallUpdate 一致的扁平 JSON 结构。
	// content[] 中的 diff 项：oldText 仅在非新文件时包含（ACP 中 *string，nil 表示新文件）。
	diffItem := map[string]any{
		"type":    "diff",
		"path":    notify.Path,
		"newText": notify.NewText,
	}
	if !notify.IsNew {
		diffItem["oldText"] = notify.OldText
		if notify.NoOldSnapshot {
			diffItem["noOldSnapshot"] = true
		}
	}

	payload := map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    fmt.Sprintf("file-%d", seq),
		"status":        "completed",
		"title":         title,
		"kind":          "edit",
		"content":       []map[string]any{diffItem},
	}
	raw, _ := json.Marshal(payload)

	return models.Message{
		SessionID:   sessionID,
		DBSessionID: dbSessionID,
		Role:        models.MessageRoleTool,
		Kind:        models.MessageKindToolCallUpdate,
		Content:     title,
		RawJSON:     string(raw),
		Sequence:    seq,
	}
}

// MapFileWriteBatch 将多个文件改动事件合并为一条 tool_call_update 消息。
// 用于快照对比场景：一次 prompt 产生的所有文件变更汇总到一条消息中。
func MapFileWriteBatch(sessionID string, dbSessionID uint, seq int, notifies []FileWriteNotify) models.Message {
	diffItems := make([]map[string]any, 0, len(notifies))
	for _, n := range notifies {
		item := map[string]any{
			"type":    "diff",
			"path":    n.Path,
			"newText": n.NewText,
		}
		if !n.IsNew {
			item["oldText"] = n.OldText
			if n.NoOldSnapshot {
				item["noOldSnapshot"] = true
			}
		}
		diffItems = append(diffItems, item)
	}

	title := fmt.Sprintf("文件改动 ×%d", len(notifies))
	payload := map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    fmt.Sprintf("snapshot-%d", seq),
		"status":        "completed",
		"title":         title,
		"kind":          "edit",
		"content":       diffItems,
	}
	raw, _ := json.Marshal(payload)

	return models.Message{
		SessionID:   sessionID,
		DBSessionID: dbSessionID,
		Role:        models.MessageRoleTool,
		Kind:        models.MessageKindToolCallUpdate,
		Content:     title,
		RawJSON:     string(raw),
		Sequence:    seq,
	}
}
