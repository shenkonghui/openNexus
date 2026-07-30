package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// 通用 goal 循环：让所有 agent 都能"朝目标持续工作"。
//
// 机制（对齐 Claude Code /goal 的 prompt-based Stop hook 语义）：
//  1. /goal <条件>  设定 goal 并改写为 directive 发给 agent 开始工作；
//  2. 每轮 prompt 正常结束（finalStatus=done）后，goalOnTurnEnd 用小模型评估
//     对话是否满足完成条件（RunPromptOnce 临时会话，不落库）；
//  3. 未达成则携带评估理由自动续轮，达成/超限则清除 goal 并留言。
//
// 命令在客户端侧拦截，优先于 agent 原生同名命令（如 claude-code 的 /goal）；
// 旧前缀名 /opennexus-goal 仍作为兼容别名被解析（不在 "/" 弹窗展示）。

// goalCommandName 是通用 goal 循环的 slash 命令名。
const goalCommandName = "goal"

// legacyGoalCommandName 是历史带前缀命令名，仅兼容解析（存量任务 detail / 用户习惯）。
const legacyGoalCommandName = "opennexus-goal"

// goal 循环的默认限制（GoalSettings 对应字段为 0 时生效）。
const (
	defaultGoalMaxTurns    = 20
	defaultGoalMaxDuration = 60 * time.Minute
	// GoalMaxConditionLen 完成条件上限（对齐 Claude Code /goal 的 4000 字符）；
	// 导出供 MCP create_task 等外部入口截断/校验 goal 条件。
	GoalMaxConditionLen = 4000
	goalMaxConditionLen = GoalMaxConditionLen
	// goalTranscriptMaxMsgs / goalTranscriptMaxChars 控制送评的对话摘录规模。
	goalTranscriptMaxMsgs  = 40
	goalTranscriptMaxChars = 8000
	// goalEvalCallTimeout 单次评估/选角调用的总时长上限（RunPromptOnce 内部另有空闲超时），
	// 评估 agent 思考/调工具阶段可能较长，给足余量避免误杀。
	goalEvalCallTimeout = 5 * time.Minute
)

// sessionGoal 是单个会话的 goal 内存态（服务重启即清空，goal 不跨重启存活）。
type sessionGoal struct {
	Condition  string
	StartedAt  time.Time
	Turns      int    // 已自动续轮次数
	EvalCount  int    // 已执行的达成审计次数（一轮会签算一次）
	LastReason string // 最近一次评估理由
	Evaluating bool   // 评估进行中（防重入）
	// Roles 自动选取的评估角色（空=无匹配，走内置评估；多个时会签评估，全部 YES 才算达成）；
	// RoleResolved 每个 goal 只选一次。
	Roles        []GoalRoleDef
	RoleResolved bool
}

// SetGoalSettingsRepo 注入 goal 设置仓库（评估 agent/模型 + 限制条件）。
func (s *Service) SetGoalSettingsRepo(repo *repository.GoalSettingsRepository) {
	s.goalSettings = repo
}

// goalClearAliases 是清除 goal 的子命令别名（对齐 Claude Code）。
var goalClearAliases = map[string]bool{
	"clear": true, "stop": true, "off": true, "reset": true, "none": true, "cancel": true,
}

// parseGoalCommand 解析 "/goal ..."（含兼容别名 /opennexus-goal）输入。ok=false 表示不是 goal 命令。
// action 取值：set（arg=完成条件）/ status / clear。
func parseGoalCommand(prompt string) (action, arg string, ok bool) {
	trimmed := strings.TrimSpace(prompt)
	var rest string
	matched := false
	for _, name := range []string{goalCommandName, legacyGoalCommandName} {
		cmd := "/" + name
		if trimmed == cmd || strings.HasPrefix(trimmed, cmd+" ") || strings.HasPrefix(trimmed, cmd+"\n") {
			rest = strings.TrimSpace(strings.TrimPrefix(trimmed, cmd))
			matched = true
			break
		}
	}
	if !matched {
		return "", "", false
	}
	if rest == "" {
		return "status", "", true
	}
	if goalClearAliases[strings.ToLower(rest)] {
		return "clear", "", true
	}
	if strings.EqualFold(rest, "status") {
		return "status", "", true
	}
	return "set", rest, true
}

