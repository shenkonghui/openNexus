package acp

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/logging"
)

// subscriber 表示一个会话 update 流的订阅者，每个订阅者拥有独立的 buffered channel。
type subscriber struct {
	ch chan acp.SessionUpdate
}

// ElicitationEvent 表示一次 elicitation（如浏览器登录）完成事件。
// 由 agent 发送 UnstableCompleteElicitation 通知时产生，供前端 SSE 订阅感知登录结束。
type ElicitationEvent struct {
	ElicitationId string
}

// Client 实现 acp.Client 接口，处理权限交互并按 sessionID 路由 session update。
// streams 采用 fan-out 设计：每个会话可有多个订阅者，支持多客户端同时监听（如断点续传重连）。
type Client struct {
	mu      sync.RWMutex
	streams map[acp.SessionId]map[*subscriber]struct{}
	perm    *permissionBroker
	rec     *fileRecorder
	// term 可选：ACP terminal 能力桥接器（Service 级共享）。nil 时 terminal/* 为 no-op。
	term *TerminalBridge
	// elicitationCh 缓冲 agent 发来的 elicitation 完成通知，供前端 SSE 消费。
	// buffer 满时丢弃旧事件（非阻塞），避免拖慢 JSON-RPC 处理。
	elicitationCh chan ElicitationEvent
}

// NewClient 创建一个新的 Client。
func NewClient() *Client {
	return &Client{
		streams:       make(map[acp.SessionId]map[*subscriber]struct{}),
		perm:          newPermissionBroker(),
		rec:           newFileRecorder(),
		elicitationCh: make(chan ElicitationEvent, 16),
	}
}

// ElicitationEvents 返回 elicitation 完成事件的只读 channel（agent → client 通知）。
func (c *Client) ElicitationEvents() <-chan ElicitationEvent {
	return c.elicitationCh
}

// Subscribe 为指定 session 注册一个新订阅者，返回该订阅者。
// 多次调用会创建多个独立订阅者，SessionUpdate 会分发给所有订阅者。
func (c *Client) Subscribe(sessionID acp.SessionId, bufSize int) *subscriber {
	c.mu.Lock()
	defer c.mu.Unlock()
	sub := &subscriber{ch: make(chan acp.SessionUpdate, bufSize)}
	if c.streams[sessionID] == nil {
		c.streams[sessionID] = make(map[*subscriber]struct{})
	}
	c.streams[sessionID][sub] = struct{}{}
	return sub
}

// Unsubscribe 移除并关闭单个订阅者（不影响该会话的其他订阅者）。
func (c *Client) Unsubscribe(sessionID acp.SessionId, sub *subscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if subs, ok := c.streams[sessionID]; ok {
		if _, exists := subs[sub]; exists {
			delete(subs, sub)
			close(sub.ch)
		}
		if len(subs) == 0 {
			delete(c.streams, sessionID)
		}
	}
}

// UnsubscribeAll 移除并关闭指定 session 的全部订阅者（会话关闭时调用）。
func (c *Client) UnsubscribeAll(sessionID acp.SessionId) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if subs, ok := c.streams[sessionID]; ok {
		for sub := range subs {
			close(sub.ch)
		}
		delete(c.streams, sessionID)
	}
}

// RegisterStream 为指定 session 注册一个 update stream，返回只读 channel。
// 兼容接口：内部转为单订阅者，新代码应直接使用 Subscribe。
func (c *Client) RegisterStream(sessionID acp.SessionId, bufSize int) chan acp.SessionUpdate {
	return c.Subscribe(sessionID, bufSize).ch
}

// UnregisterStream 注销并关闭指定 session 的全部 stream。
// 兼容接口：语义为清空该会话所有订阅者。
func (c *Client) UnregisterStream(sessionID acp.SessionId) {
	c.UnsubscribeAll(sessionID)
}

// RegisterPermissionWaiter 注册权限请求监听（Prompt 流期间调用）。
func (c *Client) RegisterPermissionWaiter(sessionID acp.SessionId) chan PermissionNotify {
	return c.perm.registerWaiter(sessionID)
}

