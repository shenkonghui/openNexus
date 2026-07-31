package acp

import (
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

	if item := evalMCPItem("MCP: opennexus-gateway_foo", servers, ""); item.Status != CapTestPassed {
		t.Errorf("匹配 server 名应 passed，得到 %s", item.Status)
	}
	if item := evalMCPItem("MCP: something-else", servers, ""); item.Status != CapTestPartial {
		t.Errorf("有工具但不匹配应 partial，得到 %s", item.Status)
	}
	if item := evalMCPItem("MCP: NONE", servers, ""); item.Status != CapTestFailed {
		t.Errorf("NONE 应 failed，得到 %s", item.Status)
	}
	if item := evalMCPItem("MCP: whatever", nil, ""); item.Status != CapTestSkipped {
		t.Errorf("无注入 server 应 skipped，得到 %s", item.Status)
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