// builtinGoalCommand 是内置 goal 命令描述，供 "/" 弹窗展示（所有 agent 通用）。
func builtinGoalCommand() acp.AvailableCommand {
	return acp.AvailableCommand{
		Name:        goalCommandName,
		Description: "设定目标并自动续轮直至达成（status 查看 / clear 清除）",
		Input: &acp.AvailableCommandInput{
			Unstructured: &acp.UnstructuredCommandInput{Hint: "<完成条件> | status | clear"},
		},
	}
}

// getGoal 返回 goal 的值快照（锁内拷贝），供只读展示使用；
// 避免调用方在锁外读到评估 goroutine 写了一半的字段（如 Roles 的 slice header）。
func (s *Service) getGoal(sessionID string) (sessionGoal, bool) {
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	g, ok := s.goals[sessionID]
	if !ok {
		return sessionGoal{}, false
	}
	return *g, true
}

func (s *Service) setGoal(sessionID, condition string) {
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	s.goals[sessionID] = &sessionGoal{Condition: condition, StartedAt: time.Now()}
}

// goalDirective 返回附加给 agent 的目标工作指示（/goal set 与任务自动开启共用）。
func goalDirective(condition string) string {
	return "请朝以下目标持续工作。每轮结束后系统会自动评估是否达成，未达成会要求你继续，无需向用户确认。\n\n目标（完成条件）：\n" + condition
}

// EnableGoal 以编程方式为会话开启 goal 模式（等价 /goal <条件>），供编排任务自动开启使用。
// 会话已有生效 goal 时不重置（保留续轮/审计计数），仅重新通知 active 快照。
func (s *Service) EnableGoal(sessionID, condition string) error {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return fmt.Errorf("goal 完成条件不能为空")
	}
	if len(condition) > goalMaxConditionLen {
		return fmt.Errorf("goal 完成条件过长（%d 字符），上限 %d 字符", len(condition), goalMaxConditionLen)
	}
	session, err := s.sessions.FindBySessionID(sessionID)
	if err != nil {
		return fmt.Errorf("会话不存在: %w", err)
	}
	if _, ok := s.getGoal(sessionID); !ok {
		s.setGoal(sessionID, condition)
		s.recordGoalEvent(session, "设定 goal（任务自动开启）："+condition, models.ToolCallStatusCompleted)
		slog.Info("goal 已自动开启", "session", sessionID, "agent", session.AgentType, "chars", len(condition))
	}
	if g, ok := s.getGoal(sessionID); ok {
		s.notifyGoalState(session, models.TaskGoalStatusActive, &g, "")
	}
	return nil
}

// recordGoalEvent 把 goal 生命周期事件写入工具调用记录（kind=goal），
// 使会话「记录」面板与工具调用记录页能看到设定/评估/续轮/终止轨迹。
func (s *Service) recordGoalEvent(session *models.Session, title, status string) {
	if s.toolCallRecords == nil || session == nil {
		return
	}
	now := time.Now()
	rec := &models.ToolCallRecord{
		UserID:      session.UserID,
		DBSessionID: session.ID,
		ToolCallID:  fmt.Sprintf("goal:%s:%d", session.SessionID, now.UnixNano()),
		Kind:        "goal",
		Title:       title,
		Status:      status,
		StartedAt:   now,
		FinishedAt:  &now,
	}
	if err := s.toolCallRecords.Create(rec); err != nil {
		slog.Warn("创建 goal 事件记录失败", "session", session.SessionID, "err", err)
	}
}

func (s *Service) clearGoal(sessionID string) bool {
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	_, ok := s.goals[sessionID]
	delete(s.goals, sessionID)
	return ok
}

