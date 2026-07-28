package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"opennexus/internal/config"
)

// goal 评估角色：文件式定义（markdown + frontmatter，对齐 subagent 文件规范），
// 一个角色 = 评估 agent/model + 指定 skills + 自定义评估 prompt 模板。
// goal 首次评估时由小模型根据完成条件自动选取最匹配的角色，
// 无匹配则回退内置评估逻辑（GoalSettings / 会话 agent + 标准 prompt）。

// GoalRoleDef 表示一个已发现的 goal 评估角色定义。
//
// 角色文件由 YAML frontmatter（name/description/agent/model/skills）+ markdown 正文构成，
// 正文作为评估 prompt 模板，支持 {{condition}} / {{transcript}} 占位符；
// 不含占位符时正文作为"评估指引"段落与标准条件/摘录段落拼接。
type GoalRoleDef struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Agent          string   `json:"agent,omitempty"`  // 评估 agent，空则走 GoalSettings/会话 agent 回退链
	Model          string   `json:"model,omitempty"`  // 评估模型
	Skills         []string `json:"skills,omitempty"` // 评估前要求运用的 skill 名
	PromptTemplate string   `json:"-"`                // 正文，评估 prompt 模板
	Location       string   `json:"location"`         // .md 文件绝对路径
	Scope          string   `json:"scope"`            // "project" | "user"
	Path           string   `json:"path"`             // 相对扫描根目录的展示路径
}

// goalRoleFrontmatter 是角色 .md 文件头部的 YAML frontmatter。
type goalRoleFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Agent       string `yaml:"agent"`
	Model       string `yaml:"model"`
	// RawSkills 用 yaml.Node 承载，兼容逗号分隔字符串与 YAML 列表两种写法（同 subagent tools）。
	RawSkills yaml.Node `yaml:"skills"`
}

// SetGoalRoleDirs 注入/热刷新 goal 评估角色扫描目录（独立 setter，不动 SetScanDirs 签名）。
func (s *Service) SetGoalRoleDirs(cfg config.GoalRolesConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goalRoleUserDirs = append([]string(nil), cfg.UserDirs...)
	s.goalRoleProjectDirs = append([]string(nil), cfg.ProjectDirs...)
}

// GoalRolesSnapshot 用当前注入的扫描目录扫描角色列表，并返回主用户目录（新建角色文件的落盘位置）。
// cwd 可为空（仅扫用户目录），供设置页角色管理接口使用。
func (s *Service) GoalRolesSnapshot(cwd string) ([]GoalRoleDef, string) {
	s.mu.RLock()
	userDirs := append([]string(nil), s.goalRoleUserDirs...)
	projectDirs := append([]string(nil), s.goalRoleProjectDirs...)
	s.mu.RUnlock()
	primary := ""
	if len(userDirs) > 0 {
		primary = userDirs[0]
	}
	return ScanGoalRoles(cwd, userDirs, projectDirs), primary
}

// ScanGoalRoles 扫描工作区与用户配置的 goal-roles 目录（递归子目录中的 *.md）。
// userDirs 为绝对路径；projectSubdirs 为相对 cwd 的子目录。project 优先于 user，按 name 去重。
func ScanGoalRoles(cwd string, userDirs, projectSubdirs []string) []GoalRoleDef {
	scanDirs := skillScanDirs(cwd, userDirs, projectSubdirs)

	seen := make(map[string]bool)
	var defs []GoalRoleDef

	for _, dir := range scanDirs {
		scanGoalRolesUnder(dir.Path, dir.Path, dir.Scope, seen, &defs)
	}
	return defs
}

