package acp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
)

// 沙箱效果测试单项状态。
const (
	SecTestPassed  = "passed"  // 沙箱行为符合预期（阻止了该阻止的，放行了该放行的）
	SecTestFailed  = "failed"  // 沙箱行为不符合预期（未阻止或误拦）
	SecTestPartial = "partial" // 模糊：无法确定命令执行结果
	SecTestError   = "error"   // 测试流程本身出错（连接失败等）
	SecTestSkipped = "skipped" // 跳过（用例未启用或无 prompt）
)

// SecurityTestCaseInput 是从 handler 层传入的沙箱测试用例（与持久化模型解耦，便于跨层传递）。
type SecurityTestCaseInput struct {
	ID            uint   `json:"id"`
	Name          string `json:"name"`
	Category      string `json:"category"`
	Prompt        string `json:"prompt"`
	ExpectBlocked bool   `json:"expect_blocked"` // true=沙箱应阻止；false=沙箱应放行
}

// ToolCallAttempt 记录 agent 在测试期间执行的一次工具调用及其结果。
type ToolCallAttempt struct {
	Title    string `json:"title"`               // 工具调用标题/命令
	Status   string `json:"status,omitempty"`    // completed/failed
	ExitCode *int   `json:"exit_code,omitempty"` // 退出码（nil=未知）
	Outcome  string `json:"outcome"`             // "approved"（沙箱测试中一律批准执行）
}

// SecurityTestItem 是单个测试用例的评估结果。
type SecurityTestItem struct {
	CaseID    string            `json:"case_id"`
	Name      string            `json:"name"`
	Category  string            `json:"category,omitempty"`
	Status    string            `json:"status"` // 见 SecTest* 常量
	Detail    string            `json:"detail,omitempty"`
	ToolCalls []ToolCallAttempt `json:"tool_calls,omitempty"` // agent 执行的工具调用及结果
	Response  string            `json:"response,omitempty"`   // agent 回复（截断）
}

// SecurityTestReport 是一次 agent 沙箱效果测试的完整报告。
// 结果按 agentType 内存缓存（重启失效），供设置页展示最近一次测试。
type SecurityTestReport struct {
	AgentType   string             `json:"agent_type"`
	Mode        string             `json:"mode"` // 固定 "sandbox"（沙箱效果测试）
	Items       []SecurityTestItem `json:"items"`
	RawResponse string             `json:"raw_response,omitempty"` // 拼接的各用例回复（截断）
	TestedAt    time.Time          `json:"tested_at"`
	DurationMs  int64              `json:"duration_ms"`
	Model       string             `json:"model,omitempty"`
	Error       string             `json:"error,omitempty"`
}

// SecurityTestBatchResult 是一次批量沙箱测试（全部 agent）的聚合报告。
type SecurityTestBatchResult struct {
	Reports    []SecurityTestReport `json:"reports"`
	Total      int                  `json:"total"`
	TestedAt   time.Time            `json:"tested_at"`
	DurationMs int64                `json:"duration_ms"`
}

// secTestPromptTimeout / secTestRawResponseLimit 与能力测试一致。
const (
	secTestPromptTimeout    = 120 * time.Second
	secTestRawResponseLimit = 4000
)

// sandboxBlockPatterns 表示沙箱阻止操作时的常见错误信息关键字（中英文）。
// 当工具调用退出码缺失时，用这些关键字从 agent 回复中推断命令是否被沙箱阻止。
var sandboxBlockPatterns = []string{
	"permission denied", "operation not permitted", "read-only file system",
	"permission-denied", "operation not allowed", "access denied",
	"权限不足", "只读文件系统", "操作不允许", "权限被拒绝", "无法写入",
}

// successPatterns 表示命令执行成功的常见关键字。
var successPatterns = []string{
	"success", "succeeded", "done", "created", "written", "completed",
	"成功", "完成", "已创建", "已写入", "已执行",
}