// interceptGoal 在 PromptWithExecution 入口处拦截 /goal 命令（所有 agent 通用）。
// 返回 handled=true 时调用方直接返回 ch（status/clear 走合成回复，不打扰 agent）；
// handled=false 时继续正常流程，set 场景会把 *promptForAgent 改写为 goal directive。
func (s *Service) interceptGoal(session *models.Session, sessionID, prompt string, executionID *uint, promptForAgent *string) (handled bool, ch <-chan models.Message) {
	action, arg, ok := parseGoalCommand(prompt)
	if !ok {
		return false, nil
	}
	switch action {
	case "set":
		if len(arg) > goalMaxConditionLen {
			return true, s.syntheticCommandReply(session, prompt, fmt.Sprintf("⚠️ goal 完成条件过长（%d 字符），上限 %d 字符。", len(arg), goalMaxConditionLen), executionID)
		}
		s.setGoal(sessionID, arg)
		s.recordGoalEvent(session, "设定 goal："+arg, models.ToolCallStatusCompleted)
		if g, ok := s.getGoal(sessionID); ok {
			s.notifyGoalState(session, models.TaskGoalStatusActive, &g, "")
		}
		slog.Info("goal 已设定", "session", sessionID, "agent", session.AgentType, "chars", len(arg))
		*promptForAgent = goalDirective(arg)
		return false, nil
	case "clear":
		if s.clearGoal(sessionID) {
			s.recordGoalEvent(session, "goal 已手动清除", models.ToolCallStatusCompleted)
			s.notifyGoalState(session, "", nil, "")
			return true, s.syntheticCommandReply(session, prompt, "✅ goal 已清除，本会话不再自动续轮。", executionID)
		}
		return true, s.syntheticCommandReply(session, prompt, "当前会话没有生效中的 goal。", executionID)
	default: // status
		g, ok := s.getGoal(sessionID)
		if !ok {
			return true, s.syntheticCommandReply(session, prompt, "当前会话没有生效中的 goal。用 /goal <完成条件> 设定。", executionID)
		}
		text := fmt.Sprintf("🎯 goal 生效中\n\n完成条件：%s\n\n已自动续轮：%d 次\n已审计：%d 次\n持续时间：%s", g.Condition, g.Turns, g.EvalCount, time.Since(g.StartedAt).Round(time.Second))
		if len(g.Roles) > 1 {
			text += "\n评估角色：" + goalRoleNames(g.Roles) + "（自动选取，会签评估）"
		} else if len(g.Roles) == 1 {
			text += "\n评估角色：" + g.Roles[0].Name + "（自动选取）"
		}
		if g.LastReason != "" {
			text += "\n最近评估：" + g.LastReason
		}
		return true, s.syntheticCommandReply(session, prompt, text, executionID)
	}
}

// syntheticCommandReply 持久化"用户命令 + 合成回复"两条消息，返回已含消息并关闭的 channel。
// 用于 status/clear 这类不需要打扰 agent 的本地命令回复。
func (s *Service) syntheticCommandReply(session *models.Session, prompt, reply string, executionID *uint) <-chan models.Message {
	seq := s.getNextSequence(session.SessionID)
	userMsg := MapUpdate(session.SessionID, session.ID, seq+1, acp.SessionUpdate{
		UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
			Content:       acp.ContentBlock{Text: &acp.ContentBlockText{Text: prompt, Type: "text"}},
			SessionUpdate: "user_message_chunk",
		},
	})
	userMsg.ExecutionID = executionID
	agentMsg := MapUpdate(session.SessionID, session.ID, seq+2, acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content:       acp.ContentBlock{Text: &acp.ContentBlockText{Text: reply, Type: "text"}},
			SessionUpdate: "agent_message_chunk",
		},
	})
	agentMsg.ExecutionID = executionID
	out := make(chan models.Message, 2)
	for _, m := range []models.Message{userMsg, agentMsg} {
		if err := s.messages.Create(&m); err != nil {
			slog.Error("持久化 goal 合成消息失败", "session", session.SessionID, "sequence", m.Sequence, "err", err)
		}
		out <- m
	}
	close(out)
	// 本地合成回复（/shell、/goal status|clear 等）不发给 agent、不走正常 prompt
	// 生命周期，因此永远不会触发下方的 PromptFinished 收尾。但发送前 handler 已把
	// 会话登记任务置为 running（RegisterSessionTask），若不在此收尾，已完成任务发
	// 这类命令后会永远卡在“执行中”。这里立即以 done 通知任务管理，让运行态回落到
	// 真实终态（goal 生效中的任务由任务侧 goalActive 判定继续保持 running）。
	if s.promptFinished != nil {
		s.promptFinished.PromptFinished(session.ID, models.RunningTaskStatusDone)
	}
	return out
}

