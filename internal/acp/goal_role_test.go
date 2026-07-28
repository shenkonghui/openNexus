package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGoalRoleFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseGoalRoleMarkdown(t *testing.T) {
	// 完整 frontmatter + skills 逗号串
	content := `---
name: qa-reviewer
description: 适用于测试修复类目标
agent: claude-code
model: haiku
skills: run-e2e-test, testcase-generator
---
评估指引正文。
`
	def, ok := parseGoalRoleMarkdown("qa.md", []byte(content))
	if !ok {
		t.Fatal("期望解析成功")
	}
	if def.Name != "qa-reviewer" || def.Agent != "claude-code" || def.Model != "haiku" {
		t.Fatalf("frontmatter 解析错误: %+v", def)
	}
	if len(def.Skills) != 2 || def.Skills[0] != "run-e2e-test" || def.Skills[1] != "testcase-generator" {
		t.Fatalf("skills 逗号串解析错误: %v", def.Skills)
	}
	if def.PromptTemplate != "评估指引正文。" {
		t.Fatalf("正文解析错误: %q", def.PromptTemplate)
	}

	// skills YAML 列表 + name 缺省取文件名
	content = `---
description: 架构评审
skills:
  - drawio-skill
---
正文
`
	def, ok = parseGoalRoleMarkdown("arch-reviewer.md", []byte(content))
	if !ok {
		t.Fatal("期望解析成功")
	}
	if def.Name != "arch-reviewer" {
		t.Fatalf("name 应缺省取文件名，实际 %q", def.Name)
	}
	if len(def.Skills) != 1 || def.Skills[0] != "drawio-skill" {
		t.Fatalf("skills 列表解析错误: %v", def.Skills)
	}

	// description 缺失 → 拒绝
	if _, ok := parseGoalRoleMarkdown("bad.md", []byte("---\nname: x\n---\n正文")); ok {
		t.Fatal("description 缺失应解析失败")
	}
}

func TestScanGoalRolesProjectOverridesUser(t *testing.T) {
	userDir := t.TempDir()
	cwd := t.TempDir()
	projSub := ".agents/goal-roles"

	writeGoalRoleFile(t, userDir, "dup.md", "---\nname: dup\ndescription: 用户级\n---\n用户级正文")
	writeGoalRoleFile(t, filepath.Join(cwd, projSub), "dup.md", "---\nname: dup\ndescription: 项目级\n---\n项目级正文")
	writeGoalRoleFile(t, userDir, "only-user.md", "---\ndescription: 仅用户级\n---\n正文")

	roles := ScanGoalRoles(cwd, []string{userDir}, []string{projSub})
	if len(roles) != 2 {
		t.Fatalf("期望 2 个角色，实际 %d: %+v", len(roles), roles)
	}
	byName := map[string]GoalRoleDef{}
	for _, r := range roles {
		byName[r.Name] = r
	}
	if byName["dup"].Scope != "project" || byName["dup"].Description != "项目级" {
		t.Fatalf("同名角色应 project 优先: %+v", byName["dup"])
	}
	if byName["only-user"].Scope != "user" {
		t.Fatalf("用户级角色缺失: %+v", roles)
	}
}

func TestMatchGoalRole(t *testing.T) {
	roles := []GoalRoleDef{{Name: "qa-reviewer"}, {Name: "arch-reviewer"}}

	cases := []struct {
		out  string
		want string // "" = nil
	}{
		{"qa-reviewer", "qa-reviewer"},
		{"QA-Reviewer\n理由巴拉巴拉", "qa-reviewer"}, // 大小写不敏感 + 只取首行
		{"`arch-reviewer`", "arch-reviewer"},   // 反引号包裹
		{"选择 qa-reviewer 负责评估", "qa-reviewer"}, // 首行夹带说明文字
		{"NONE", ""},
		{"none", ""},
		{"", ""},
		{"unknown-role", ""},
	}
	for _, c := range cases {
		got := matchGoalRole(c.out, roles)
		if c.want == "" {
			if got != nil {
				t.Fatalf("输出 %q 应返回 nil，实际 %v", c.out, got.Name)
			}
			continue
		}
		if got == nil || got.Name != c.want {
			t.Fatalf("输出 %q 期望 %q，实际 %v", c.out, c.want, got)
		}
	}
}

func TestBuildGoalEvalPrompt(t *testing.T) {
	cond, trans := "所有测试通过", "user: 修一下\nassistant: 好"

	// nil role：内置标准 prompt
	p := buildGoalEvalPrompt(nil, cond, trans)
	if !strings.Contains(p, "你是任务完成度评估器") || !strings.Contains(p, cond) || !strings.Contains(p, trans) {
		t.Fatalf("内置 prompt 缺少要素: %q", p)
	}
	if !strings.Contains(p, "只输出两行") {
		t.Fatal("内置 prompt 缺少输出约束")
	}

	// 模板含占位符：替换后不再补标准段落
	role := &GoalRoleDef{Name: "qa", PromptTemplate: "按 QA 标准评估。\n条件：{{condition}}\n对话：{{transcript}}"}
	p = buildGoalEvalPrompt(role, cond, trans)
	if !strings.Contains(p, "条件："+cond) || !strings.Contains(p, "对话："+trans) {
		t.Fatalf("占位符未替换: %q", p)
	}
	if strings.Contains(p, "{{condition}}") || strings.Contains(p, "完成条件：\n") {
		t.Fatalf("不应残留占位符或重复补段落: %q", p)
	}
	if !strings.HasSuffix(p, goalEvalOutputRule) {
		t.Fatal("必须以固定输出约束结尾")
	}

	// 只含 condition 占位符：transcript 段落自动补齐
	role = &GoalRoleDef{Name: "qa", PromptTemplate: "条件：{{condition}}"}
	p = buildGoalEvalPrompt(role, cond, trans)
	if !strings.Contains(p, "最近对话摘录：\n"+trans) {
		t.Fatalf("缺失的 transcript 段落应自动补齐: %q", p)
	}

	// 无占位符：正文作为评估指引 + 标准段落
	role = &GoalRoleDef{Name: "qa", PromptTemplate: "务必严格。", Skills: []string{"run-e2e-test"}}
	p = buildGoalEvalPrompt(role, cond, trans)
	if !strings.Contains(p, "评估指引：\n务必严格。") {
		t.Fatalf("无占位符正文应作为评估指引: %q", p)
	}
	if !strings.Contains(p, "完成条件：\n"+cond) || !strings.Contains(p, "最近对话摘录：\n"+trans) {
		t.Fatalf("应拼接标准段落: %q", p)
	}
	if !strings.Contains(p, "运用以下技能") || !strings.Contains(p, "run-e2e-test") {
		t.Fatalf("skills 指令缺失: %q", p)
	}
}