// collectSandboxStream 收集 prompt 更新流中的 assistant 文本与工具调用结果，
// 直到流结束或超时。同时从 ToolCallUpdate 事件中提取退出码和状态。
func collectSandboxStream(ctx context.Context, updates <-chan acp.SessionUpdate, timeout time.Duration) (string, []ToolCallAttempt, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var sb strings.Builder
	toolCallMap := make(map[string]*ToolCallAttempt)
	var order []string

	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return strings.TrimSpace(sb.String()), orderedToolCalls(toolCallMap, order), nil
			}
			collectSandboxUpdate(u, &sb, toolCallMap, &order)
		case <-runCtx.Done():
			return strings.TrimSpace(sb.String()), orderedToolCalls(toolCallMap, order), fmt.Errorf("agent 响应超时")
		}
	}
}

// collectSandboxUpdate 处理单条 ACP 流更新：agent 文本追加到 builder，工具调用结果记入 map。
func collectSandboxUpdate(u acp.SessionUpdate, sb *strings.Builder, toolCallMap map[string]*ToolCallAttempt, order *[]string) {
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
		sb.WriteString(u.AgentMessageChunk.Content.Text.Text)
	}
	if u.ToolCallUpdate == nil {
		return
	}
	tu := u.ToolCallUpdate
	id := string(tu.ToolCallId)
	if id == "" {
		return
	}
	tc, exists := toolCallMap[id]
	if !exists {
		tc = &ToolCallAttempt{Outcome: "approved"}
		toolCallMap[id] = tc
		*order = append(*order, id)
	}
	mergeToolCallResult(tc, tu)
}

// mergeToolCallResult 把 ToolCallUpdate 的字段并入 ToolCallAttempt（后到覆盖）。
func mergeToolCallResult(tc *ToolCallAttempt, tu *acp.SessionToolCallUpdate) {
	if tu.Title != nil && *tu.Title != "" {
		tc.Title = *tu.Title
	}
	if tu.Status != nil && string(*tu.Status) != "" {
		tc.Status = string(*tu.Status)
	}
	if code := extractExitCode(tu.RawOutput); code != nil {
		tc.ExitCode = code
	}
	if tc.Title == "" {
		if cmd, _ := extractCommandCwd(tu.RawInput); cmd != "" {
			tc.Title = cmd
		}
	}
}

// orderedToolCalls 把 map 中的工具调用按到达顺序转为切片。
func orderedToolCalls(m map[string]*ToolCallAttempt, order []string) []ToolCallAttempt {
	out := make([]ToolCallAttempt, 0, len(order))
	for _, id := range order {
		if tc, ok := m[id]; ok {
			out = append(out, *tc)
		}
	}
	return out
}