// goalNotify 向会话追加一条 goal 系统留言（评估结论 / 终止原因），持久化并广播（若有订阅者）。
func (s *Service) goalNotify(session *models.Session, text string) {
	seq := s.getNextSequence(session.SessionID) + 1
	msg := MapUpdate(session.SessionID, session.ID, seq, acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content:       acp.ContentBlock{Text: &acp.ContentBlockText{Text: text, Type: "text"}},
			SessionUpdate: "agent_message_chunk",
		},
	})
	if err := s.messages.Create(&msg); err != nil {
		slog.Error("持久化 goal 留言失败", "session", session.SessionID, "err", err)
	}
	s.mu.RLock()
	bc := s.activePrompts[session.SessionID]
	s.mu.RUnlock()
	if bc != nil {
		bc.broadcast(msg)
	}
}

// notifyGoalState 把 goal 状态快照回调给任务管理（若已接线），由任务侧写回 tasks.json，
// 使任务列表能展示 goal 生命周期（生效中/评估中/已达成/已终止）。g 为 nil 表示 goal 已清除。
// reason 非空时覆盖 g.LastReason 作为最近说明（终止原因/评估结论）。
func (s *Service) notifyGoalState(session *models.Session, status string, g *sessionGoal, reason string) {
	if s.goalStateNotifier == nil || session == nil {
		return
	}
	if g == nil {
		s.goalStateNotifier.GoalStateChanged(session.ID, nil)
		return
	}
	if reason == "" {
		reason = g.LastReason
	}
	state := &models.TaskGoalState{
		Condition:  g.Condition,
		Status:     status,
		Turns:      g.Turns,
		EvalCount:  g.EvalCount,
		LastReason: reason,
		UpdatedAt:  time.Now(),
	}
	for _, r := range g.Roles {
		state.Roles = append(state.Roles, r.Name)
	}
	s.goalStateNotifier.GoalStateChanged(session.ID, state)
}

// goalOnTurnEnd 在 prompt 流正常结束（finalStatus=done）后被调用（独立 goroutine）。
// 有生效 goal 时评估是否达成并决定续轮/清除。
func (s *Service) goalOnTurnEnd(sessionID string) {
	s.goalMu.Lock()
	g, ok := s.goals[sessionID]
	if !ok || g.Evaluating {
		s.goalMu.Unlock()
		return
	}
	g.Evaluating = true
	s.goalMu.Unlock()
	defer func() {
		s.goalMu.Lock()
		if cur, ok := s.goals[sessionID]; ok {
			cur.Evaluating = false
		}
		s.goalMu.Unlock()
	}()
	// 循环驱动：续轮 prompt 结束后直接评估下一轮，不依赖 finisher 再次触发 goalOnTurnEnd——
	// 彼时本 goroutine 尚未退出，Evaluating 仍为 true，新触发会被去重丢弃，
	// 曾导致第 2 轮起 goal 循环停摆（不再评估、任务永远 running）。
	for s.evaluateAndContinueGoal(sessionID, g) {
	}
}

