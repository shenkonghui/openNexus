package acp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureBuiltinSkills_ReleasesExcel(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("EnsureBuiltinSkills 错误: %v", err)
	}
	skillPath := filepath.Join(dir, "excel", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("excel/SKILL.md 未释放: %v", err)
	}
	if len(data) == 0 {
		t.Errorf("excel/SKILL.md 内容为空")
	}
	// 内容应与 embed 一致
	if string(data) != builtinSkillExcel {
		t.Errorf("excel/SKILL.md 内容与 embed 不一致")
	}
}

func TestEnsureBuiltinSkills_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("首次 EnsureBuiltinSkills 错误: %v", err)
	}
	skillPath := filepath.Join(dir, "excel", "SKILL.md")
	first, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	// 第二次调用应幂等（不报错、内容不变）
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("二次 EnsureBuiltinSkills 错误: %v", err)
	}
	second, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("二次调用后内容变化: first=%d bytes second=%d bytes", len(first), len(second))
	}
}

func TestEnsureBuiltinSkills_OverwritesOnContentChange(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("首次 EnsureBuiltinSkills 错误: %v", err)
	}
	skillPath := filepath.Join(dir, "excel", "SKILL.md")
	// 篡改文件内容
	if err := os.WriteFile(skillPath, []byte("tampered"), 0o644); err != nil {
		t.Fatalf("篡改失败: %v", err)
	}
	// 再次调用应覆盖回正确内容
	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("二次 EnsureBuiltinSkills 错误: %v", err)
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(data) != builtinSkillExcel {
		t.Errorf("篡改后未恢复为 embed 内容")
	}
}

func TestBuiltinSkillsDir_NonEmpty(t *testing.T) {
	d := BuiltinSkillsDir()
	if d == "" {
		t.Errorf("BuiltinSkillsDir 返回空")
	}
	if !filepath.IsAbs(d) {
		t.Errorf("BuiltinSkillsDir 应返回绝对路径, got %q", d)
	}
}
