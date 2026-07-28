package acp

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
)

// goal 评估子框：把评估用临时会话（RunPromptOnce）的输出以 tool_call / tool_call_update
// 消息内嵌到主会话中，前端复用既有工具调用折叠框渲染——多角色会签评估即多个子框。
// 评估通常发生在 prompt 流结束后（无活跃广播者），消息落库后由前端轮询接收。

// goalEvalBoxFlushInterval 是中间增量输出的最小落盘间隔，
// 避免逐 token 落盘刷爆消息表；前端按 toolCallId 合并为单个子框。
const goalEvalBoxFlushInterval = 2 * time.Second

// goalEvalBox 表示主会话中的一个评估子框（对应一次临时会话调用）。
type goalEvalBox struct {
	svc        *Service
	session    *models.Session
	toolCallID string
	title      string

	mu        sync.Mutex
	pending   strings.Builder // 尚未落盘的增量文本
	lastFlush time.Time
}

// startGoalEvalBox 在主会话追加一个 in_progress 的 tool_call 子框并返回句柄。
func (s *Service) startGoalEvalBox(session *models.Session, title string) *goalEvalBox {
	b := &goalEvalBox{
		svc:        s,
		session:    session,
		toolCallID: fmt.Sprintf("goal-eval:%s:%d", session.SessionID, time.Now().UnixNano()),
		title:      title,
		lastFlush:  time.Now(),
	}
	b.persist(acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
		ToolCallId:    acp.ToolCallId(b.toolCallID),
		Title:         title,
		Kind:          acp.ToolKindThink,
		Status:        acp.ToolCallStatusInProgress,
		SessionUpdate: "tool_call",
	}})
	return b
}

// OnText 接收临时会话的流式文本增量：缓冲并按最小间隔落盘为 tool_call_update。
func (b *goalEvalBox) OnText(delta string) {
	b.mu.Lock()
	b.pending.WriteString(delta)
	due := time.Since(b.lastFlush) >= goalEvalBoxFlushInterval
	var chunk string
	if due {
		chunk = b.pending.String()
		b.pending.Reset()
		b.lastFlush = time.Now()
	}
	b.mu.Unlock()
	if chunk != "" {
		b.appendUpdate(chunk, nil)
	}
}

// Finish 落盘剩余文本并把子框置为 completed/failed；extra 为附加说明（如失败原因）。
func (b *goalEvalBox) Finish(failed bool, extra string) {
	b.mu.Lock()
	chunk := b.pending.String()
	b.pending.Reset()
	b.mu.Unlock()
	if extra != "" {
		if chunk != "" {
			chunk += "\n"
		}
		chunk += extra
	}
	status := acp.ToolCallStatusCompleted
	if failed {
		status = acp.ToolCallStatusFailed
	}
	b.appendUpdate(chunk, &status)
}

// appendUpdate 追加一条 tool_call_update（text 可为空，仅更新状态）。
func (b *goalEvalBox) appendUpdate(text string, status *acp.ToolCallStatus) {
	u := &acp.SessionToolCallUpdate{
		ToolCallId:    acp.ToolCallId(b.toolCallID),
		SessionUpdate: "tool_call_update",
		Status:        status,
	}
	if text != "" {
		u.Content = []acp.ToolCallContent{{Content: &acp.ToolCallContentContent{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: text, Type: "text"}},
			Type:    "content",
		}}}
	}
	b.persist(acp.SessionUpdate{ToolCallUpdate: u})
}

// persist 持久化并广播（若有订阅者），与 goalNotify 同路径。
func (b *goalEvalBox) persist(update acp.SessionUpdate) {
	s := b.svc
	seq := s.getNextSequence(b.session.SessionID) + 1
	msg := MapUpdate(b.session.SessionID, b.session.ID, seq, update)
	// tool_call_update 无标题时 content 为空，补上子框标题使前端合并后仍有摘要
	if msg.Content == "" {
		msg.Content = b.title
	}
	if err := s.messages.Create(&msg); err != nil {
		slog.Error("持久化 goal 评估子框消息失败", "session", b.session.SessionID, "err", err)
	}
	s.mu.RLock()
	bc := s.activePrompts[b.session.SessionID]
	s.mu.RUnlock()
	if bc != nil {
		bc.broadcast(msg)
	}
}