// evaluateAndContinueGoal 执行一次 goal 评估：限制检查 → 小模型评估 → 续轮或终止。
// 返回 true 表示已续轮且新一轮已结束，调用方应继续下一次评估；返回 false 表示循环终止。
func (s *Service) evaluateAndContinueGoal(sessionID string, g *sessionGoal) bool {
	// goal 已被清除/重设（如续轮期间用户 CancelSession 或 /goal clear）则终止循环，
	// 避免拿着旧 goal 继续评估、与用户意图对抗。
	s.goalMu.Lock()
	cur, ok := s.goals[sessionID]
	s.goalMu.Unlock()
	if !ok || cur != g {
		return false
	}
	session, err := s.GetSession(sessionID)
	if err != nil {
		s.clearGoal(sessionID)
		return false
	}

	// 限制条件（评估 agent/模型也来自同一设置）
	maxTurns := defaultGoalMaxTurns
	maxDuration := defaultGoalMaxDuration
	evalAgent := session.AgentType
	evalModel := ""
	if s.goalSettings != nil {
		if gs, err := s.goalSettings.FindByUserID(session.UserID); err == nil {
			if gs.MaxTurns > 0 {
				maxTurns = gs.MaxTurns
			}
			if gs.MaxDurationMinutes > 0 {
				maxDuration = time.Duration(gs.MaxDurationMinutes) * time.Minute
			}
			if strings.TrimSpace(gs.AgentType) != "" {
				evalAgent = strings.TrimSpace(gs.AgentType)
				evalModel = strings.TrimSpace(gs.ModelValue)
			}
		}
	}
	if g.Turns >= maxTurns {
		s.clearGoal(sessionID)
		reason := fmt.Sprintf("自动续轮达到上限（%d 次）", maxTurns)
		s.recordGoalEvent(session, fmt.Sprintf("goal 终止：%s，已审计 %d 次", reason, g.EvalCount), models.ToolCallStatusFailed)
		s.notifyGoalState(session, models.TaskGoalStatusStopped, g, reason)
		s.goalNotify(session, fmt.Sprintf("⏹️ goal 已终止：%s（已审计 %d 次）。可重新 /goal 设定。", reason, g.EvalCount))
		return false
	}
	if time.Since(g.StartedAt) >= maxDuration {
		s.clearGoal(sessionID)
		reason := fmt.Sprintf("持续时间超过上限（%s）", maxDuration)
		s.recordGoalEvent(session, fmt.Sprintf("goal 终止：%s，已审计 %d 次", reason, g.EvalCount), models.ToolCallStatusFailed)
		s.notifyGoalState(session, models.TaskGoalStatusStopped, g, reason)
		s.goalNotify(session, fmt.Sprintf("⏹️ goal 已终止：%s（已审计 %d 次）。可重新 /goal 设定。", reason, g.EvalCount))
		return false
	}

	transcript := s.goalTranscript(session.SessionID)

	// 首次评估时自动选取评估角色（文件式定义，可多选会签；无候选/选取失败则回退内置评估）。
	if !g.RoleResolved {
		roles, _ := s.GoalRolesSnapshot(sessionCwd(session, s.workspaces))
		var selected []GoalRoleDef
		if len(roles) > 0 {
			selCtx, selCancel := context.WithTimeout(context.Background(), goalEvalCallTimeout)
			selBox := s.startGoalEvalBox(session, "🎯 goal 评估角色选取")
			selected = s.selectGoalRoles(selCtx, evalAgent, evalModel, g.Condition, roles, selBox.OnText)
			selCancel()
			if len(selected) > 0 {
				selBox.Finish(false, "→ 选定评估角色："+goalRoleNames(selected))
			} else {
				selBox.Finish(false, "→ 无匹配角色，使用内置评估")
			}
		}
		// 锁内发布，避免 status 分支并发读到写了一半的 slice header
		s.goalMu.Lock()
		g.RoleResolved = true
		g.Roles = selected
		s.goalMu.Unlock()
		if len(selected) > 1 {
			s.recordGoalEvent(session, fmt.Sprintf("goal 评估角色选定（会签）：%s", goalRoleNames(selected)), models.ToolCallStatusCompleted)
			s.goalNotify(session, fmt.Sprintf("🎭 goal 评估角色已自动选定 %d 个（会签评估，全部通过才算达成）：%s", len(selected), goalRoleNames(selected)))
		} else if len(selected) == 1 {
			s.recordGoalEvent(session, fmt.Sprintf("goal 评估角色选定：%s", selected[0].Name), models.ToolCallStatusCompleted)
			s.goalNotify(session, fmt.Sprintf("🎭 goal 评估角色已自动选定：%s（%s）", selected[0].Name, selected[0].Description))
		}
	}

	// 一轮评估（含会签多角色）算一次审计，进入评估前递增；
	// 锁内写入，避免与 status 分支的快照读取竞争。
	s.goalMu.Lock()
	g.EvalCount++
	s.goalMu.Unlock()

	s.notifyGoalState(session, models.TaskGoalStatusEvaluating, g, "")

	achieved, reason, evalErr := s.runGoalEvaluation(session, g, evalAgent, evalModel, transcript)
	if evalErr != nil {
		// 评估失败保守终止，避免无评估依据地无限续轮
		s.clearGoal(sessionID)
		s.recordGoalEvent(session, fmt.Sprintf("goal 评估失败：%v，已审计 %d 次", evalErr, g.EvalCount), models.ToolCallStatusFailed)
		s.notifyGoalState(session, models.TaskGoalStatusStopped, g, fmt.Sprintf("评估失败：%v", evalErr))
		s.goalNotify(session, fmt.Sprintf("⚠️ goal 评估失败（%v），已停止自动续轮（已审计 %d 次）。可重新 /goal 设定。", evalErr, g.EvalCount))
		return false
	}

	if achieved {
		s.clearGoal(sessionID)
		recTitle := "goal 评估：已达成"
		if reason != "" {
			recTitle += "（" + reason + "）"
		}
		s.recordGoalEvent(session, recTitle, models.ToolCallStatusCompleted)
		s.notifyGoalState(session, models.TaskGoalStatusAchieved, g, reason)
		msg := "🎯 goal 已达成，自动续轮结束。"
		if reason != "" {
			msg += "\n评估：" + reason
		}
		s.goalNotify(session, msg)
		slog.Info("goal 达成", "session", sessionID, "turns", g.Turns)
		return false
	}

	// 未达成：自动续轮
	s.goalMu.Lock()
	if cur, ok := s.goals[sessionID]; ok {
		cur.Turns++
		cur.LastReason = reason
	} else {
		// 评估期间被用户 clear/cancel，放弃续轮
		s.goalMu.Unlock()
		return false
	}
	s.goalMu.Unlock()

	s.notifyGoalState(session, models.TaskGoalStatusActive, g, reason)

	contPrompt := "自动评估：goal 尚未达成，请继续。\n\n目标（完成条件）：\n" + g.Condition
	if reason != "" {
		contPrompt += "\n\n未达成原因：" + reason
	}
	contPrompt += "\n\n请继续推进直到满足完成条件，无需向用户确认。"
	recTitle := fmt.Sprintf("goal 评估：未达成，自动续轮（第 %d 次）", g.Turns)
	if reason != "" {
		recTitle += "：" + reason
	}
	s.recordGoalEvent(session, recTitle, models.ToolCallStatusCompleted)
	slog.Info("goal 未达成，自动续轮", "session", sessionID, "turn", g.Turns, "reason", reason)

	// 续轮 prompt：若会话繁忙（前端发送队列等并发场景），间隔重试而非直接终止。
	// 前端有 goal_active 检查 + 后端 HasActivePrompt 互斥，理论上极少触发；
	// 此重试是安全网，防止极端时序下 goal 被意外终止。
	var ch <-chan models.Message
	for retry := 0; retry < 5; retry++ {
		// 重试前确认 goal 未被用户清除
		s.goalMu.Lock()
		if _, ok := s.goals[sessionID]; !ok {
			s.goalMu.Unlock()
			return false
		}
		s.goalMu.Unlock()

		ch, err = s.PromptWithExecution(context.Background(), sessionID, contPrompt, nil)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrSessionBusy) {
			break
		}
		slog.Info("goal 续轮时会话繁忙，等待后重试", "session", sessionID, "retry", retry+1)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		s.clearGoal(sessionID)
		s.recordGoalEvent(session, fmt.Sprintf("goal 自动续轮失败：%v，已审计 %d 次", err, g.EvalCount), models.ToolCallStatusFailed)
		s.notifyGoalState(session, models.TaskGoalStatusStopped, g, fmt.Sprintf("自动续轮失败：%v", err))
		s.goalNotify(session, fmt.Sprintf("⚠️ goal 自动续轮失败（%v），已停止。", err))
		return false
	}
	// 必须消费主订阅 channel，否则 buffer 满会阻塞 prompt 消费 goroutine；
	// 前端经 5s 轮询 + subscribeStream 断点续传照常收到消息。
	for range ch {
	}
	// 新一轮已结束，由调用方循环继续下一次评估
	return true
}

