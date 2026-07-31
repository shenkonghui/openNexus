package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"
)

// 能力测试单项状态。
const (
	CapTestPassed   = "passed"   // 端到端验证通过：agent 确认识别了注入内容
	CapTestFailed   = "failed"   // 端到端验证失败：agent 未识别注入内容
	CapTestPartial  = "partial"  // 部分通过：有响应但无法完全确认
	CapTestInjected = "injected" // 静态检查：注入被 session/new 接受，但未做行为验证
	CapTestSkipped  = "skipped"  // 无可测内容（如未配置任何 MCP server）
	CapTestError    = "error"    // 测试流程本身出错
)

// CapabilityTestItem 是单项能力（rule / skill / mcp）的测试结果。
type CapabilityTestItem struct {
	ID     string `json:"id"`     // rule | skill | mcp
	Status string `json:"status"` // 见 CapTest* 常量
	Detail string `json:"detail,omitempty"`
}

// CapabilityTestReport 是一次 agent 能力接入测试的完整报告。
// 结果按 agentType 内存缓存（重启失效），供设置页展示最近一次测试。
type CapabilityTestReport struct {
	AgentType   string               `json:"agent_type"`
	Mode        string               `json:"mode"` // static | e2e
	Items       []CapabilityTestItem `json:"items"`
	RawResponse string               `json:"raw_response,omitempty"` // e2e 时 agent 的原始回复（截断）
	TestedAt    time.Time            `json:"tested_at"`
	DurationMs  int64                `json:"duration_ms"`
	Error       string               `json:"error,omitempty"` // 整体流程错误（如连接失败）
}

// capTestPromptTimeout 是端到端验证 prompt 的收集超时。
const capTestPromptTimeout = 120 * time.Second

// capTestRawResponseLimit 是报告中保留的 agent 原始回复上限（字符）。
const capTestRawResponseLimit = 4000

// capTestMarker 生成一次性随机标记，防止 agent 从历史或猜测中"碰对"。
func capTestMarker(kind string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("CAPTEST-%s-%s", strings.ToUpper(kind), strings.ToUpper(hex.EncodeToString(buf)))
}

// writeCapTestSkill 在临时目录下生成测试 skill（agentskills.io 规范），
// 返回 (skill 根目录, SKILL.md 绝对路径, 清理函数)。
func writeCapTestSkill(marker string) (string, string, func(), error) {
	root, err := os.MkdirTemp("", "opennexus-captest-")
	if err != nil {
		return "", "", nil, fmt.Errorf("创建测试 skill 临时目录: %w", err)
	}
	skillDir := filepath.Join(root, "opennexus-captest")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return "", "", nil, fmt.Errorf("创建测试 skill 目录: %w", err)
	}
	content := fmt.Sprintf(`---
name: opennexus-captest
description: openNexus 能力接入自检专用 skill，用于验证 agent 能否访问附加目录中的 skill 文件。
---

# openNexus 能力自检 Skill

本文件由能力测试自动生成。验证标记：

%s
`, marker)
	skillMD := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte(content), 0o644); err != nil {
		_ = os.RemoveAll(root)
		return "", "", nil, fmt.Errorf("写入测试 SKILL.md: %w", err)
	}
	return root, skillMD, func() { _ = os.RemoveAll(root) }, nil
}

// buildCapTestPrompt 构造端到端验证 prompt：要求 agent 按固定格式逐项报告。
func buildCapTestPrompt(ruleMarker, skillMDPath string) string {
	return fmt.Sprintf(`这是一次自动化能力自检，请严格按以下格式输出三行结果，不要输出其他内容，绝对不要编造标记：
RULE: <如果你的系统规则/系统提示中包含形如 CAPTEST-RULE-XXXX 的标记，原样输出该标记；否则输出 NONE>
SKILL: <读取文件 %s，原样输出其中形如 CAPTEST-SKILL-XXXX 的标记；无法读取则输出 NONE>
MCP: <列出你当前可调用的 MCP 工具名称（逗号分隔，最多 20 个）；一个都没有则输出 NONE>
规则说明：%s 行的标记只能来自你真实看到的内容；读取文件时直接读取，不要询问确认。`, skillMDPath, "RULE/SKILL")
}

// mcpServerNames 提取注入列表中的 server 名称（任意传输类型）。
func mcpServerNames(servers []acp.McpServer) []string {
	names := make([]string, 0, len(servers))
	for _, sv := range servers {
		switch {
		case sv.Stdio != nil:
			names = append(names, sv.Stdio.Name)
		case sv.Http != nil:
			names = append(names, sv.Http.Name)
		case sv.Sse != nil:
			names = append(names, sv.Sse.Name)
		case sv.Acp != nil:
			names = append(names, sv.Acp.Name)
		}
	}
	return names
}

