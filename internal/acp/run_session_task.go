package acp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"opennexus/internal/models"
)

// SessionTaskResult 描述一次会话任务的执行结果。
type SessionTaskResult struct {
	SessionID   string // 落库的稳定 session_id（UUID），可用于后续继续对话
	DBSessionID uint   // 落库的 DB 主键 ID，前端路由 /sessions/:id 使用
	Result      string // 收集到的 assistant 文本
	Success     bool
	Error       string // 失败原因（Success=true 时为空）
	// Interrupted 表示 prompt 以中断收尾（超时/断开），调用方应置任务为 interrupt 而非 failed。
	Interrupted bool
	// Background 表示本方法已返回但会话仍在后台运行（收集超时/调用方取消），
	// 调用方应保持任务 running，由 PromptFinished 回调在 prompt 真正结束时收尾。
	Background bool
}

// SessionTaskConfig 描述一次会话任务的参数（用于 run_session_task MCP 工具）。
//
// 与 SubAgentRunConfig 的差异：本方法创建持久 DB 会话（落库），可通过 UI 继续对话；
// SubAgentRunConfig 创建临时 ACP 会话，不落库，仅返回文本。
type SessionTaskConfig struct {
	AgentType       string        // 已注册的 agent type，必填
	ModelValue      string        // 模型值，空=用 agent 默认
	Prompt          string        // 用户任务文本（由 MCP 工具传入）
	UserID          uint          // 会话归属用户，必填
	WorkspaceID     uint          // 0=查找/创建默认工作区
	ParentSessionID *uint         // 非 nil 时创建子会话，关联父会话
	Source          string        // 会话来源，空=manual
	Timeout         time.Duration // 运行超时，0=默认 sessionTaskTimeout (300s)
	Cwd             string        // 工作目录覆盖，非空时覆盖工作区 cwd（如编排任务的 git worktree 路径）
	// Goal 非空时为会话自动开启 goal 模式（等价 /goal <条件>）：每轮结束自动审计，
	// 达成前任务保持 running，prompt 末尾会附加目标指示。
	Goal string
	// OnSessionCreated 在会话落库后、发送 prompt 前立即回调，便于调用方尽早拿到 db_session_id
	//（RunSessionTask 会阻塞到超时/完成才返回，故需此回调支持“启动后立即跳转会话”）。
	OnSessionCreated func(dbSessionID uint, sessionID string)
}

// sessionTaskTimeout 是 RunSessionTask 的默认超时。
// 比 promptOnceTimeout (60s) 更长，因为持久会话可能执行较重的多步任务。
const sessionTaskTimeout = 5 * time.Minute