// runGoalEvaluation 执行一次 goal 评估。无角色时走内置单评估；
// 有角色时会签评估（默认开启）：逐角色独立调用评估，全部 YES 才算达成；
// 任一角色评估调用失败即返回 err（调用方保守终止）。
func (s *Service) runGoalEvaluation(session *models.Session, g *sessionGoal, evalAgent, evalModel, transcript string) (bool, string, error) {
	if len(g.Roles) == 0 {
		verdict, err := s.runGoalEvalOnce(session, "🎯 goal 评估", evalAgent, evalModel, buildGoalEvalPrompt(nil, g.Condition, transcript))
		if err != nil {
			return false, "", err
		}
		achieved, reason := parseGoalVerdict(verdict)
		return achieved, reason, nil
	}

	achieved := true
	var reasons []string
	for i := range g.Roles {
		role := &g.Roles[i]
		// 评估 agent/model 优先级：角色 > GoalSettings > 会话 agent（逐角色独立生效）。
		rAgent, rModel := evalAgent, evalModel
		if role.Agent != "" {
			rAgent, rModel = role.Agent, role.Model
		} else if role.Model != "" {
			rModel = role.Model
		}
		verdict, err := s.runGoalEvalOnce(session, "🎯 goal 评估 · "+role.Name, rAgent, rModel, buildGoalEvalPrompt(role, g.Condition, transcript))
		if err != nil {
			return false, "", fmt.Errorf("角色 %s 评估失败: %w", role.Name, err)
		}
		ok, reason := parseGoalVerdict(verdict)
		if !ok {
			achieved = false
		}
		if len(g.Roles) > 1 {
			// 会签：逐角色留痕，「记录」面板可见各角色判定；汇总理由带角色名前缀。
			head := "YES"
			if !ok {
				head = "NO"
			}
			title := fmt.Sprintf("goal 会签评估 %s：%s", role.Name, head)
			if reason != "" {
				title += "（" + reason + "）"
			}
			s.recordGoalEvent(session, title, models.ToolCallStatusCompleted)
			summary := head
			if reason != "" {
				summary = reason
			}
			reasons = append(reasons, role.Name+": "+summary)
		} else if reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return achieved, strings.Join(reasons, "；"), nil
}

// runGoalEvalOnce 带超时执行一次评估调用（临时会话），
// 输出经评估子框内嵌到主会话（tool_call 折叠框，流式落盘）。
func (s *Service) runGoalEvalOnce(session *models.Session, title, agent, model, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), goalEvalCallTimeout)
	defer cancel()
	box := s.startGoalEvalBox(session, title)
	out, err := s.RunPromptOnceStream(ctx, agent, model, prompt, box.OnText)
	if err != nil {
		box.Finish(true, fmt.Sprintf("⚠️ 评估调用失败：%v", err))
	} else {
		box.Finish(false, "")
	}
	return out, err
}