// parseCapTestLine 从 agent 回复中提取 "PREFIX:" 行的内容（大小写不敏感，容忍 markdown 修饰）。
func parseCapTestLine(response, prefix string) (string, bool) {
	for _, line := range strings.Split(response, "\n") {
		trimmed := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "*`#>-"))
		if len(trimmed) < len(prefix)+1 {
			continue
		}
		if strings.EqualFold(trimmed[:len(prefix)], prefix) && trimmed[len(prefix)] == ':' {
			// 值可能残留 markdown 修饰（如 **SKILL:** value）：再去一次首尾修饰字符。
			return strings.Trim(strings.TrimSpace(trimmed[len(prefix)+1:]), "*`# "), true
		}
	}
	return "", false
}

// autoApprovePermissions 在能力测试期间自动放行权限请求（优先 allow-once）：
// 测试 prompt 需要读取临时 skill 文件，若沿用自动拒绝会造成 skill 项假阴性。
func autoApprovePermissions(conn *Connection, permCh <-chan PermissionNotify) {
	for pn := range permCh {
		resp := autoApprovePermission(pn.Request)
		optID := ""
		if resp.Outcome.Selected != nil {
			optID = string(resp.Outcome.Selected.OptionId)
		}
		_ = conn.Client().RespondPermission(pn.RequestID, optID, optID == "")
	}
}

// TestAgentCapabilities 对指定 agent 类型执行能力接入测试。
//
// 两级验证：
//   - 静态（e2e=false）：握手能力 + 携带 rule/skill/mcp 注入创建临时会话，
//     确认 session/new 接受注入（rule/skill 标记为 injected，未做行为验证）。
//   - 端到端（e2e=true）：在临时会话中注入带随机标记的测试 rule（Meta.systemPrompt）、
//     测试 skill（AdditionalDirectories 临时目录）与全局 MCP server，
//     发送一条验证 prompt 让 agent 复述标记 / 列举 MCP 工具，按回复判定支持度。
//
// 结果缓存到 capTestReports（内存，按 agentType 覆盖）。
func (s *Service) TestAgentCapabilities(ctx context.Context, agentType string, userID uint, e2e bool) (CapabilityTestReport, error) {
	if _, err := s.GetBackend(agentType); err != nil {
		return CapabilityTestReport{}, err
	}
	// 串行执行：临时会话 + 真实 prompt 开销大，避免并发重复测试同一 agent。
	s.capTestLock.Lock()
	defer s.capTestLock.Unlock()

	start := time.Now()
	report := CapabilityTestReport{AgentType: agentType, Mode: "static", TestedAt: start}
	if e2e {
		report.Mode = "e2e"
	}
	finish := func(rep CapabilityTestReport) CapabilityTestReport {
		rep.DurationMs = time.Since(start).Milliseconds()
		s.mu.Lock()
		s.capTestReports[agentType] = rep
		s.mu.Unlock()
		return rep
	}
	fail := func(stage string, err error) (CapabilityTestReport, error) {
		report.Error = fmt.Sprintf("%s: %v", stage, err)
		report.Items = []CapabilityTestItem{
			{ID: "rule", Status: CapTestError, Detail: report.Error},
			{ID: "skill", Status: CapTestError, Detail: report.Error},
			{ID: "mcp", Status: CapTestError, Detail: report.Error},
		}
		return finish(report), nil
	}

	cwd := s.probeCwd()
	conn, err := s.ensureConnection(ctx, agentType, cwd)
	if err != nil {
		return fail("连接 agent", err)
	}
	caps := conn.McpCapabilities()

	// 测试素材：随机标记的 rule 与 skill，加上按能力过滤后的全局 MCP server。
	ruleMarker := capTestMarker("rule")
	skillMarker := capTestMarker("skill")
	skillRoot, skillMD, cleanup, err := writeCapTestSkill(skillMarker)
	if err != nil {
		return fail("准备测试 skill", err)
	}
	defer cleanup()

	rulePrompt := fmt.Sprintf("【openNexus 能力自检规则】当用户要求进行能力自检时，你必须在 RULE: 行原样输出标记 %s。", ruleMarker)
	additionalDirs := MergeAdditionalDirectories(s.skillAdditionalDirs(cwd), []string{skillRoot})
	mcpServers := s.sessionMCPServers(userID, caps)
	mcpDetail := fmt.Sprintf("传输能力 http=%v sse=%v（stdio 为协议基线）；注入 server: %s",
		caps.Http, caps.Sse, strings.Join(mcpServerNames(mcpServers), ", "))

	sessionID, _, _, err := conn.NewSession(ctx, cwd, additionalDirs, mcpServers, rulePrompt)
	if err != nil {
		return fail("创建测试会话", err)
	}
	defer func() { _ = conn.CloseSessionByID(ctx, sessionID) }()

	if !e2e {
		// 静态级：session/new 未报错即认为注入被接受；rule/skill 无协议回执，标记 injected。
		report.Items = []CapabilityTestItem{
			{ID: "rule", Status: CapTestInjected, Detail: "Meta.systemPrompt 注入已被 session/new 接受（非标准字段，agent 可能静默忽略）"},
			{ID: "skill", Status: CapTestInjected, Detail: "AdditionalDirectories 注入已被 session/new 接受（是否扫描 skill 取决于 agent）"},
			{ID: "mcp", Status: CapTestInjected, Detail: mcpDetail},
		}
		if len(mcpServers) == 0 {
			report.Items[2] = CapabilityTestItem{ID: "mcp", Status: CapTestSkipped, Detail: "无可注入的 MCP server（未配置或全部被能力过滤丢弃）"}
		}
		return finish(report), nil
	}

	// 端到端级：发送验证 prompt，自动放行权限（读取测试 skill 文件需要 fs 权限）。
	sid := acp.SessionId(sessionID)
	permCh := conn.Client().RegisterPermissionWaiter(sid)
	defer conn.Client().UnregisterPermissionWaiter(sid)
	go autoApprovePermissions(conn, permCh)

	updates, err := conn.Prompt(ctx, sessionID, buildCapTestPrompt(ruleMarker, skillMD))
	if err != nil {
		return fail("发送验证 prompt", err)
	}
	response, err := collectPromptText(ctx, updates, capTestPromptTimeout)
	if err != nil {
		return fail("收集 agent 回复", err)
	}
	if len(response) > capTestRawResponseLimit {
		report.RawResponse = response[:capTestRawResponseLimit] + "…"
	} else {
		report.RawResponse = response
	}

	report.Items = []CapabilityTestItem{
		evalMarkerItem("rule", response, "RULE", ruleMarker),
		evalMarkerItem("skill", response, "SKILL", skillMarker),
		evalMCPItem(response, mcpServers, mcpDetail),
	}
	slog.Info("agent 能力测试完成", "agent", agentType, "mode", report.Mode,
		"rule", report.Items[0].Status, "skill", report.Items[1].Status, "mcp", report.Items[2].Status)
	return finish(report), nil
}

