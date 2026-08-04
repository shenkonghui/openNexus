package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRules_ConfiguredDirs(t *testing.T) {
	root := t.TempDir()
	userRules := filepath.Join(root, "user-rules")
	projectRoot := filepath.Join(root, "project")
	projectRules := filepath.Join(projectRoot, ".cursor", "rules")

	for _, dir := range []string{userRules, projectRules} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	userRule := `---
description: global rule
alwaysApply: true
---
Always respond in Chinese.`
	projectRule := `---
description: project rule
alwaysApply: true
---
Use Go conventions.`

	if err := os.WriteFile(filepath.Join(userRules, "global.mdc"), []byte(userRule), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRules, "project.mdc"), []byte(projectRule), 0o644); err != nil {
		t.Fatal(err)
	}

	rules := ScanRules(projectRoot, []string{userRules}, []string{".cursor/rules"})
	if len(rules) != 2 {
		t.Fatalf("期望 2 个 rules，实际 %d", len(rules))
	}
	if rules[0].Name != "project" || rules[0].Scope != "project" || !rules[0].AlwaysApply {
		t.Fatalf("project rule 异常: %+v", rules[0])
	}
	if rules[1].Name != "global" || rules[1].Scope != "user" || !rules[1].AlwaysApply {
		t.Fatalf("user rule 异常: %+v", rules[1])
	}
}

func TestScanRules_ConfiguredFile(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	claudeMD := filepath.Join(root, "CLAUDE.md")
	projectClaude := filepath.Join(projectRoot, "CLAUDE.md")

	if err := os.WriteFile(claudeMD, []byte("# Global instructions\nUse Chinese."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectClaude, []byte("# Project instructions\nUse Go."), 0o644); err != nil {
		t.Fatal(err)
	}

	rules := ScanRules(projectRoot, []string{claudeMD}, []string{"CLAUDE.md"})
	if len(rules) != 2 {
		t.Fatalf("期望 2 个 rules，实际 %d: %+v", len(rules), rules)
	}
	if rules[0].Name != "CLAUDE" || rules[0].Scope != "project" || !rules[0].AlwaysApply {
		t.Fatalf("project CLAUDE.md 异常: %+v", rules[0])
	}
	if rules[1].Name != "CLAUDE" || rules[1].Scope != "user" || !rules[1].AlwaysApply {
		t.Fatalf("user CLAUDE.md 异常: %+v", rules[1])
	}

	out := AlwaysApplySystemPrompt(projectRoot, []string{claudeMD}, []string{"CLAUDE.md"})
	if !strings.Contains(out, "Project instructions") || !strings.Contains(out, "Global instructions") {
		t.Fatalf("未汇总 CLAUDE.md 内容: %q", out)
	}
}

func TestRuleAdditionalDirectories_FileAndDir(t *testing.T) {
	root := t.TempDir()
	ruleDir := filepath.Join(root, "rules")
	claudeMD := filepath.Join(root, "CLAUDE.md")
	if err := os.MkdirAll(ruleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeMD, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs := RuleAdditionalDirectories("", []string{claudeMD, ruleDir}, nil)
	if len(dirs) != 2 {
		t.Fatalf("期望 2 个 additional dir，实际 %d: %v", len(dirs), dirs)
	}
}

func TestAlwaysApplySystemPrompt(t *testing.T) {
	root := t.TempDir()
	userRules := filepath.Join(root, "user-rules")
	if err := os.MkdirAll(userRules, 0o755); err != nil {
		t.Fatal(err)
	}
	rule := `---
description: test
alwaysApply: true
---
Rule body here.`
	if err := os.WriteFile(filepath.Join(userRules, "test.mdc"), []byte(rule), 0o644); err != nil {
		t.Fatal(err)
	}

	out := AlwaysApplySystemPrompt("", []string{userRules}, nil)
	if out != "Rule body here." {
		t.Fatalf("期望纯规则正文, 得到: %q", out)
	}

	empty := AlwaysApplySystemPrompt("", []string{filepath.Join(root, "missing")}, nil)
	if empty != "" {
		t.Fatalf("无规则时应返回空: %q", empty)
	}
}

func TestWrapRulePrompt(t *testing.T) {
	out := wrapRulePrompt("Rule body here.")
	// 必须以 <project_rules> 标签包裹，正文前后有引导语
	if !strings.HasPrefix(out, "<project_rules>\n") {
		t.Fatalf("缺少开标签: %q", out)
	}
	if !strings.HasSuffix(out, "</project_rules>") {
		t.Fatalf("缺少闭标签: %q", out)
	}
	if !strings.Contains(out, "Rule body here.") {
		t.Fatalf("正文丢失: %q", out)
	}
	// 空正文仍应正常包裹（调用方负责判空，wrapRulePrompt 本身不判）
	empty := wrapRulePrompt("")
	if !strings.Contains(empty, "<project_rules>") || !strings.HasSuffix(empty, "</project_rules>") {
		t.Fatalf("空正文包裹异常: %q", empty)
	}
}
