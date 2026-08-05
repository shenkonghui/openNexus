package acp

import (
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
)

func TestParseCapTestLine(t *testing.T) {
	resp := "RULE: CAPTEST-RULE-ABCD\n**SKILL:** CAPTEST-SKILL-1234\nMCP: opennexus-gateway, foo"
	cases := []struct {
		prefix string
		want   string
	}{
		{"RULE", "CAPTEST-RULE-ABCD"},
		{"SKILL", "CAPTEST-SKILL-1234"}, // 容忍 markdown 加粗修饰
		{"MCP", "opennexus-gateway, foo"},
	}
	for _, c := range cases {
		got, ok := parseCapTestLine(resp, c.prefix)
		if !ok || got != c.want {
			t.Errorf("parseCapTestLine(%q) = %q, %v; want %q", c.prefix, got, ok, c.want)
		}
	}
	if _, ok := parseCapTestLine(resp, "NOPE"); ok {
		t.Error("parseCapTestLine 对不存在前缀应返回 false")
	}

	// 工具输出与格式行粘连（无换行）：前缀允许出现在行中间。
	glued := "CAPTEST-SUBAGENT-D109C7EBMCP: mcp__opennexus-gateway__opennexus-notes_get_note\nAGENT: CAPTEST-SUBAGENT-D109C7EB"
	if got, ok := parseCapTestLine(glued, "MCP"); !ok || got != "mcp__opennexus-gateway__opennexus-notes_get_note" {
		t.Errorf("粘连行 MCP 解析失败: %q, %v", got, ok)
	}
	// AGENT 不应误命中 SUBAGENT 的中段。
	if got, ok := parseCapTestLine("SUBAGENT: X", "AGENT"); ok {
		t.Errorf("AGENT 误匹配 SUBAGENT 中段: %q", got)
	}
	if got, ok := parseCapTestLine(glued, "AGENT"); !ok || got != "CAPTEST-SUBAGENT-D109C7EB" {
		t.Errorf("AGENT 行解析失败: %q, %v", got, ok)
	}
}

func TestEvalMarkerItem(t *testing.T) {
	marker := "CAPTEST-RULE-DEAD"
	if item := evalMarkerItem("rule", "RULE: CAPTEST-RULE-DEAD", "RULE", marker); item.Status != CapTestPassed {
		t.Errorf("含标记应 passed，得到 %s", item.Status)
	}
	if item := evalMarkerItem("rule", "RULE: NONE", "RULE", marker); item.Status != CapTestFailed {
		t.Errorf("无标记应 failed，得到 %s", item.Status)
	}
	if item := evalMarkerItem("rule", "无关内容", "RULE", marker); item.Status != CapTestFailed {
		t.Errorf("缺行应 failed，得到 %s", item.Status)
	}
}

func TestEvalMCPItem(t *testing.T) {
	servers := []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "opennexus-gateway"}}}
	accept := append(mcpServerNames(servers), "opennexus-notes")
	noProbe := BridgeProbeResult{}

	if item := evalMCPItem("MCP: opennexus-gateway_foo", servers, accept, "", noProbe); item.Status != CapTestPassed {
		t.Errorf("匹配 server 名应 passed，得到 %s", item.Status)
	}
	if item := evalMCPItem("MCP: opennexus-notes_get_note", servers, accept, "", noProbe); item.Status != CapTestPassed {
		t.Errorf("匹配网关上游名应 passed，得到 %s", item.Status)
	}
	if item := evalMCPItem("MCP: something-else", servers, accept, "", noProbe); item.Status != CapTestPartial {
		t.Errorf("有工具但不匹配应 partial，得到 %s", item.Status)
	}
	if item := evalMCPItem("MCP: NONE", servers, accept, "", noProbe); item.Status != CapTestFailed {
		t.Errorf("NONE 无桥探测应 failed，得到 %s", item.Status)
	}
	if item := evalMCPItem("MCP: whatever", nil, nil, "", noProbe); item.Status != CapTestSkipped {
		t.Errorf("无注入 server 应 skipped，得到 %s", item.Status)
	}

	// 桥探测就绪但 agent 报 NONE：降级为 partial（agent 异步加载未完成，非链路故障）
	probeReady := BridgeProbeResult{Available: true, ToolCount: 11, Detail: "桥就绪：11 个工具"}
	if item := evalMCPItem("MCP: NONE", servers, accept, "", probeReady); item.Status != CapTestPartial {
		t.Errorf("桥就绪但 agent 报 NONE 应 partial，得到 %s（detail: %s）", item.Status, item.Detail)
	}
}

func TestCapTestRetryMerge(t *testing.T) {
	first := "RULE: NONE\nSKILL: CAPTEST-SKILL-AAAA\nMCP: NONE\nAGENT: NONE"
	retry := "MCP: opennexus-notes_get_note\nAGENT: CAPTEST-SUBAGENT-BBBB"

	if !needCapTestRetry(first) {
		t.Error("MCP/AGENT 为 NONE 时应触发重试")
	}
	if needCapTestRetry("MCP: tool_a\nAGENT: CAPTEST-SUBAGENT-X") {
		t.Error("两行均有值时不应触发重试")
	}
	merged := mergeCapTestResponses(first, retry)
	for _, want := range []string{"SKILL: CAPTEST-SKILL-AAAA", "MCP: opennexus-notes_get_note", "AGENT: CAPTEST-SUBAGENT-BBBB", "RULE: NONE"} {
		if !strings.Contains(merged, want) {
			t.Errorf("合并结果缺少 %q，得到:\n%s", want, merged)
		}
	}
}

func TestMcpServerNames(t *testing.T) {
	servers := []acp.McpServer{
		{Stdio: &acp.McpServerStdio{Name: "a"}},
		{Http: &acp.McpServerHttpInline{Name: "b"}},
		{Sse: &acp.McpServerSseInline{Name: "c"}},
	}
	names := mcpServerNames(servers)
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Errorf("mcpServerNames = %v", names)
	}
}

func TestCapTestMarkerUnique(t *testing.T) {
	m1 := capTestMarker("rule")
	m2 := capTestMarker("rule")
	if m1 == m2 {
		t.Errorf("标记应随机唯一，两次相同: %s", m1)
	}
}