// UnregisterPermissionWaiter 注销权限请求监听。
func (c *Client) UnregisterPermissionWaiter(sessionID acp.SessionId) {
	c.perm.unregisterWaiter(sessionID)
}

// RespondPermission 提交用户对权限请求的响应。
func (c *Client) RespondPermission(requestID, optionID string, cancelled bool) error {
	return c.perm.respond(requestID, optionID, cancelled)
}

// RegisterFileWaiter 注册文件改动监听（Prompt 流期间调用），返回事件 channel。
func (c *Client) RegisterFileWaiter(sessionID acp.SessionId) chan FileWriteNotify {
	return c.rec.registerWaiter(sessionID)
}

// UnregisterFileWaiter 注销文件改动监听。
func (c *Client) UnregisterFileWaiter(sessionID acp.SessionId) {
	c.rec.unregisterWaiter(sessionID)
}

// SetYoloCheck 注入会话 YOLO 查询（按 ACP SessionId）。
func (c *Client) SetYoloCheck(fn func(acp.SessionId) bool) {
	c.perm.setYoloCheck(fn)
}

// SetTerminalBridge 注入 terminal 能力桥接器（建连后、Initialize 前调用）。
func (c *Client) SetTerminalBridge(b *TerminalBridge) {
	c.term = b
}

// CancelPermissions 取消 session 所有挂起的权限请求。
func (c *Client) CancelPermissions(sessionID acp.SessionId) {
	c.perm.cancelSession(sessionID)
}

// RequestPermission 等待用户在前端选择权限选项；无活跃 Prompt 流时自动批准。
func (c *Client) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	toolTitle := ""
	if params.ToolCall.Title != nil {
		toolTitle = *params.ToolCall.Title
	}
	slog.Debug("ACP requestPermission", "session", params.SessionId, "tool", toolTitle)
	return c.perm.request(ctx, params)
}

// SessionUpdate 将 update 按 SessionId 分发给该会话的全部订阅者。
// 对 buffer 满的慢订阅者采用非阻塞丢弃（记日志），避免拖慢其他订阅者。
func (c *Client) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	c.mu.RLock()
	subs := c.streams[params.SessionId]
	// 复制订阅者集合，避免持锁发送
	snapshot := make([]*subscriber, 0, len(subs))
	for sub := range subs {
		snapshot = append(snapshot, sub)
	}
	c.mu.RUnlock()

	kind, _ := extractKindRole(params.Update)
	content := extractContent(params.Update)
	slog.Debug("ACP sessionUpdate", "session", params.SessionId, "kind", kind, "content_len", len(content), "preview", logging.Preview(content, 80), "subscribers", len(snapshot))

	for _, sub := range snapshot {
		select {
		case sub.ch <- params.Update:
		default:
			// 慢订阅者 buffer 满，丢弃本条避免阻塞其他订阅者
			slog.Warn("ACP sessionUpdate 订阅者 buffer 满，丢弃消息", "session", params.SessionId, "kind", kind)
		}
	}
	return nil
}

// WriteTextFile 将文件写入工作区，并记录文件改动（旧内容 → 新内容）供前端展示 diff。
func (c *Client) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.WriteTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}

	// 写入前读取旧内容（不存在视为新建文件）。
	oldBytes, readErr := os.ReadFile(params.Path)
	isNew := readErr != nil // 不存在或读取失败均视为新文件
	oldText := ""
	if !isNew {
		oldText = string(oldBytes)
	}

	dir := filepath.Dir(params.Path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return acp.WriteTextFileResponse{}, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(params.Path, []byte(params.Content), 0o644); err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("write %s: %w", params.Path, err)
	}

	// 仅当内容实际变化时记录，避免无意义的空写。
	if isNew || oldText != params.Content {
		c.rec.record(FileWriteNotify{
			SessionId: params.SessionId,
			Path:      params.Path,
			OldText:   oldText,
			NewText:   params.Content,
			IsNew:     isNew,
		})
	}

	return acp.WriteTextFileResponse{}, nil
}