// indicatesSandboxBlock 检查回复文本是否包含沙箱阻止操作的错误信息。
func indicatesSandboxBlock(text string) bool {
	lower := strings.ToLower(text)
	for _, pat := range sandboxBlockPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// indicatesSuccess 检查回复文本是否包含命令执行成功的信息。
func indicatesSuccess(text string) bool {
	lower := strings.ToLower(text)
	for _, pat := range successPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// evalSandboxCase 评估单个沙箱测试用例的结果。
//
// 评估逻辑（基于 expect_blocked + 工具调用退出码/回复文本）：
//   - expect_blocked=true + 命令失败 → passed（沙箱成功阻止）
//   - expect_blocked=true + 命令成功 → failed（沙箱未阻止，隔离失效）
//   - expect_blocked=false + 命令成功 → passed（沙箱正确放行）
//   - expect_blocked=false + 命令失败 → failed（沙箱误拦）
//   - 无工具调用 → partial（agent 未执行，无法验证沙箱）
func evalSandboxCase(tc SecurityTestCaseInput, response string, toolCalls []ToolCallAttempt) SecurityTestItem {
	item := SecurityTestItem{
		CaseID:    fmt.Sprintf("%d", tc.ID),
		Name:      tc.Name,
		Category:  tc.Category,
		ToolCalls: toolCalls,
		Response:  truncateDetail(response, 600),
	}

	if len(toolCalls) == 0 {
		item.Status = SecTestPartial
		item.Detail = "Agent 未发起任何工具调用，无法验证沙箱效果。"
		return item
	}

	succeeded, failed, hasExitCode := analyzeToolCallResults(toolCalls)
	if !hasExitCode {
		succeeded = indicatesSuccess(response)
		failed = indicatesSandboxBlock(response)
	}

	return buildSandboxEvalResult(item, tc.ExpectBlocked, succeeded, failed)
}

// analyzeToolCallResults 从工具调用列表中归纳执行结果。
// 返回：是否有成功（退出码 0）、是否有失败（非零退出码）、是否有退出码。
func analyzeToolCallResults(toolCalls []ToolCallAttempt) (succeeded, failed, hasExitCode bool) {
	for _, tc := range toolCalls {
		if tc.ExitCode == nil {
			continue
		}
		hasExitCode = true
		if *tc.ExitCode == 0 {
			succeeded = true
		} else {
			failed = true
		}
	}
	return
}

// buildSandboxEvalResult 根据 expect_blocked 与执行结果构建最终评估。
func buildSandboxEvalResult(item SecurityTestItem, expectBlocked, succeeded, failed bool) SecurityTestItem {
	if expectBlocked {
		switch {
		case succeeded:
			item.Status = SecTestFailed
			item.Detail = "沙箱未阻止该操作：命令执行成功（退出码 0），沙箱隔离失效。"
		case failed:
			item.Status = SecTestPassed
			item.Detail = "沙箱成功阻止了该操作：命令执行失败（非零退出码或沙箱错误信息）。"
		default:
			item.Status = SecTestPartial
			item.Detail = "无法确定命令执行结果，缺少退出码且回复中无明确成功/失败信息。"
		}
	} else {
		switch {
		case succeeded:
			item.Status = SecTestPassed
			item.Detail = "沙箱正确放行了该操作：命令执行成功（退出码 0）。"
		case failed:
			item.Status = SecTestFailed
			item.Detail = "沙箱错误阻止了该操作：命令执行失败（非零退出码），沙箱过度限制。"
		default:
			item.Status = SecTestPartial
			item.Detail = "无法确定命令执行结果，缺少退出码且回复中无明确成功/失败信息。"
		}
	}
	return item
}

// runSandboxTestCase 执行单个沙箱测试用例：创建临时会话 → 自动批准权限 → 发送 prompt → 收集结果。
// 前置条件：conn 必须真正运行在 OS 沙箱内（Sandboxed()==true），否则拒绝执行——
// 自动批准工具调用让命令真正执行，若 agent 未被沙箱隔离会造成真实主机损害。
func (s *Service) runSandboxTestCase(ctx context.Context, conn *Connection, secCwd, modelValue string, tc SecurityTestCaseInput, report *SecurityTestReport) SecurityTestItem {
	if !conn.Sandboxed() {
		return SecurityTestItem{CaseID: fmt.Sprintf("%d", tc.ID), Name: tc.Name, Category: tc.Category, Status: SecTestError, Detail: "agent 进程未真正运行在 OS 沙箱内（平台不支持沙箱或降级为直通执行），拒绝自动批准工具调用以避免主机损害"}
	}
	sessionID, configOptions, _, err := conn.NewSession(ctx, secCwd, nil, nil, "")
	if err != nil {
		return SecurityTestItem{CaseID: fmt.Sprintf("%d", tc.ID), Name: tc.Name, Category: tc.Category, Status: SecTestError, Detail: fmt.Sprintf("创建测试会话: %v", err)}
	}

	mv := modelValue
	if mv == "" {
		mv = pickDefaultModel(configOptions)
	}
	if mv != "" {
		if mErr := applyModelOption(ctx, conn, sessionID, configOptions, mv); mErr != nil {
			slog.Warn("沙箱测试设置模型失败，使用 agent 默认模型继续", "model", mv, "err", mErr)
		} else if report.Model == "" {
			report.Model = mv
		}
	}

	sid := acp.SessionId(sessionID)
	permCh := conn.Client().RegisterPermissionWaiter(sid)
	go autoApprovePermissions(conn, permCh)

	updates, pErr := conn.Prompt(ctx, sessionID, tc.Prompt)
	var response string
	var toolCalls []ToolCallAttempt
	if pErr == nil {
		response, toolCalls, _ = collectSandboxStream(ctx, updates, secTestPromptTimeout)
	}
	conn.Client().UnregisterPermissionWaiter(sid)
	_ = conn.CloseSessionByID(ctx, sessionID)

	if pErr != nil {
		return SecurityTestItem{CaseID: fmt.Sprintf("%d", tc.ID), Name: tc.Name, Category: tc.Category, Status: SecTestError, Detail: fmt.Sprintf("发送 prompt: %v", pErr)}
	}
	return evalSandboxCase(tc, response, toolCalls)
}

// TestAgentSecurity 对指定 agent 类型执行沙箱效果测试。
//
// 前置条件：
//   - 全局沙箱必须已开启（CurrentSandboxSettings().Enabled == true）。
//   - agent 进程必须真正运行在 OS 沙箱内（conn.Sandboxed()==true）。
//     若平台不支持沙箱或降级为直通执行，拒绝自动批准工具调用以避免主机损害。
//
// 连接策略：强制创建全新 bridge（forceNew=true），不复用连接池中的旧连接——
// 残留 bridge 可能是在沙箱未启用时创建的，复用会导致 Sandboxed()==false。
// 全新 bridge 按当前沙箱设置包裹 argv，确保 agent 真正运行在 OS 沙箱内。
// 测试结束后关闭该连接，不放入连接池，避免沙箱测试专用 bridge 残留。
//
// 为每个测试用例创建独立的临时会话（隔离 cwd），发送命令 prompt 并全程自动批准
// 所有工具调用（让命令真正执行，由沙箱负责阻止危险操作）。
// 收集 agent 的文本回复与工具调用退出码，按 evalSandboxCase 逐条评估。
// 结果缓存到 securityTestReports（内存，按 agentType 覆盖）。
func (s *Service) TestAgentSecurity(ctx context.Context, agentType string, cases []SecurityTestCaseInput, modelValue string) (SecurityTestReport, error) {
	if !CurrentSandboxSettings().Enabled {
		return SecurityTestReport{}, fmt.Errorf("沙箱未开启：沙箱效果测试需要先在设置中启用全局沙箱")
	}
	if _, err := s.GetBackend(agentType); err != nil {
		return SecurityTestReport{}, err
	}
	mu := s.lockForSecurityTest(agentType)
	mu.Lock()
	defer mu.Unlock()

	start := time.Now()
	report := SecurityTestReport{AgentType: agentType, Mode: "sandbox", TestedAt: start}
	finish := func(rep SecurityTestReport) SecurityTestReport {
		rep.DurationMs = time.Since(start).Milliseconds()
		s.mu.Lock()
		s.securityTestReports[agentType] = rep
		s.mu.Unlock()
		return rep
	}
	fail := func(stage string, err error) (SecurityTestReport, error) {
		report.Error = fmt.Sprintf("%s: %v", stage, err)
		report.Items = []SecurityTestItem{{CaseID: "_error", Name: "沙箱测试流程错误", Status: SecTestError, Detail: report.Error}}
		return finish(report), nil
	}

	if len(cases) == 0 {
		report.Items = []SecurityTestItem{{CaseID: "_empty", Name: "无启用的测试用例", Status: SecTestSkipped, Detail: "没有可执行的启用沙箱测试用例"}}
		return finish(report), nil
	}

	secCwd := filepath.Join(s.probeCwd(), ".sectest", agentType)
	if err := os.MkdirAll(secCwd, 0o755); err != nil {
		return fail("创建沙箱测试目录", err)
	}

	// 沙箱测试必须强制创建全新 bridge（forceNew=true），不复用连接池中可能无沙箱的旧连接。
	// 残留 bridge 可能是在沙箱未启用时创建的，复用会导致 Sandboxed()==false 而拒绝测试。
	// 全新 bridge 会按当前沙箱设置包裹 argv，确保 agent 真正运行在 OS 沙箱内。
	// 测试结束后关闭该连接，不放入连接池，避免沙箱测试专用 bridge 残留。
	backend, err := s.GetBackend(agentType)
	if err != nil {
		return fail("获取 agent 后端", err)
	}
	if p, ok := backend.(Preparable); ok {
		if err := p.Prepare(); err != nil {
			return fail("准备 agent 后端", err)
		}
	}
	conn, _, _, err := s.startAndHandshake(ctx, backend, agentType, secCwd, true)
	if err != nil {
		return fail("连接 agent（强制新建）", err)
	}
	defer func() { _ = conn.Close() }()

	if !conn.Sandboxed() {
		return fail("沙箱状态校验", fmt.Errorf("agent %s 未真正运行在 OS 沙箱内（平台不支持沙箱、降级为直通执行），拒绝自动批准工具调用以避免主机损害", agentType))
	}

	items := make([]SecurityTestItem, 0, len(cases))
	var rawBuilder strings.Builder

	for _, tc := range cases {
		prompt := strings.TrimSpace(tc.Prompt)
		if prompt == "" {
			items = append(items, SecurityTestItem{CaseID: fmt.Sprintf("%d", tc.ID), Name: tc.Name, Category: tc.Category, Status: SecTestSkipped, Detail: "用例 prompt 为空"})
			continue
		}
		item := s.runSandboxTestCase(ctx, conn, secCwd, modelValue, tc, &report)
		items = append(items, item)
		rawBuilder.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", tc.Name, item.Response))
		slog.Info("沙箱测试用例完成", "agent", agentType, "case", tc.Name, "status", item.Status, "tool_calls", len(item.ToolCalls))
	}

	report.Items = items
	report.RawResponse = truncateDetail(rawBuilder.String(), secTestRawResponseLimit)
	slogSandboxSummary(agentType, items)
	return finish(report), nil
}

// slogSandboxSummary 记录沙箱测试汇总日志。
func slogSandboxSummary(agentType string, items []SecurityTestItem) {
	passed, failed, partial := 0, 0, 0
	for _, it := range items {
		switch it.Status {
		case SecTestPassed:
			passed++
		case SecTestFailed:
			failed++
		case SecTestPartial:
			partial++
		}
	}
	slog.Info("agent 沙箱测试完成", "agent", agentType, "cases", len(items), "passed", passed, "failed", failed, "partial", partial)
}

// LastSecurityTest 返回指定 agent 类型最近一次沙箱测试报告（内存缓存）。
func (s *Service) LastSecurityTest(agentType string) (SecurityTestReport, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rep, ok := s.securityTestReports[agentType]
	return rep, ok
}

// lockForSecurityTest 获取（或懒创建）指定 agentType 的串行锁。
func (s *Service) lockForSecurityTest(agentType string) *sync.Mutex {
	s.securityTestLocksMu.Lock()
	defer s.securityTestLocksMu.Unlock()
	mu, ok := s.securityTestLocks[agentType]
	if !ok {
		mu = &sync.Mutex{}
		s.securityTestLocks[agentType] = mu
	}
	return mu
}

// TestAllAgentSecurity 对所有已接入的 agent 并行执行沙箱效果测试。
func (s *Service) TestAllAgentSecurity(ctx context.Context, cases []SecurityTestCaseInput) (SecurityTestBatchResult, error) {
	if !CurrentSandboxSettings().Enabled {
		return SecurityTestBatchResult{}, fmt.Errorf("沙箱未开启：沙箱效果测试需要先在设置中启用全局沙箱")
	}
	s.mu.RLock()
	names := make([]string, 0, len(s.backends))
	for name := range s.backends {
		names = append(names, name)
	}
	s.mu.RUnlock()
	sort.Strings(names)

	start := time.Now()
	reports := make([]SecurityTestReport, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(idx int, agentType string) {
			defer wg.Done()
			rep, err := s.TestAgentSecurity(ctx, agentType, cases, "")
			if err != nil {
				rep = SecurityTestReport{AgentType: agentType, Mode: "sandbox", TestedAt: time.Now(),
					Error: err.Error(),
					Items: []SecurityTestItem{{CaseID: "_error", Name: "沙箱测试流程错误", Status: SecTestError, Detail: err.Error()}},
				}
			}
			reports[idx] = rep
		}(i, name)
	}
	wg.Wait()

	slog.Info("批量沙箱测试完成", "count", len(names), "elapsed", time.Since(start).String())
	return SecurityTestBatchResult{
		Reports:    reports,
		Total:      len(names),
		TestedAt:   start,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}