// parseGoalVerdict 解析评估器输出：首行 YES/NO，次行理由。
// 首行无法识别时视为未达成（保守），整段输出作为理由。
func parseGoalVerdict(out string) (achieved bool, reason string) {
	lines := strings.SplitN(strings.TrimSpace(out), "\n", 2)
	head := strings.ToUpper(strings.TrimSpace(strings.Trim(lines[0], "*`# ")))
	if len(lines) > 1 {
		reason = strings.TrimSpace(lines[1])
	}
	if strings.HasPrefix(head, "YES") {
		return true, reason
	}
	if strings.HasPrefix(head, "NO") {
		return false, reason
	}
	return false, strings.TrimSpace(out)
}

// goalTranscript 取最近的用户/助手文本消息拼装评估用对话摘录。
func (s *Service) goalTranscript(stableSessionID string) string {
	msgs, err := s.messages.FindBySessionIDLastN(stableSessionID, goalTranscriptMaxMsgs)
	if err != nil {
		return "(无法读取对话记录)"
	}
	var sb strings.Builder
	for _, m := range msgs {
		if m.Kind != models.MessageKindUserMessageChunk && m.Kind != models.MessageKindAgentMessageChunk {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		role := "assistant"
		if m.Role == models.MessageRoleUser {
			role = "user"
		}
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	out := sb.String()
	if len(out) > goalTranscriptMaxChars {
		out = "...(前文省略)\n" + out[len(out)-goalTranscriptMaxChars:]
	}
	if strings.TrimSpace(out) == "" {
		return "(暂无文本对话)"
	}
	return out
}
