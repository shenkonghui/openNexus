package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"
)

// SubAgentRunConfig 描述一次临时会话任务的参数。
type SubAgentRunConfig struct {
	AgentType    string        // 已注册的 agent type，必填
	ModelValue   string        // 模型值，空=用 agent 默认
	Prompt       string        // 用户任务文本
	SystemPrompt string        // 角色定义，注入 _meta.systemPrompt；空=不注入
	UserID       uint          // 用于拉全局 mcpServers（继承父会话级别），0=不注入
	Timeout      time.Duration // 单次调用超时，0=默认 promptOnceTimeout (60s)
}

// RunSubAgent 在临时 ACP 会话中执行一次任务，收集 assistant 文本后关闭会话，不落库。
// 供内部功能（如笔记标签分类）使用。
//
// 与 RunPromptOnce 的差异：
//   - 注入全局 mcpServers（按 UserID），让任务继承父会话级别的工具
//   - 注入 SystemPrompt 到 NewSessionRequest._meta.systemPrompt（角色定义）
//   - 可配置超时
//
// 权限处理与 RunPromptOnce 一致：自动拒绝所有权限请求（适合无状态的文本类任务）。
func (s *Service) RunSubAgent(ctx context.Context, cfg SubAgentRunConfig) (string, error) {
	if strings.TrimSpace(cfg.Prompt) == "" {
		return "", fmt.Errorf("prompt 不能为空")
	}
	if _, err := s.GetBackend(cfg.AgentType); err != nil {
		return "", err
	}

	conn, err := s.ensureConnection(ctx, cfg.AgentType, s.probeCwd())
	if err != nil {
		return "", err
	}
	cwd := s.probeCwd()

	// 注入全局 mcpServers（继承父会话级别）+ systemPrompt
	var mcpServers []acp.McpServer
	if cfg.UserID > 0 {
		mcpServers = s.sessionMCPServers(cfg.UserID, conn.McpCapabilities())
	}
	sessionID, configOptions, _, err := conn.NewSession(ctx, cwd, s.skillAdditionalDirs(cwd), mcpServers, cfg.SystemPrompt)
	if err != nil {
		return "", fmt.Errorf("创建临时会话: %w", err)
	}
	defer func() { _ = conn.CloseSessionByID(ctx, sessionID) }()

	if cfg.ModelValue != "" {
		if err := applyModelOption(ctx, conn, sessionID, configOptions, cfg.ModelValue); err != nil {
			return "", fmt.Errorf("设置模型: %w", err)
		}
	}

	sid := acp.SessionId(sessionID)
	permCh := conn.Client().RegisterPermissionWaiter(sid)
	defer conn.Client().UnregisterPermissionWaiter(sid)
	go autoCancelPermissions(conn, permCh)

	updates, err := conn.Prompt(ctx, sessionID, cfg.Prompt)
	if err != nil {
		return "", err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = promptOnceTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var sb strings.Builder
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return strings.TrimSpace(sb.String()), nil
			}
			if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
				sb.WriteString(u.AgentMessageChunk.Content.Text.Text)
			}
		case <-runCtx.Done():
			if sb.Len() > 0 {
				return strings.TrimSpace(sb.String()), nil
			}
			return "", fmt.Errorf("agent 响应超时")
		}
	}
}
