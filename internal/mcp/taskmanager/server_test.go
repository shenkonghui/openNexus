package taskmanagermcp

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"opennexus/internal/acp"
	"opennexus/internal/models"
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
	t.Run("sendPromptIn", func(t *testing.T) { _, err := jsonschema.For[sendPromptIn](nil); check(t, err) })
	t.Run("setMaxParallelIn", func(t *testing.T) { _, err := jsonschema.For[setMaxParallelIn](nil); check(t, err) })
	t.Run("listTasksIn", func(t *testing.T) { _, err := jsonschema.For[listTasksIn](nil); check(t, err) })
}

// TestResolveGoalCondition 验证 create_task 的 goal 入参解析：默认开启（留空回退任务详情）、
// off 等别名显式关闭、超长截断且不破坏 UTF-8 rune 边界。
func TestResolveGoalCondition(t *testing.T) {
	if got := resolveGoalCondition("验收标准", "detail"); got != "验收标准" {
		t.Errorf("显式 goal = %q, want 验收标准", got)
	}
	if got := resolveGoalCondition("", "detail"); got != "detail" {
		t.Errorf("留空应回退任务详情, got %q", got)
	}
	for _, alias := range []string{"off", "OFF", "none", "false", "disable"} {
		if got := resolveGoalCondition(alias, "detail"); got != "" {
			t.Errorf("%q 应关闭 goal, got %q", alias, got)
		}
	}
	// 超长截断：中文 3 字节/字，截断后仍须是合法 UTF-8 且不超上限。
	long := strings.Repeat("目", acp.GoalMaxConditionLen)
	got := resolveGoalCondition(long, "detail")
	if len(got) > acp.GoalMaxConditionLen {
		t.Errorf("截断后长度 %d 超上限 %d", len(got), acp.GoalMaxConditionLen)
	}
	if !utf8.ValidString(got) {
		t.Error("截断后不是合法 UTF-8")
	}
}

// fakeTaskCreator 仅实现 Load，供 expandTaskIDs 测试使用。
type fakeTaskCreator struct {
	def models.TaskManagerDef
}

func (f *fakeTaskCreator) UpsertTask(string, models.TaskManagerTask) error { return nil }
func (f *fakeTaskCreator) DeleteTask(string, string) error                 { return nil }
func (f *fakeTaskCreator) SetMaxParallel(string, int) error                { return nil }
func (f *fakeTaskCreator) Stop(string, string) error                       { return nil }
func (f *fakeTaskCreator) Start(context.Context, string, uint, uint, string) error {
	return nil
}
func (f *fakeTaskCreator) Load(string) (*models.TaskManagerDef, error) {
	d := f.def
	return &d, nil
}
func (f *fakeTaskCreator) SendPrompt(context.Context, string, string, string) error { return nil }

// TestExpandTaskIDs 验证 task_id 的 glob 展开行为：默认开启、可显式关闭、
// 无元字符时按字面返回、无命中时报错。
func TestExpandTaskIDs(t *testing.T) {
	creator := &fakeTaskCreator{def: models.TaskManagerDef{Tasks: []models.TaskManagerTask{
		{ID: "t100"}, {ID: "t101"}, {ID: "x200"},
	}}}
	boolPtr := func(b bool) *bool { return &b }

	t.Run("默认开启glob批量匹配", func(t *testing.T) {
		ids, err := expandTaskIDs(creator, "/cwd", "t1*", nil)
		if err != nil {
			t.Fatalf("expandTaskIDs: %v", err)
		}
		if len(ids) != 2 || ids[0] != "t100" || ids[1] != "t101" {
			t.Fatalf("期望 [t100 t101]，实际 %v", ids)
		}
	})

	t.Run("显式开启与默认一致", func(t *testing.T) {
		ids, err := expandTaskIDs(creator, "/cwd", "?200", boolPtr(true))
		if err != nil {
			t.Fatalf("expandTaskIDs: %v", err)
		}
		if len(ids) != 1 || ids[0] != "x200" {
			t.Fatalf("期望 [x200]，实际 %v", ids)
		}
	})

	t.Run("关闭glob时按字面返回", func(t *testing.T) {
		ids, err := expandTaskIDs(creator, "/cwd", "t1*", boolPtr(false))
		if err != nil {
			t.Fatalf("expandTaskIDs: %v", err)
		}
		if len(ids) != 1 || ids[0] != "t1*" {
			t.Fatalf("期望字面 [t1*]，实际 %v", ids)
		}
	})

	t.Run("无元字符时不加载任务直接返回", func(t *testing.T) {
		ids, err := expandTaskIDs(creator, "/cwd", "t100", nil)
		if err != nil {
			t.Fatalf("expandTaskIDs: %v", err)
		}
		if len(ids) != 1 || ids[0] != "t100" {
			t.Fatalf("期望 [t100]，实际 %v", ids)
		}
	})

	t.Run("无命中时报错", func(t *testing.T) {
		if _, err := expandTaskIDs(creator, "/cwd", "zzz*", nil); err == nil {
			t.Fatal("期望无命中报错，实际成功")
		}
	})

	t.Run("非法模式报错", func(t *testing.T) {
		if _, err := expandTaskIDs(creator, "/cwd", "[", nil); err == nil {
			t.Fatal("期望非法 glob 模式报错，实际成功")
		}
	})
}