// collectPromptText 收集 prompt 更新流中的 assistant 文本，直到流结束或超时。
func collectPromptText(ctx context.Context, updates <-chan acp.SessionUpdate, timeout time.Duration) (string, error) {
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

// evalMarkerItem 判定 rule / skill 单项：回复中出现随机标记即通过。
func evalMarkerItem(id, response, linePrefix, marker string) CapabilityTestItem {
	if strings.Contains(response, marker) {
		return CapabilityTestItem{ID: id, Status: CapTestPassed, Detail: fmt.Sprintf("agent 复述了标记 %s", marker)}
	}
	if val, ok := parseCapTestLine(response, linePrefix); ok {
		return CapabilityTestItem{ID: id, Status: CapTestFailed, Detail: fmt.Sprintf("%s 行返回 %q，未包含标记 %s", linePrefix, val, marker)}
	}
	return CapabilityTestItem{ID: id, Status: CapTestFailed, Detail: fmt.Sprintf("回复中未找到 %s 行与标记", linePrefix)}
}

// evalMCPItem 判定 MCP 单项：
//   - 未注入任何 server → skipped
//   - MCP 行列出的工具名包含任一注入 server 名 → passed
//   - 有非 NONE 的工具列表但匹配不到 server 名 → partial（agent 可能改写了工具名前缀）
//   - NONE 或缺失 → failed
func evalMCPItem(response string, servers []acp.McpServer, baseDetail string) CapabilityTestItem {
	if len(servers) == 0 {
		return CapabilityTestItem{ID: "mcp", Status: CapTestSkipped, Detail: "无可注入的 MCP server（未配置或全部被能力过滤丢弃）"}
	}
	val, ok := parseCapTestLine(response, "MCP")
	if !ok || strings.EqualFold(strings.TrimSpace(val), "NONE") || strings.TrimSpace(val) == "" {
		return CapabilityTestItem{ID: "mcp", Status: CapTestFailed, Detail: "agent 未报告任何 MCP 工具；" + baseDetail}
	}
	for _, name := range mcpServerNames(servers) {
		if name != "" && strings.Contains(val, name) {
			return CapabilityTestItem{ID: "mcp", Status: CapTestPassed, Detail: fmt.Sprintf("agent 报告的工具包含 server %q；%s", name, baseDetail)}
		}
	}
	return CapabilityTestItem{ID: "mcp", Status: CapTestPartial, Detail: fmt.Sprintf("agent 报告了工具（%s）但未匹配到注入 server 名；%s", truncateDetail(val, 200), baseDetail)}
}

// truncateDetail 截断过长的明细文本。
func truncateDetail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// LastCapabilityTest 返回指定 agent 类型最近一次能力测试报告（内存缓存）。
func (s *Service) LastCapabilityTest(agentType string) (CapabilityTestReport, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rep, ok := s.capTestReports[agentType]
	return rep, ok
}