// ReadTextFile 从工作区读取文件。
func (c *Client) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.ReadTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}
	b, err := os.ReadFile(params.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("read %s: %w", params.Path, err)
	}
	return acp.ReadTextFileResponse{Content: string(b)}, nil
}

// CreateTerminal 代 agent 启动命令并开始采集输出（bridge 未注入时 no-op）。
func (c *Client) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	if c.term == nil {
		return acp.CreateTerminalResponse{}, nil
	}
	return c.term.Create(ctx, params)
}

// TerminalOutput 返回终端当前输出与退出状态（bridge 未注入时 no-op）。
func (c *Client) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	if c.term == nil {
		return acp.TerminalOutputResponse{}, nil
	}
	return c.term.Output(params)
}

// ReleaseTerminal 终止并释放终端（bridge 未注入时 no-op）。
func (c *Client) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	if c.term == nil {
		return acp.ReleaseTerminalResponse{}, nil
	}
	return c.term.Release(params)
}

// WaitForTerminalExit 阻塞等待终端命令退出（bridge 未注入时 no-op）。
func (c *Client) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	if c.term == nil {
		return acp.WaitForTerminalExitResponse{}, nil
	}
	return c.term.WaitForExit(ctx, params)
}

// KillTerminal 终止终端进程但保留输出（bridge 未注入时 no-op）。
func (c *Client) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	if c.term == nil {
		return acp.KillTerminalResponse{}, nil
	}
	return c.term.Kill(params)
}

// UnstableCreateElicitation 处理 agent 发起的 elicitation 请求。
// URL 类型：校验 scheme 后用系统默认浏览器打开 URL 让用户完成登录，返回 accept 表示用户已开始流程。
// Form 类型：暂不支持，返回 cancel。
func (c *Client) UnstableCreateElicitation(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	if params.Url != nil {
		slog.Info("ACP elicitation: 打开浏览器认证", "url", params.Url.Url, "message", params.Url.Message)
		// 校验 URL scheme：仅允许 http/https，防止恶意 agent 利用 file:/javascript: 等协议
		if !isSafeBrowserURL(params.Url.Url) {
			slog.Warn("ACP elicitation: URL scheme 不允许，已拒绝打开", "url", params.Url.Url)
			return acp.UnstableCreateElicitationResponse{
				Cancel: &acp.UnstableCreateElicitationCancel{
					Action: "cancel",
				},
			}, nil
		}
		if err := openBrowser(params.Url.Url); err != nil {
			slog.Warn("ACP elicitation: 打开浏览器失败", "url", params.Url.Url, "err", err)
		}
		// 返回 accept：用户已开始浏览器登录流程，agent 会等待回调完成后发 elicitation/complete
		return acp.UnstableCreateElicitationResponse{
			Accept: &acp.UnstableCreateElicitationAccept{
				Action: "accept",
			},
		}, nil
	}
	// Form 类型暂不支持
	return acp.UnstableCreateElicitationResponse{
		Cancel: &acp.UnstableCreateElicitationCancel{
			Action: "cancel",
		},
	}, nil
}

// UnstableCompleteElicitation 处理 agent 发送的 elicitation 完成通知。
// 通常表示浏览器登录流程已完成（用户已认证或取消）。
// 将事件推入 elicitationCh 供前端 SSE 消费，buffer 满时丢弃（非阻塞）。
func (c *Client) UnstableCompleteElicitation(ctx context.Context, params acp.UnstableCompleteElicitationNotification) error {
	slog.Info("ACP elicitation 完成", "elicitation_id", params.ElicitationId)
	select {
	case c.elicitationCh <- ElicitationEvent{ElicitationId: string(params.ElicitationId)}:
	default:
		slog.Warn("ACP elicitation 事件 channel 满，丢弃完成通知", "elicitation_id", params.ElicitationId)
	}
	return nil
}

// isSafeBrowserURL 校验 URL 仅使用 http/https scheme，防止 file:/javascript: 等危险协议。
func isSafeBrowserURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// openBrowser 用系统默认浏览器打开 URL。
func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "linux":
		return exec.Command("xdg-open", rawURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
