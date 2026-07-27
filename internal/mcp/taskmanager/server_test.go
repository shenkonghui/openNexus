package taskmanagermcp

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// TestNewServerDoesNotPanic 验证 newServer 在依赖项缺失（nil）时也能安全构造，
// 不会因为 mcp.AddTool 的潜在 panic 把整个 server 拖垮。
// 这是 opennexus-task 曾缺失工具的根因回归保护：
// 单个工具注册失败（panic）应被 addTool 兜住，server 仍可返回。
func TestNewServerDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("newServer 不应 panic，但发生了: %v", r)
		}
	}()
	// 传入 nil 依赖：各工具 handler 在被调用时才会报错，注册阶段不应 panic。
	srv := newServer(nil, nil, nil)
	if srv == nil {
		t.Fatal("newServer 返回 nil")
	}
}

// TestTaskManagerJSONSchemaTagsValid 校验所有编排工具输入结构都能成功推断 schema。
// jsonschema-go 的 forType 在遇到形如 "WORD=" 的 tag（第一个 '=' 前不含空白）时会返回
// "tag must not begin with 'WORD='" 错误，进而让 mcp.AddTool panic。
// 历史上 set_max_parallel 的 tag "并发上限，1=串行，范围 1~16" 触发该规则，
// 导致 AddTool panic → 整个 MCP server 对外 500 → 工具全部消失。此处做回归保护。
func TestTaskManagerJSONSchemaTagsValid(t *testing.T) {
	check := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("推断 schema 失败（含非法 jsonschema tag）: %v", err)
		}
	}

	t.Run("createTaskIn", func(t *testing.T) { _, err := jsonschema.For[createTaskIn](nil); check(t, err) })
	t.Run("updateTaskIn", func(t *testing.T) { _, err := jsonschema.For[updateTaskIn](nil); check(t, err) })
	t.Run("deleteTaskIn", func(t *testing.T) { _, err := jsonschema.For[deleteTaskIn](nil); check(t, err) })
	t.Run("startTaskIn", func(t *testing.T) { _, err := jsonschema.For[startTaskIn](nil); check(t, err) })
	t.Run("stopTaskIn", func(t *testing.T) { _, err := jsonschema.For[stopTaskIn](nil); check(t, err) })
	t.Run("setMaxParallelIn", func(t *testing.T) { _, err := jsonschema.For[setMaxParallelIn](nil); check(t, err) })
	t.Run("listTasksIn", func(t *testing.T) { _, err := jsonschema.For[listTasksIn](nil); check(t, err) })
}
