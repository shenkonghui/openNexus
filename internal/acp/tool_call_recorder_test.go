package acp

import (
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
)

func TestHasTerminalContent(t *testing.T) {
	if hasTerminalContent(nil) {
		t.Error("nil update 不应含 terminal content")
	}
	tu := &acp.SessionToolCallUpdate{}
	if hasTerminalContent(tu) {
		t.Error("空 content 不应含 terminal content")
	}
	tu.Content = []acp.ToolCallContent{
		{Terminal: &acp.ToolCallContentTerminal{TerminalId: "term-1", Type: "terminal"}},
	}
	if !hasTerminalContent(tu) {
		t.Error("内嵌 terminal content 应返回 true")
	}
	tu.Content = []acp.ToolCallContent{
		{Terminal: &acp.ToolCallContentTerminal{TerminalId: "", Type: "terminal"}},
	}
	if hasTerminalContent(tu) {
		t.Error("terminalId 为空不应返回 true")
	}
}

func TestKeepTerminalAnchorRaw(t *testing.T) {
	anchor := `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","content":[{"type":"terminal","terminalId":"term-1"}]}`
	final := `{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"completed"}`

	// 旧 raw 含锚点、新 raw 无锚点：锚点行保留在前
	got := keepTerminalAnchorRaw(anchor, final)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || lines[0] != anchor || lines[1] != final {
		t.Errorf("锚点行应保留在新 raw 之前，实际 %q", got)
	}

	// 旧 raw 无锚点：直接用新 raw
	if got := keepTerminalAnchorRaw(final, final); got != final {
		t.Errorf("旧 raw 无锚点时应返回新 raw，实际 %q", got)
	}

	// 新 raw 本身含锚点：不重复叠加
	if got := keepTerminalAnchorRaw(anchor, anchor); got != anchor {
		t.Errorf("新 raw 已含锚点时应原样返回，实际 %q", got)
	}

	// 旧 raw 为空：返回新 raw
	if got := keepTerminalAnchorRaw("", final); got != final {
		t.Errorf("旧 raw 为空时应返回新 raw，实际 %q", got)
	}

	// 多次覆盖链路：锚点跨多轮覆盖仍保留且不重复
	step1 := keepTerminalAnchorRaw(anchor, `{"toolCallId":"tc1","status":"in_progress"}`)
	step2 := keepTerminalAnchorRaw(step1, final)
	if strings.Count(step2, "term-1") != 1 {
		t.Errorf("多轮覆盖后锚点应恰好保留一份，实际 %q", step2)
	}
	if !strings.Contains(step2, `"status":"completed"`) {
		t.Errorf("多轮覆盖后应保留最新终态，实际 %q", step2)
	}
}