// scanGoalRolesUnder 递归扫描目录树，发现 *.md 文件即尝试解析为 goal 角色。
func scanGoalRolesUnder(root, scanRoot, scope string, seen map[string]bool, defs *[]GoalRoleDef) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		entryPath := filepath.Join(root, name)
		if isScanEntryDir(entryPath, entry) {
			scanGoalRolesUnder(entryPath, scanRoot, scope, seen, defs)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		content, err := os.ReadFile(entryPath)
		if err != nil {
			continue
		}
		def, ok := parseGoalRoleMarkdown(name, content)
		if !ok {
			continue
		}
		if seen[def.Name] {
			continue
		}
		seen[def.Name] = true
		def.Location = entryPath
		def.Scope = scope
		def.Path = commandDisplayPath(entryPath, scanRoot)
		*defs = append(*defs, def)
	}
}

// parseGoalRoleMarkdown 解析角色 .md 文件。
// 要求 frontmatter 中 description 非空（自动选取的依据）；正文（strip frontmatter 后）作为 PromptTemplate。
func parseGoalRoleMarkdown(filename string, content []byte) (GoalRoleDef, bool) {
	base := strings.TrimSuffix(filename, ".md")
	base = strings.TrimSuffix(base, ".MD")
	text := string(content)

	def := GoalRoleDef{}
	if strings.HasPrefix(text, "---") {
		rest := text[3:]
		if strings.HasPrefix(rest, "\r\n") {
			rest = rest[2:]
		} else if strings.HasPrefix(rest, "\n") {
			rest = rest[1:]
		}
		if endIdx := strings.Index(rest, "\n---"); endIdx >= 0 {
			var fm goalRoleFrontmatter
			if yaml.Unmarshal([]byte(rest[:endIdx]), &fm) == nil {
				def.Name = strings.TrimSpace(fm.Name)
				def.Description = strings.TrimSpace(fm.Description)
				def.Agent = strings.TrimSpace(fm.Agent)
				def.Model = strings.TrimSpace(fm.Model)
				def.Skills = asToolsList(&fm.RawSkills)
			}
		}
	}

	if def.Name == "" {
		def.Name = base
	}
	// description 必填（自动选取角色的判断依据）
	if strings.TrimSpace(def.Description) == "" {
		return GoalRoleDef{}, false
	}

	def.PromptTemplate = strings.TrimSpace(stripSubAgentFrontmatter(content))
	return def, true
}

// selectGoalRoles 用小模型根据完成条件从候选角色中自动选取评估角色（可多选，多角色会签评估）。
// 返回空切片表示无匹配/选取失败，调用方回退内置评估逻辑。
func (s *Service) selectGoalRoles(ctx context.Context, evalAgent, evalModel, condition string, roles []GoalRoleDef) []GoalRoleDef {
	if len(roles) == 0 {
		return nil
	}
	var list strings.Builder
	for _, r := range roles {
		fmt.Fprintf(&list, "- %s: %s\n", r.Name, r.Description)
	}
	prompt := fmt.Sprintf(`你是评估角色选择器。根据 goal 完成条件，从候选角色中选出适合负责评估的角色。
可多选（每行输出一个 name）：完成条件涉及多个方面时选多个角色会签评估，否则只选最匹配的一个；
都不合适时输出 NONE。不要输出其他内容。

完成条件：
%s

候选角色：
%s`, condition, list.String())

	out, err := s.RunPromptOnce(ctx, evalAgent, evalModel, prompt)
	if err != nil {
		return nil
	}
	return matchGoalRoles(out, roles)
}

