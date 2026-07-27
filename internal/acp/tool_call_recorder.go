package acp

import (
	"log/slog"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// SetToolCallRecordRepo 注入工具调用记录仓库，并挂接终端桥接的退出回调
// （终端能力委托型 agent 的 shell 退出码由此兜底回填）。nil 则不记录。
func (s *Service) SetToolCallRecordRepo(repo *repository.ToolCallRecordRepository) {
	s.toolCallRecords = repo
	if s.terminalBridge != nil {
		s.terminalBridge.SetOnExit(s.handleTerminalExitRecord)
	}
}

// toolCallMeta 是单个 toolCallId 在攒批窗口内合并的 tool_call_update 增量。
type toolCallMeta struct {
	status   string
	exitCode *int
	title    string
	command  string
}

// mergeToolCallDelta 把一条 tool_call_update 的关键字段并入 meta（后到覆盖）。
func mergeToolCallDelta(meta *toolCallMeta, tu *acp.SessionToolCallUpdate) {
	if tu.Status != nil && string(*tu.Status) != "" {
		meta.status = string(*tu.Status)
	}
	if tu.Title != nil && *tu.Title != "" {
		meta.title = *tu.Title
	}
	if code := extractExitCode(tu.RawOutput); code != nil {
		meta.exitCode = code
	}
	if cmd, _ := extractCommandCwd(tu.RawInput); cmd != "" {
		meta.command = cmd
	}
}

// recordToolCallStart 收到 tool_call 时创建记录。shell 调用（kind=execute）解析
// rawInput 中的命令与目录，目录缺省回退到会话工作目录。
func (s *Service) recordToolCallStart(session *models.Session, sessionCwd string, tc *acp.SessionUpdateToolCall) {
	if s.toolCallRecords == nil || tc == nil {
		return
	}
	command, cwd := extractCommandCwd(tc.RawInput)
	if cwd == "" {
		cwd = sessionCwd
	}
	status := string(tc.Status)
	if status == "" {
		status = models.ToolCallStatusPending
	}
	rec := &models.ToolCallRecord{
		UserID:      session.UserID,
		DBSessionID: session.ID,
		ToolCallID:  string(tc.ToolCallId),
		Kind:        string(tc.Kind),
		Title:       tc.Title,
		Command:     command,
		Cwd:         cwd,
		ExitCode:    extractExitCode(tc.RawOutput),
		Status:      status,
		StartedAt:   time.Now(),
	}
	if err := s.toolCallRecords.Create(rec); err != nil {
		slog.Warn("创建工具调用记录失败", "session", session.SessionID, "tool_call", rec.ToolCallID, "err", err)
	}
}

// applyToolCallMeta 把攒批合并的增量落到记录上（flushToolUpdates 时调用）。
func (s *Service) applyToolCallMeta(dbSessionID uint, toolCallID string, meta *toolCallMeta) {
	if s.toolCallRecords == nil || meta == nil {
		return
	}
	err := s.toolCallRecords.ApplyUpdate(dbSessionID, toolCallID, repository.ToolCallUpdateFields{
		Status:   meta.status,
		ExitCode: meta.exitCode,
		Title:    meta.title,
		Command:  meta.command,
	})
	if err != nil {
		slog.Warn("更新工具调用记录失败", "tool_call", toolCallID, "err", err)
	}
}

// linkToolCallTerminal 在 tool_call_update 内嵌 terminal content 时立即写关联
// （一个终端只发生一次，实时写库使终端退出回调能按 terminal_id 命中记录）。
func (s *Service) linkToolCallTerminal(dbSessionID uint, tu *acp.SessionToolCallUpdate) {
	if s.toolCallRecords == nil || tu == nil {
		return
	}
	for _, c := range tu.Content {
		if c.Terminal != nil && c.Terminal.TerminalId != "" {
			err := s.toolCallRecords.ApplyUpdate(dbSessionID, string(tu.ToolCallId),
				repository.ToolCallUpdateFields{TerminalID: c.Terminal.TerminalId})
			if err != nil {
				slog.Warn("关联工具调用终端失败", "tool_call", tu.ToolCallId, "terminal", c.Terminal.TerminalId, "err", err)
			}
			return
		}
	}
}

// handleTerminalExitRecord 是 TerminalBridge 退出回调：按 terminal_id 回填退出码；
// 无关联记录（agent 未内嵌 terminal content 或落库时序竞争）时兜底创建独立 shell 记录，
// 保证委托终端执行的命令始终留有 命令/目录/退出码。
func (s *Service) handleTerminalExitRecord(dbSessionID uint, terminalID, command, cwd string, exitCode *int, signal *string) {
	if s.toolCallRecords == nil || dbSessionID == 0 {
		return
	}
	status := models.ToolCallStatusCompleted
	if signal != nil || (exitCode != nil && *exitCode != 0) {
		status = models.ToolCallStatusFailed
	}
	n, err := s.toolCallRecords.FinishByTerminal(dbSessionID, terminalID, exitCode, status)
	if err != nil {
		slog.Warn("终端退出回填工具调用记录失败", "terminal", terminalID, "err", err)
		return
	}
	if n > 0 {
		return
	}
	// 未按 terminal_id 命中：agent 可能未内嵌 terminal content，改按命令文本对齐
	// 同会话未关联终端的 execute 记录，避免兜底新建造成同一命令重复两条。
	matched, err := s.toolCallRecords.FinishByCommand(dbSessionID, command, terminalID, exitCode, status)
	if err != nil {
		slog.Warn("终端退出按命令回填工具调用记录失败", "terminal", terminalID, "err", err)
		return
	}
	if matched {
		return
	}
	var userID uint
	if sess, err := s.sessions.FindByID(dbSessionID); err == nil {
		userID = sess.UserID
	}
	now := time.Now()
	rec := &models.ToolCallRecord{
		UserID:      userID,
		DBSessionID: dbSessionID,
		ToolCallID:  "term:" + terminalID,
		Kind:        string(acp.ToolKindExecute),
		Title:       command,
		Command:     command,
		Cwd:         cwd,
		TerminalID:  terminalID,
		ExitCode:    exitCode,
		Status:      status,
		StartedAt:   now,
		FinishedAt:  &now,
	}
	if err := s.toolCallRecords.Create(rec); err != nil {
		slog.Warn("创建终端工具调用记录失败", "terminal", terminalID, "err", err)
	}
}

// extractCommandCwd 从 rawInput 中提取 shell 命令与执行目录。
// 不同 agent 字段命名不一：命令统一在 command；目录尝试 cwd / workdir / directory。
func extractCommandCwd(rawInput any) (command, cwd string) {
	m, ok := rawInput.(map[string]any)
	if !ok {
		return "", ""
	}
	if v, ok := m["command"].(string); ok {
		command = v
	}
	for _, key := range []string{"cwd", "workdir", "directory"} {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			cwd = v
			break
		}
	}
	return command, cwd
}

// extractExitCode 从 rawOutput 中提取退出码（如 claude-code 的 {"exitCode":0,...}）。
func extractExitCode(rawOutput any) *int {
	m, ok := rawOutput.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range []string{"exitCode", "exit_code"} {
		if v, ok := m[key].(float64); ok {
			code := int(v)
			return &code
		}
	}
	return nil
}
