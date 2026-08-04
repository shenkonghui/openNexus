package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/logging"
)

// Connection 封装 acp.ClientSideConnection，管理一个 agent 传输通道与一条 ACP 连接。
//
// 一条 Connection 可承载多个 ACP session（多路复用同一 agent 进程），
// 各 session 的 update 通过 Client 按 SessionId 路由分发。
//
// 底层 transport 两种形态：
//   - 直连（*Process）：agent 是主 server 子进程，随主 server 退出；
//   - 常驻（*bridgeTransport）：agent 挂在独立 acp-bridge 守护进程下，UDS 通信，
//     主 server 重启后重新拨号即可复用原 agent。
type Connection struct {
	conn      *acp.ClientSideConnection
	transport agentTransport
	client    *Client

	// reused 标记底层 transport 是否复用了已存活的 bridge+agent（主 server 重启场景）。
	reused bool

	// terminalEnabled 握手时是否向 agent 声明 terminal 能力（agent shell 由本服务代执行）。
	terminalEnabled bool

	// mcpCaps 是 initialize 握手中 agent 声明的 MCP 传输能力（http/sse 等）。
	// 在 Initialize 成功后写入（连接入池前），之后只读。
	mcpCaps acp.McpCapabilities

	// initResp 是 initialize 握手的完整响应（agent 能力、认证方式、协议版本等），
	// 在 Initialize 成功后写入（连接入池前），之后只读，供设置页展示 ACP 能力。
	initResp acp.InitializeResponse
}

// Sandboxed 返回底层 agent 进程是否真正运行在 OS 沙箱内（非降级直通）。
// 复用已存在 bridge 时无法确定原 bridge 的沙箱状态，返回 false。
// 沙箱效果测试据此判断是否可安全自动批准工具调用。
func (c *Connection) Sandboxed() bool {
	if c == nil || c.transport == nil {
		return false
	}
	return c.transport.Sandboxed()
}

// NewConnection 启动 agent 进程（直连模式）并建立 ACP 连接。
// dbg 非空且 Enabled 时，用 tee 包装 stdin/stdout 捕获 JSON-RPC 报文。
// terminalEnabled 为 true 时握手声明 terminal 能力（agent 的 shell 改由本服务代执行）。
func NewConnection(backend Backend, workDir string, dbg *ACPDebugger, terminalEnabled bool) (*Connection, error) {
	proc, err := NewProcess(backend, workDir)
	if err != nil {
		return nil, err
	}
	return newConnectionWithTransport(proc, backend, dbg, terminalEnabled, false), nil
}

// NewBridgeConnection 通过常驻 acp-bridge 建立 ACP 连接（agent 进程与主 server 解耦）。
// 优先拨号复用已存活的 bridge（主 server 重启后 agent 上下文保留）；
// 拨不通或 forceNew 时拉起全新 bridge+agent。
func NewBridgeConnection(backend Backend, workDir string, dbg *ACPDebugger, terminalEnabled bool, socketPath string, forceNew bool) (*Connection, error) {
	bt, err := NewBridgeTransport(backend, workDir, socketPath, forceNew)
	if err != nil {
		return nil, err
	}
	return newConnectionWithTransport(bt, backend, dbg, terminalEnabled, bt.Reused()), nil
}

// newConnectionWithTransport 在任意 transport 上组装 ACP 客户端连接。
func newConnectionWithTransport(t agentTransport, backend Backend, dbg *ACPDebugger, terminalEnabled bool, reused bool) *Connection {
	client := NewClient()
	stdin := io.WriteCloser(t.Stdin())
	stdout := io.Reader(t.Stdout())
	if dbg != nil && dbg.Enabled() {
		agentType := backend.Name()
		stdin = &teeWriter{w: t.Stdin(), dbg: dbg, direction: "send", agentType: agentType}
		stdout = &teeReader{r: t.Stdout(), dbg: dbg, direction: "recv", agentType: agentType}
	}
	conn := acp.NewClientSideConnection(client, stdin, stdout)
	conn.SetLogger(slog.Default())

	return &Connection{
		conn:            conn,
		transport:       t,
		client:          client,
		reused:          reused,
		terminalEnabled: terminalEnabled,
	}
}

// Reused 返回是否复用了已存活的常驻 agent（主 server 重启后重连场景）。
func (c *Connection) Reused() bool {
	return c.reused
}

// clientCapabilities 构造握手时声明的 client 能力。
// terminalEnabled 为 true 时声明 terminal 能力，agent 的 shell 执行会改走 terminal/* 方法，
// 由 TerminalBridge 代执行并推送到前端终端面板。
func clientCapabilities(terminalEnabled bool) acp.ClientCapabilities {
	return acp.ClientCapabilities{
		Fs: acp.FileSystemCapabilities{
			ReadTextFile:  true,
			WriteTextFile: true,
		},
		Terminal: terminalEnabled,
	}
}