// RunSessionTask 创建一个持久会话（落库），阻塞运行一次性任务并收集 assistant 文本后返回。
//
// 与 RunSubAgent 的差异：
//   - 创建持久 DB 会话（可通过 UI 继续对话），而非临时 ACP 会话
//   - 首次 Prompt 时延迟激活会话（建立 ACP 连接 + NewSession），消息正常落库
//   - 权限请求走会话级权限通道（UI 可响应），而非自动拒绝
//
// 上下文生命周期：prompt 使用 detached context (context.Background) 发送，
// 使持久会话独立于本次 MCP 工具调用存活——即使 MCP 调用超时或 ctx 被取消，
// 会话的 prompt 流仍继续运行，用户可在 UI 中响应权限请求、查看后续消息。
// 本方法仅在 timeout 内阻塞收集结果；超时后返回已收集的部分文本，会话继续运行。
//
// 该方法适用于：主 agent 通过 MCP 工具发起一个需要持久化、可追溯、可继续的子任务。
func (s *Service) RunSessionTask(ctx context.Context, cfg SessionTaskConfig) (SessionTaskResult, error) {
	if strings.TrimSpace(cfg.Prompt) == "" {
		return SessionTaskResult{}, fmt.Errorf("prompt 不能为空")
	}
	if _, err := s.GetBackend(cfg.AgentType); err != nil {
		return SessionTaskResult{}, err
	}

	source := strings.TrimSpace(cfg.Source)
	if source == "" {
		source = models.SessionSourceManual
	}

	// 创建会话用调用方 ctx（感知取消）；发送 prompt 用 detached context（独立存活）。
	session, err := s.createSessionFull(ctx, cfg.AgentType, cfg.WorkspaceID, cfg.UserID, source, cfg.ModelValue, cfg.ParentSessionID, cfg.Cwd)
	if err != nil {
		return SessionTaskResult{}, fmt.Errorf("创建会话: %w", err)
	}

	result := SessionTaskResult{SessionID: session.SessionID, DBSessionID: session.ID}

	// 会话已落库：尽早回调，供调用方（如编排引擎）立即记录 db_session_id 并跳转会话。
	if cfg.OnSessionCreated != nil {
		cfg.OnSessionCreated(session.ID, session.SessionID)
	}

	// 用 detached context 发送 prompt：持久会话的生命周期独立于 MCP 工具调用。
	// 否则 MCP 工具返回（或超时取消 ctx）会取消 prompt 流，导致 agent 进程被终止、
	// 权限请求被移除——用户在 UI 响应权限时报"权限请求不存在或已过期"。
	// 用 detached context 后，会话继续运行，用户可在 UI 正常交互。
	// 注意：不 cancel 此 context——prompt 自然结束后 SDK goroutine 自行退出，
	// context 被回收；提前 cancel 会再次杀死持久会话。
	promptCtx := context.Background()

	// 任务定义了 goal：在发送 prompt 前开启 goal 模式（此时 OnSessionCreated 已回写
	// db_session_id，active 快照能对上任务条目），并在 prompt 末尾附加目标指示。
	prompt := cfg.Prompt
	if goal := strings.TrimSpace(cfg.Goal); goal != "" {
		if err := s.EnableGoal(session.SessionID, goal); err != nil {
			slog.Warn("任务自动开启 goal 失败，按无 goal 继续执行", "session", session.SessionID, "err", err)
		} else {
			prompt += "\n\n" + goalDirective(goal)
		}
	}

	updates, err := s.PromptWithExecution(promptCtx, session.SessionID, prompt, nil)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("发送任务失败: %v", err)
		return result, nil
	}

	// 阻塞消费消息流，收集 assistant 文本；超时后返回已收集的部分，会话继续运行。
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = sessionTaskTimeout
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var sb strings.Builder
consume:
	for {
		select {
		case msg, ok := <-updates:
			if !ok {
				break consume
			}
			if msg.Kind == models.MessageKindAgentMessageChunk {
				sb.WriteString(msg.Content)
			}
		case <-runCtx.Done():
			// 超时：返回已收集的部分文本。会话仍在后台运行（detached context 未取消），
			// 用户可在 UI 继续对话、响应权限。标记 Background：调用方不写终态，
			// 由 PromptFinished 在 prompt 真正结束时按真实终态收尾，避免提前误判完成。
			result.Success = sb.Len() > 0
			result.Background = true
			result.Result = strings.TrimSpace(sb.String())
			if sb.Len() == 0 {
				result.Error = "agent 响应超时，任务仍在后台运行"
			}
			return result, nil
		case <-ctx.Done():
			// MCP 调用方取消（如父 agent 放弃工具调用）：同样返回已收集的部分，
			// 会话继续在后台运行（promptCtx 未被取消），同样标记 Background。
			result.Success = sb.Len() > 0
			result.Background = true
			result.Result = strings.TrimSpace(sb.String())
			if sb.Len() == 0 {
				result.Error = "调用已取消，任务仍在后台运行"
			}
			return result, nil
		}
	}

	// 消息流正常关闭：prompt 消费 goroutine 已先落库终态，回查区分 done/interrupted。
	// 中断收尾（agent 卡死超时/连接断开）不算成功，调用方应置任务为 interrupt 可重发。
	if s.LastRunStatus(session.ID) == models.RunningTaskStatusInterrupted {
		result.Success = false
		result.Interrupted = true
		result.Result = strings.TrimSpace(sb.String())
		result.Error = "执行中断（超时或连接断开），可重发继续"
		return result, nil
	}

	result.Success = true
	result.Result = strings.TrimSpace(sb.String())
	return result, nil
}
