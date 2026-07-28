package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"
)

// promptOnceTimeout 是空闲超时：连续这么久收不到任何 session update 才判定 agent 无响应；
// agent 思考/调工具阶段会持续产生非文本 update，不会误杀慢模型。总时长由调用方 ctx 控制。
const promptOnceTimeout = 60 * time.Second

// RunPromptOnce 在临时 ACP 会话中发送 prompt 并收集 assistant 文本，不落库。
func (s *Service) RunPromptOnce(ctx context.Context, agentType, modelValue, prompt string) (string, error) {
	return s.RunPromptOnceStream(ctx, agentType, modelValue, prompt, nil)
}

// RunPromptOnceStream 同 RunPromptOnce，额外在每收到一段 assistant 文本时回调 onText（增量），
// 供调用方实时展示临时会话的输出（如 goal 评估子框）。onText 为 nil 时行为不变。
func (s *Service) RunPromptOnceStream(ctx context.Context, agentType, modelValue, prompt string, onText func(delta string)) (string, error) {
	if _, err := s.GetBackend(agentType); err != nil {
		return "", err
	}
	conn, err := s.ensureConnection(ctx, agentType, s.probeCwd())
	if err != nil {
		return "", err
	}
	cwd := s.probeCwd()
	sessionID, configOptions, _, err := conn.NewSession(ctx, cwd, s.skillAdditionalDirs(cwd), nil, "")
	if err != nil {
		return "", fmt.Errorf("创建临时会话: %w", err)
	}
	defer func() { _ = conn.CloseSessionByID(ctx, sessionID) }()

	if modelValue != "" {
		if err := applyModelOption(ctx, conn, sessionID, configOptions, modelValue); err != nil {
			return "", fmt.Errorf("设置模型: %w", err)
		}
	}

	sid := acp.SessionId(sessionID)
	permCh := conn.Client().RegisterPermissionWaiter(sid)
	defer conn.Client().UnregisterPermissionWaiter(sid)
	go autoCancelPermissions(conn, permCh)

	updates, err := conn.Prompt(ctx, sessionID, prompt)
	if err != nil {
		return "", err
	}

	idle := time.NewTimer(promptOnceTimeout)
	defer idle.Stop()

	var sb strings.Builder
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return strings.TrimSpace(sb.String()), nil
			}
			// 任意 update 都说明 agent 存活，重置空闲计时
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(promptOnceTimeout)
			if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
				sb.WriteString(u.AgentMessageChunk.Content.Text.Text)
				if onText != nil {
					onText(u.AgentMessageChunk.Content.Text.Text)
				}
			}
		case <-idle.C:
			if sb.Len() > 0 {
				return strings.TrimSpace(sb.String()), nil
			}
			return "", fmt.Errorf("agent 响应超时")
		case <-ctx.Done():
			if sb.Len() > 0 {
				return strings.TrimSpace(sb.String()), nil
			}
			return "", fmt.Errorf("agent 响应超时")
		}
	}
}

func autoCancelPermissions(conn *Connection, permCh <-chan PermissionNotify) {
	for pn := range permCh {
		_ = conn.Client().RespondPermission(pn.RequestID, "", true)
	}
}

func applyModelOption(ctx context.Context, conn *Connection, sessionID string, opts []acp.SessionConfigOption, modelValue string) error {
	for _, opt := range opts {
		if opt.Select == nil || opt.Select.Category == nil || string(*opt.Select.Category) != "model" {
			continue
		}
		valid := modelInOptions(modelValue, opt.Select.Options)
		if !valid {
			return fmt.Errorf("模型值 %s 不在可用列表中", modelValue)
		}
		return conn.SetConfigOption(ctx, sessionID, string(opt.Select.Id), modelValue)
	}
	return nil
}

func modelInOptions(modelValue string, options acp.SessionConfigSelectOptions) bool {
	if options.Ungrouped != nil {
		for _, o := range *options.Ungrouped {
			if string(o.Value) == modelValue {
				return true
			}
		}
	}
	if options.Grouped != nil {
		for _, g := range *options.Grouped {
			for _, o := range g.Options {
				if string(o.Value) == modelValue {
					return true
				}
			}
		}
	}
	return false
}