// Initialize 执行 ACP 握手。
func (c *Connection) Initialize(ctx context.Context) (acp.InitializeResponse, error) {
	slog.Debug("ACP initialize", "terminal", c.terminalEnabled)
	resp, err := c.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: clientCapabilities(c.terminalEnabled),
	})
	if err != nil {
		return acp.InitializeResponse{}, fmt.Errorf("ACP initialize: %w", err)
	}
	c.mcpCaps = resp.AgentCapabilities.McpCapabilities
	c.initResp = resp
	slog.Debug("ACP initialize 完成", "protocol", resp.ProtocolVersion, "auth_methods", len(resp.AuthMethods),
		"mcp_http", c.mcpCaps.Http, "mcp_sse", c.mcpCaps.Sse)
	return resp, nil
}

// InitializeInfo 返回握手的完整响应（Initialize 成功前为零值）。
func (c *Connection) InitializeInfo() acp.InitializeResponse {
	return c.initResp
}

// McpCapabilities 返回 agent 握手时声明的 MCP 传输能力。
// stdio 是 ACP 基线能力（协议保证支持），此处只描述 http/sse 等可选传输。
func (c *Connection) McpCapabilities() acp.McpCapabilities {
	return c.mcpCaps
}

// authMethodID 从 Initialize 返回的认证方式中提取 methodId。
func authMethodID(m acp.AuthMethod) string {
	switch {
	case m.Agent != nil:
		return m.Agent.Id
	case m.EnvVar != nil:
		return m.EnvVar.Id
	case m.Terminal != nil:
		return m.Terminal.Id
	default:
		return ""
	}
}

// autoAuthMethodIDs 返回可后台自动认证的 methodId 列表。
// 仅 env_var 类型（API Key 已由 Backend.Env() 注入）适合自动调用 authenticate；
// agent / terminal 类型需要交互式登录，不在预连接阶段自动调用。
func autoAuthMethodIDs(methods []acp.AuthMethod) []string {
	var ids []string
	for _, m := range methods {
		if m.EnvVar != nil && m.EnvVar.Id != "" {
			ids = append(ids, m.EnvVar.Id)
		}
	}
	return ids
}

// AuthenticateIfRequired 尝试对 env_var 类型认证方式调用 authenticate。
// 认证失败不阻断连接——部分 Agent 通过环境变量鉴权，无需或未实现 authenticate。
func (c *Connection) AuthenticateIfRequired(ctx context.Context, initResp acp.InitializeResponse) error {
	ids := autoAuthMethodIDs(initResp.AuthMethods)
	if len(ids) == 0 {
		if len(initResp.AuthMethods) > 0 {
			slog.Debug("跳过自动 ACP 认证（无 env_var 方式，agent/terminal 需交互式登录）",
				"auth_methods", len(initResp.AuthMethods))
		}
		return nil
	}
	for _, methodID := range ids {
		slog.Info("ACP authenticate", "method", methodID, "type", "env_var")
		if _, err := c.conn.Authenticate(ctx, acp.AuthenticateRequest{MethodId: methodID}); err != nil {
			slog.Warn("ACP authenticate 失败（非致命，继续连接）", "method", methodID, "err", err)
			continue
		}
		slog.Info("ACP authenticate 完成", "method", methodID)
		return nil
	}
	return nil
}

// NewSession 在当前连接上创建新的 ACP session，返回 session ID、初始 config options 和初始 modes。
// additionalDirectories 为 ACP 额外可访问根目录（如 skills 目录），路径须为绝对路径。
// mcpServers 为可选 MCP 配置；nil 时传空数组。
// systemPrompt 非空时写入 _meta.systemPrompt，由支持该扩展的 Agent 注入为系统提示词。
// 同一 Connection 可多次调用，每次返回不同的 session ID。
func (c *Connection) NewSession(ctx context.Context, cwd string, additionalDirectories []string, mcpServers []acp.McpServer, systemPrompt string) (string, []acp.SessionConfigOption, []acp.SessionMode, error) {
	slog.Info("ACP newSession", "cwd", cwd, "extra_dirs", len(additionalDirectories), "mcp_servers", len(mcpServers), "mcp_server_names", mcpServerNames(mcpServers), "system_prompt_chars", len(systemPrompt))
	if mcpServers == nil {
		mcpServers = []acp.McpServer{}
	}
	req := acp.NewSessionRequest{
		Cwd:                   cwd,
		AdditionalDirectories: additionalDirectories,
		McpServers:            mcpServers,
	}
	if systemPrompt != "" {
		req.Meta = map[string]any{"systemPrompt": systemPrompt}
	}
	resp, err := c.conn.NewSession(ctx, req)
	if err != nil {
		return "", nil, nil, fmt.Errorf("ACP newSession: %w", err)
	}

	var modes []acp.SessionMode
	if resp.Modes != nil {
		modes = resp.Modes.AvailableModes
	}
	slog.Debug("ACP newSession 完成", "session", resp.SessionId, "config_options", len(resp.ConfigOptions), "modes", len(modes))
	return string(resp.SessionId), resp.ConfigOptions, modes, nil
}

