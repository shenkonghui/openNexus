package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// ConversationHandler 提供跨会话的对话记录查询（「对话记录」页面）。
// 只保留用户发送的消息与 agent 每轮的最终回复，中间的思考与工具调用不纳入。
type ConversationHandler struct {
	sessions *repository.SessionRepository
	messages *repository.MessageRepository
}

func NewConversationHandler(sessions *repository.SessionRepository, messages *repository.MessageRepository) *ConversationHandler {
	return &ConversationHandler{sessions: sessions, messages: messages}
}

// ConversationRecord 是一条对话记录：用户消息或 agent 最终回复（连续 chunk 已合并）。
type ConversationRecord struct {
	DBSessionID  uint      `json:"db_session_id"`
	Role         string    `json:"role"`
	Content      string    `json:"content"`
	Sequence     int       `json:"sequence"`
	CreatedAt    time.Time `json:"created_at"`
	SessionTitle string    `json:"session_title"`
	AgentType    string    `json:"agent_type"`
}

// List 分页查询当前用户的对话记录（按时间倒序），可按任务（session_id）过滤。
// GET /api/v1/conversation-records?session_id=1&limit=50&offset=0
func (h *ConversationHandler) List(c *gin.Context) {
	userID := c.GetUint("user_id")

	var sessions []models.Session
	if v, err := strconv.ParseUint(c.Query("session_id"), 10, 64); err == nil && v > 0 {
		s, err := h.sessions.FindByID(uint(v))
		if err != nil || s.UserID != userID {
			Fail(c, http.StatusNotFound, "SESSION_NOT_FOUND", "会话不存在")
			return
		}
		sessions = []models.Session{*s}
	} else {
		all, err := h.sessions.FindByUserID(userID)
		if err != nil {
			Fail(c, http.StatusInternalServerError, "LIST_CONVERSATIONS_FAILED", err.Error())
			return
		}
		sessions = all
	}

	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		if v > 200 {
			v = 200
		}
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(c.Query("offset")); err == nil && v > 0 {
		offset = v
	}

	records := make([]ConversationRecord, 0)
	for i := range sessions {
		s := &sessions[i]
		msgs, err := h.messages.FindBySessionID(s.SessionID)
		if err != nil {
			continue // 单会话消息文件异常不影响整体列表
		}
		for _, r := range extractConversationRecords(msgs) {
			r.DBSessionID = s.ID
			r.SessionTitle = s.Title
			r.AgentType = s.AgentType
			records = append(records, r)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		if a.DBSessionID != b.DBSessionID {
			return a.DBSessionID > b.DBSessionID
		}
		return a.Sequence > b.Sequence
	})
	total := len(records)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	Success(c, http.StatusOK, gin.H{"items": records[offset:end], "total": total})
}

// extractConversationRecords 从会话消息流（sequence 升序）提炼对话记录：
//   - 连续的 user_message_chunk 合并为一条用户消息；
//   - 每轮（两条用户消息之间）只保留最后一段连续的 agent_message_chunk 作为最终回复，
//     中间穿插的思考（agent_thought_chunk）与工具调用不产生记录。
func extractConversationRecords(msgs []models.Message) []ConversationRecord {
	out := make([]ConversationRecord, 0)

	var userBuf strings.Builder
	var userAt time.Time
	userSeq := 0
	userOpen := false

	var agentBuf strings.Builder
	var agentAt time.Time
	agentSeq := 0
	agentOpen := false
	// lastReply 是当前轮内最后一段完整 agent 回复的候选，遇到下一条用户消息或流结束时落定。
	var lastReply *ConversationRecord

	flushUser := func() {
		if !userOpen {
			return
		}
		if content := strings.TrimSpace(userBuf.String()); content != "" {
			out = append(out, ConversationRecord{Role: models.MessageRoleUser, Content: content, Sequence: userSeq, CreatedAt: userAt})
		}
		userBuf.Reset()
		userOpen = false
	}
	closeAgentRun := func() {
		if !agentOpen {
			return
		}
		if content := strings.TrimSpace(agentBuf.String()); content != "" {
			lastReply = &ConversationRecord{Role: models.MessageRoleAssistant, Content: content, Sequence: agentSeq, CreatedAt: agentAt}
		}
		agentBuf.Reset()
		agentOpen = false
	}
	emitReply := func() {
		if lastReply != nil {
			out = append(out, *lastReply)
			lastReply = nil
		}
	}

	for i := range msgs {
		m := &msgs[i]
		switch m.Kind {
		case models.MessageKindUserMessageChunk:
			// 新一轮开始：先落上一轮的最终回复
			closeAgentRun()
			emitReply()
			if !userOpen {
				userAt, userSeq, userOpen = m.CreatedAt, m.Sequence, true
			}
			userBuf.WriteString(m.Content)
		case models.MessageKindAgentMessageChunk:
			flushUser()
			if !agentOpen {
				agentAt, agentSeq, agentOpen = m.CreatedAt, m.Sequence, true
			}
			agentBuf.WriteString(m.Content)
		default:
			// 思考/工具调用等打断连续块；已累积的 agent 块暂存为候选（可能被本轮更晚的回复替换）
			flushUser()
			closeAgentRun()
		}
	}
	flushUser()
	closeAgentRun()
	emitReply()
	return out
}
