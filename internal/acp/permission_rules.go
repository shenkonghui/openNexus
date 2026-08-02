package acp

import (
	"strings"

	"opennexus/internal/config"
)

// Decision 表示全局权限规则对一个工具调用的裁决。
type Decision int

const (
	// DecisionAsk 走 UI 询问（默认）。
	DecisionAsk Decision = iota
	// DecisionAllow 自动放行。
	DecisionAllow
	// DecisionDeny 自动拒绝（最高优先级）。
	DecisionDeny
)

// PermissionRules 是生效中的全局权限规则（从 config.PermissionsConfig 构造）。
// 规则按 agent 上报的 ToolCall.Title 匹配，支持 `*` 通配符，大小写不敏感。
// 白/询问/黑名单全局生效；YOLO = 全局 Mode=yolo 或会话级开关。
// 优先级：deny > allow > ask > (yolo→allow | ask)。
type PermissionRules struct {
	Mode  string   // normal | yolo；yolo 时未命中名单自动放行
	Allow []string // 白名单：命中→放行
	Ask   []string // 询问名单：命中→强制询问
	Deny  []string // 黑名单：命中→拒绝
}

// matchGlob 报告 title 是否匹配 rule（大小写不敏感，`*` 通配任意字符序列，含 `/`）。
// 用自定义实现而非 path.Match——path.Match 的 `*` 不跨 `/`（路径分隔符语义），
// 而权限规则中的 `*` 应匹配命令参数里的路径（如 "Bash(cat:/etc/*)" 需匹配 "Bash(cat:/etc/passwd)")。
//
// 简化约定：规则不含 `*` 时自动按子串匹配（等价于前后补 `*`），
// 这样用户在 config.yaml 写 "git push" 即等价于 "*git push*"，无需手写通配符。
// 规则含 `*` 时保持原语义（支持精确前缀/后缀/中间通配）。
func matchGlob(rule, title string) bool {
	r := strings.ToLower(strings.TrimSpace(rule))
	t := strings.ToLower(strings.TrimSpace(title))
	if r == "" || t == "" {
		return false
	}
	if !strings.Contains(r, "*") {
		return strings.Contains(t, r)
	}
	return starMatch(r, t)
}

// starMatch 实现 `*` 匹配任意（含空）字符序列的简单 glob。
// `*` 可匹配包括 `/` 在内的任意字符（不同于 path.Match 的路径分隔语义）。
// 不支持 `?` 或字符类，保持 Claude Code 风格的最小语义。
// 算法：按 `*` 切分为字面段 segs；首段须为 s 前缀、末段须为 s 后缀、中间段按序在 s 中首现。
func starMatch(pattern, s string) bool {
	segs := strings.Split(pattern, "*")
	// 无 `*`：整段精确相等
	if len(segs) == 1 {
		return pattern == s
	}
	first := segs[0]
	// 首段必须匹配 s 开头
	if !strings.HasPrefix(s, first) {
		return false
	}
	last := segs[len(segs)-1]
	// 末段必须匹配 s 结尾（注意末段可能与已匹配首段重叠，需在剩余范围内校验）
	rest := s[len(first):]
	if !strings.HasSuffix(rest, last) {
		return false
	}
	// 中间段按序在 (rest 去掉末段) 中首现
	cur := rest
	if len(rest) >= len(last) {
		cur = rest[:len(rest)-len(last)]
	}
	for _, seg := range segs[1 : len(segs)-1] {
		k := strings.Index(cur, seg)
		if k < 0 {
			return false
		}
		cur = cur[k+len(seg):]
	}
	return true
}

// anyMatch 报告 title 是否匹配 rules 中任一规则。
func anyMatch(rules []string, title string) bool {
	for _, r := range rules {
		if matchGlob(r, title) {
			return true
		}
	}
	return false
}

// MatchDeny 返回 title 命中的第一条 deny 规则原文；未命中返回空串。
// 供 TerminalBridge 等执行层在 spawn 前做确定性拒绝（并把规则原文回显给 agent）。
func (r PermissionRules) MatchDeny(title string) string {
	for _, rule := range r.Deny {
		if matchGlob(rule, title) {
			return rule
		}
	}
	return ""
}

// Decide 根据工具调用标题与 YOLO（全局 Mode 或会话开关）返回裁决。
//   - title 为空：yolo → Allow；否则 Ask（无法匹配名单时的兜底）
//   - deny 命中 → DecisionDeny
//   - allow 命中 → DecisionAllow
//   - ask 命中 → DecisionAsk
//   - 全局/会话 yolo → DecisionAllow；否则 DecisionAsk
func (r PermissionRules) Decide(title string, yolo bool) Decision {
	yolo = yolo || r.Mode == config.PermissionModeYolo
	if strings.TrimSpace(title) == "" {
		if yolo {
			return DecisionAllow
		}
		return DecisionAsk
	}
	if anyMatch(r.Deny, title) {
		return DecisionDeny
	}
	if anyMatch(r.Allow, title) {
		return DecisionAllow
	}
	if anyMatch(r.Ask, title) {
		return DecisionAsk
	}
	if yolo {
		return DecisionAllow
	}
	return DecisionAsk
}
