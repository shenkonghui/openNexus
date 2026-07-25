package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
)

var ErrPermissionNotFound = errors.New("权限请求不存在或已过期")

// PermissionNotify 推送给 Prompt 流的权限请求事件。
type PermissionNotify struct {
	RequestID string
	Request   acp.RequestPermissionRequest
}

type pendingPermission struct {
	sessionID acp.SessionId
	respCh    chan acp.RequestPermissionResponse
}

// permissionBroker 管理 ACP 权限请求与用户响应的桥接。
type permissionBroker struct {
	mu      sync.Mutex
	seq     atomic.Uint64
	waiters map[acp.SessionId]chan PermissionNotify
	pending map[string]pendingPermission
	// rules 全局权限规则（白名单/黑名单/询问名单）。nil=无规则，未开 yolo 时全部询问。
	rules atomic.Pointer[PermissionRules]
	// yoloCheck 查询会话是否开启 YOLO（由 Service 注入，按 ACP SessionId 查）。
	yoloMu    sync.RWMutex
	yoloCheck func(acp.SessionId) bool
}

func newPermissionBroker() *permissionBroker {
	return &permissionBroker{
		waiters: make(map[acp.SessionId]chan PermissionNotify),
		pending: make(map[string]pendingPermission),
	}
}

// setRules 设置全局权限规则（热更新）。传 nil 清除规则（回到全部询问）。
func (b *permissionBroker) setRules(rules *PermissionRules) {
	if rules == nil {
		b.rules.Store(nil)
		return
	}
	cp := *rules
	b.rules.Store(&cp)
}

// setYoloCheck 注入会话 YOLO 查询函数（按 ACP SessionId）。
func (b *permissionBroker) setYoloCheck(fn func(acp.SessionId) bool) {
	b.yoloMu.Lock()
	b.yoloCheck = fn
	b.yoloMu.Unlock()
}

func (b *permissionBroker) isYolo(sessionID acp.SessionId) bool {
	b.yoloMu.RLock()
	fn := b.yoloCheck
	b.yoloMu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(sessionID)
}

func (b *permissionBroker) registerWaiter(sessionID acp.SessionId) chan PermissionNotify {
	ch := make(chan PermissionNotify, 64)
	b.mu.Lock()
	b.waiters[sessionID] = ch
	b.mu.Unlock()
	return ch
}

func (b *permissionBroker) unregisterWaiter(sessionID acp.SessionId) {
	b.mu.Lock()
	ch, ok := b.waiters[sessionID]
	if ok {
		delete(b.waiters, sessionID)
	}
	b.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (b *permissionBroker) cancelSession(sessionID acp.SessionId) {
	cancelled := acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
	}
	b.mu.Lock()
	for id, item := range b.pending {
		if item.sessionID != sessionID {
			continue
		}
		select {
		case item.respCh <- cancelled:
		default:
		}
		delete(b.pending, id)
	}
	b.mu.Unlock()
}

func (b *permissionBroker) request(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	// 内置 opennexus-notes MCP 由本服务注入，自动放行，避免并行 list_notes 把 UI 卡死。
	if isTrustedMCPTool(params) {
		return autoApprovePermission(params), nil
	}

	// 全局名单 + 全局/会话 YOLO：在入队 UI 之前裁决，
	// 命中 allow / yolo→自动放行，命中 deny→自动拒绝，命中 ask 或未命中→继续走 UI 询问。
	yolo := b.isYolo(params.SessionId)
	if rules := b.rules.Load(); rules != nil {
		switch rules.Decide(toolCallTitle(params), yolo) {
		case DecisionAllow:
			return autoApprovePermission(params), nil
		case DecisionDeny:
			return autoRejectPermission(params), nil
		}
	} else if yolo {
		return autoApprovePermission(params), nil
	}

	requestID := fmt.Sprintf("perm-%d", b.seq.Add(1))
	respCh := make(chan acp.RequestPermissionResponse, 1)

	b.mu.Lock()
	b.pending[requestID] = pendingPermission{sessionID: params.SessionId, respCh: respCh}
	waiter := b.waiters[params.SessionId]
	b.mu.Unlock()

	if waiter != nil {
		select {
		case waiter <- PermissionNotify{RequestID: requestID, Request: params}:
		case <-ctx.Done():
			b.removePending(requestID)
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
			}, nil
		}
	} else {
		b.removePending(requestID)
		return autoApprovePermission(params), nil
	}

	select {
	case resp := <-respCh:
		return resp, nil
	case <-ctx.Done():
		b.removePending(requestID)
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
		}, nil
	}
}