// Prompt 发送 prompt 并返回该 session 专属的流式 update channel。
// 使用 Subscribe 创建独立订阅者（fan-out），prompt turn 完成后仅注销该订阅者，
// 不影响其他订阅者（如断点续传重连的监听者）。
func (c *Connection) Prompt(ctx context.Context, sessionID, prompt string) (<-chan acp.SessionUpdate, error) {
	sid := acp.SessionId(sessionID)
	sub := c.client.Subscribe(sid, 4096)
	slog.Debug("ACP prompt", "session", sessionID, "chars", len(prompt), "preview", logging.Preview(prompt, 120))

	go func() {
		defer c.client.Unsubscribe(sid, sub)
		_, err := c.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sid,
			Prompt:    []acp.ContentBlock{acp.TextBlock(prompt)},
		})
		if err != nil {
			slog.Debug("ACP prompt 失败", "session", sessionID, "err", err)
		} else {
			slog.Debug("ACP prompt 完成", "session", sessionID)
		}
	}()

	return sub.ch, nil
}

// Cancel 取消正在进行的 prompt。
func (c *Connection) Cancel(ctx context.Context, sessionID string) error {
	slog.Debug("ACP cancel", "session", sessionID)
	return c.conn.Cancel(ctx, acp.CancelNotification{SessionId: acp.SessionId(sessionID)})
}

// SetConfigOption 设置会话的 config option（如模型选择）。
func (c *Connection) SetConfigOption(ctx context.Context, sessionID, configID, value string) error {
	slog.Debug("ACP setSessionConfigOption", "session", sessionID, "config", configID, "value", value)
	_, err := c.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: acp.SessionId(sessionID),
			ConfigId:  acp.SessionConfigId(configID),
			Value:     acp.SessionConfigValueId(value),
		},
	})
	return err
}

// SetSessionMode 切换会话模式（如 ask / agent / edit）。
func (c *Connection) SetSessionMode(ctx context.Context, sessionID, modeID string) error {
	slog.Debug("ACP setSessionMode", "session", sessionID, "mode", modeID)
	_, err := c.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionId: acp.SessionId(sessionID),
		ModeId:    acp.SessionModeId(modeID),
	})
	return err
}

// CloseSessionByID 关闭单个 ACP session（释放 agent 侧该 session 的资源），
// 但不停止 agent 进程，连接可继续承载其他 session。
func (c *Connection) CloseSessionByID(ctx context.Context, sessionID string) error {
	slog.Debug("ACP closeSession", "session", sessionID)
	c.client.UnregisterStream(acp.SessionId(sessionID))
	_, err := c.conn.CloseSession(ctx, acp.CloseSessionRequest{
		SessionId: acp.SessionId(sessionID),
	})
	return err
}

// Done 返回连接关闭信号 channel。
// agent 进程退出或连接断开时该 channel 关闭。
func (c *Connection) Done() <-chan struct{} {
	return c.conn.Done()
}

// Close 关闭连接并停止 agent 进程（bridge 模式连 bridge 一并终止）。
// 用于彻底销毁该 Connection（不再承载任何 session）。
func (c *Connection) Close() error {
	return c.transport.Stop()
}

// Detach 仅断开通信通道：bridge 模式下 agent 保持运行，主 server 重启后可复用；
// 直连模式无此语义，等价 Close。
func (c *Connection) Detach() error {
	return c.transport.Detach()
}

// InspectFailure 在 agent 启动/握手失败后诊断底层状态，返回人类可读的线索。
// 必须在 Close() 之前调用。
func (c *Connection) InspectFailure() string {
	return c.transport.InspectFailure()
}

// Client 返回内部 Client（用于测试）。
func (c *Connection) Client() *Client {
	return c.client
}

// Pid 返回可供 KillProcessGroup 使用的进程组 PGID
// （直连模式为 agent 直系子进程 PID，bridge 模式为 bridge 守护进程 PID）。未启动返回 0。
func (c *Connection) Pid() int {
	return c.transport.Pid()
}
