package acp

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed builtin_skills/excel/SKILL.md
var builtinSkillExcel string

// builtinSkillEntry 描述一个内置 skill：相对路径与其内容。
type builtinSkillEntry struct {
	relPath string // 相对 builtinSkillsDir 的路径，如 excel/SKILL.md
	content string
}

// builtinSkillEntries 是所有内置 skill 文件清单。
// 新增内置 skill 时在此追加一条（relPath 用 / 分隔）。
var builtinSkillEntries = []builtinSkillEntry{
	{relPath: "excel/SKILL.md", content: builtinSkillExcel},
}

// EnsureBuiltinSkills 把内置 skill 文件释放到 dir 目录。
// 幂等：文件已存在且内容一致时跳过；内容不一致时覆盖（保证升级后内置 skill 同步更新）。
// 目录不存在时自动创建。dir 为空时使用默认 ~/.openNexus/builtin-skills。
func EnsureBuiltinSkills(dir string) error {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("解析内置 skill 目录: %w", err)
		}
		dir = filepath.Join(home, ".openNexus", "builtin-skills")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建内置 skill 目录 %s: %w", dir, err)
	}
	for _, e := range builtinSkillEntries {
		target := filepath.Join(dir, filepath.FromSlash(e.relPath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建内置 skill 子目录 %s: %w", filepath.Dir(target), err)
		}
		existing, err := os.ReadFile(target)
		if err == nil && string(existing) == e.content {
			continue // 内容一致，跳过
		}
		if err := os.WriteFile(target, []byte(e.content), 0o644); err != nil {
			return fmt.Errorf("写入内置 skill %s: %w", target, err)
		}
	}
	return nil
}

// BuiltinSkillsDir 返回默认内置 skill 目录（~/.openNexus/builtin-skills），供 config 注入扫描路径。
func BuiltinSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".openNexus", "builtin-skills")
}