// matchGoalRoles 从选择器输出中解析角色 name 列表：逐行解析、容忍引号/反引号/列表符号包裹；
// 行内按逗号/顿号切 token 做精确匹配（按输出顺序，支持 "a, b" 单行多选）；
// 包含匹配兜底仅限首个有效行（后续行多为说明文字，提及角色名不代表选中），
// 且角色名互为子串时只取最长命中。按 name 去重；NONE、空输出或全部未命中返回 nil（回退内置评估）。
func matchGoalRoles(out string, roles []GoalRoleDef) []GoalRoleDef {
	if strings.EqualFold(strings.TrimSpace(out), "NONE") {
		return nil
	}
	seen := make(map[string]bool)
	var matched []GoalRoleDef
	add := func(r GoalRoleDef) {
		if !seen[r.Name] {
			seen[r.Name] = true
			matched = append(matched, r)
		}
	}
	firstLine := true
	for _, raw := range strings.Split(out, "\n") {
		line := strings.Trim(strings.TrimSpace(raw), "`\"'*#- ")
		if line == "" || strings.EqualFold(line, "NONE") {
			continue
		}
		exact := false
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == '，' || r == '、' }) {
			tok = strings.Trim(strings.TrimSpace(tok), "`\"'* ")
			for i := range roles {
				if strings.EqualFold(roles[i].Name, tok) {
					add(roles[i])
					exact = true
					break
				}
			}
		}
		if !exact && firstLine {
			// 包含匹配兜底：支持 "选择 qa-reviewer 负责评估" 等夹带说明文字的写法
			lower := strings.ToLower(line)
			best := -1
			for i := range roles {
				if strings.Contains(lower, strings.ToLower(roles[i].Name)) {
					if best < 0 || len(roles[i].Name) > len(roles[best].Name) {
						best = i
					}
				}
			}
			if best >= 0 {
				add(roles[best])
			}
		}
		firstLine = false
	}
	return matched
}

// goalRoleNames 把角色列表拼成逗号分隔的展示名串。
func goalRoleNames(roles []GoalRoleDef) string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return strings.Join(names, "、")
}

// goalEvalOutputRule 是评估输出的固定约束，无论角色模板怎么写都强制追加，保证 parseGoalVerdict 可解析。
const goalEvalOutputRule = "只输出两行：第一行 YES 或 NO（YES=已完全满足条件）；第二行用一句话说明理由。\n不要输出其他内容。"

// buildGoalEvalPrompt 构建 goal 评估 prompt。role 为 nil 时返回内置标准 prompt（行为不变）；
// 有 role 时以角色正文为模板：替换 {{condition}}/{{transcript}} 占位符（缺失的段落自动补齐），
// 有 skills 时前置运用技能的指令（RunPromptOnce 临时会话已挂载 skill 目录，agent 可读取）。
func buildGoalEvalPrompt(role *GoalRoleDef, condition, transcript string) string {
	if role == nil {
		return fmt.Sprintf(`你是任务完成度评估器。判断以下对话是否已满足给定的完成条件。
%s

完成条件：
%s

最近对话摘录：
%s`, goalEvalOutputRule, condition, transcript)
	}

	var sb strings.Builder
	if len(role.Skills) > 0 {
		fmt.Fprintf(&sb, "评估前请先阅读并运用以下技能（已挂载于会话技能目录）：%s\n\n", strings.Join(role.Skills, "、"))
	}
	body := strings.TrimSpace(role.PromptTemplate)
	hasCond := strings.Contains(body, "{{condition}}")
	hasTrans := strings.Contains(body, "{{transcript}}")
	if body != "" && (hasCond || hasTrans) {
		body = strings.ReplaceAll(body, "{{condition}}", condition)
		body = strings.ReplaceAll(body, "{{transcript}}", transcript)
		sb.WriteString(body)
		if !hasCond {
			fmt.Fprintf(&sb, "\n\n完成条件：\n%s", condition)
		}
		if !hasTrans {
			fmt.Fprintf(&sb, "\n\n最近对话摘录：\n%s", transcript)
		}
	} else {
		sb.WriteString("你是任务完成度评估器。判断以下对话是否已满足给定的完成条件。\n")
		if body != "" {
			sb.WriteString("\n评估指引：\n" + body + "\n")
		}
		fmt.Fprintf(&sb, "\n完成条件：\n%s\n\n最近对话摘录：\n%s", condition, transcript)
	}
	sb.WriteString("\n\n" + goalEvalOutputRule)
	return sb.String()
}