func (b *permissionBroker) respond(requestID, optionID string, cancelled bool) error {
	b.mu.Lock()
	item, ok := b.pending[requestID]
	if !ok {
		b.mu.Unlock()
		return ErrPermissionNotFound
	}
	delete(b.pending, requestID)
	// allow-always：同会话其余挂起权限一并放行，避免并行工具调用逐条卡死。
	var batch []pendingPermission
	if !cancelled && optionID == "allow-always" {
		for id, p := range b.pending {
			if p.sessionID != item.sessionID {
				continue
			}
			batch = append(batch, p)
			delete(b.pending, id)
		}
	}
	b.mu.Unlock()

	send := func(p pendingPermission) {
		if cancelled {
			p.respCh <- acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
			}
			return
		}
		p.respCh <- acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: acp.PermissionOptionId(optionID)},
			},
		}
	}
	send(item)
	for _, p := range batch {
		send(p)
	}
	return nil
}

func isTrustedMCPTool(params acp.RequestPermissionRequest) bool {
	return strings.HasPrefix(toolCallTitle(params), "opennexus-notes")
}

// toolCallTitle 返回权限请求的工具调用标题（用于权限规则匹配）。
// 优先用 ToolCall.Title；CodeBuddy 等常不填 title，改从 _meta.toolName + rawInput.command
// 拼成 "Bash(cmd)" 以便 yolo/白名单匹配。皆无则返回空串。
func toolCallTitle(params acp.RequestPermissionRequest) string {
	if params.ToolCall.Title != nil {
		if t := strings.TrimSpace(*params.ToolCall.Title); t != "" {
			return t
		}
	}
	name := metaString(params.ToolCall.Meta, "codebuddy.ai/toolName", "toolName", "tool_name")
	cmd := rawInputString(params.ToolCall.RawInput, "command", "cmd")
	switch {
	case name != "" && cmd != "":
		return name + "(" + cmd + ")"
	case name != "":
		return name
	case cmd != "":
		return cmd
	}
	if params.ToolCall.Kind != nil {
		return string(*params.ToolCall.Kind)
	}
	return ""
}

// metaString 从 ACP _meta 中按候选键取非空字符串。
func metaString(meta map[string]any, keys ...string) string {
	if meta == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := meta[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// rawInputString 从 toolCall.rawInput（通常为 map）中按候选键取非空字符串。
func rawInputString(raw any, keys ...string) string {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func (b *permissionBroker) removePending(requestID string) {
	b.mu.Lock()
	delete(b.pending, requestID)
	b.mu.Unlock()
}

func autoApprovePermission(params acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	// 必须优先 allow-once：若选 allow-always，agent（如 CodeBuddy）会把本会话记成永久放行，
	// 关掉会话 yolo 后现有会话仍会自动跑命令。
	pick := func(kind acp.PermissionOptionKind) *acp.PermissionOptionId {
		for i := range params.Options {
			if params.Options[i].Kind == kind {
				id := params.Options[i].OptionId
				return &id
			}
		}
		return nil
	}
	if id := pick(acp.PermissionOptionKindAllowOnce); id != nil {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: *id},
			},
		}
	}
	if id := pick(acp.PermissionOptionKindAllowAlways); id != nil {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: *id},
			},
		}
	}
	if len(params.Options) > 0 {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: params.Options[0].OptionId},
			},
		}
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
	}
}

// autoRejectPermission 生成自动拒绝响应：优先选 reject 选项，没有则标记 Cancelled。
// 用于黑名单命中或显式拒绝。
func autoRejectPermission(params acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	for _, o := range params.Options {
		if o.Kind == acp.PermissionOptionKindRejectOnce || o.Kind == acp.PermissionOptionKindRejectAlways {
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{OptionId: o.OptionId},
				},
			}
		}
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
	}
}

// MapPermissionRequest 将权限请求映射为 Message。
func MapPermissionRequest(sessionID string, dbSessionID uint, seq int, notify PermissionNotify) models.Message {
	payload := map[string]any{
		"request_id": notify.RequestID,
		"tool_call":  notify.Request.ToolCall,
		"options":    notify.Request.Options,
	}
	raw, _ := json.Marshal(payload)
	title := toolCallTitle(notify.Request)
	return models.Message{
		SessionID:   sessionID,
		DBSessionID: dbSessionID,
		Role:        models.MessageRoleAssistant,
		Kind:        models.MessageKindPermissionRequest,
		Content:     title,
		RawJSON:     string(raw),
		Sequence:    seq,
	}
}
