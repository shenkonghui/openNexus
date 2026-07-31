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

// DefaultDenyRules 是内置默认黑名单：外发不可逆操作与系统级破坏，命中即自动拒绝。
// 与用户配置的 deny 合并生效；用户可在配置中写 "!规则原文" 显式移除某条默认规则。
// 规则为大小写不敏感的 `*` 子串通配，按 agent 上报的工具调用标题匹配。
var DefaultDenyRules = []string{
	// —— 远端推送 / 发布（外发不可逆）——
	"*git push*",
	"*git remote set-url*",
	"*docker push*",
	"*docker login*",
	"*crane push*",
	"*skopeo copy*",
	"*helm push*",
	"*npm publish*",
	// —— 本地 Git 历史/工作区不可逆改写（丢失已提交或未提交内容）——
	"*git reset --hard*",
	"*git clean -f*",
	"*git checkout -f*",
	"*git checkout --force*",
	"*git branch -D*",
	"*git filter-branch*",
	// —— 系统级破坏 / 重启关机 ——
	"*reboot*",
	"*shutdown*",
	"*poweroff*",
	"*halt*",
	"*init 0*",
	"*init 6*",
	"*mkfs*",
	"*fdisk*",
	"*dd if=*",
	// —— 灾难性删除（针对根/家目录的强制递归删除；相对路径删除见 Ask 名单）——
	"*rm -rf /*",
	"*rm -fr /*",
	"*rm -rf ~*",
	"*rm -rf --no-preserve-root*",
}

// DefaultAskRules 是内置默认询问名单：本地可逆但需留痕/确认的操作。
// 沙箱开启时由调用方跳过（环境已兜底，避免打断全自动流程）。
var DefaultAskRules = []string{
	// —— Git 需留痕的写操作 ——
	"*git commit*",
	"*git merge*",
	"*git rebase*",
	"*git tag*",
	"*git stash*",
	// —— 提权 / 远端访问 ——
	"*sudo *",
	"*ssh *",
	"*scp *",
	"*sftp *",
	// —— 强制递归删除（非根目录）/ 批量改权限 ——
	"*rm -rf*",
	"*rm -fr*",
	"*chmod -R*",
	"*chown -R*",
	// —— 集群变更 ——
	"*kubectl delete*",
	"*kubectl apply*",
}

// DefaultAllowRules 是内置默认白名单：只读/无副作用的常用命令，命中即自动放行（免打断）。
// 与用户配置的 allow 合并生效；由于 deny 优先级最高，含破坏性子命令（如 "git branch -D"）
// 仍会被 deny 拦截，白名单的宽松通配（如 "*git branch*"）不会放开它们。
var DefaultAllowRules = []string{
	"*git status*",
	"*git diff*",
	"*git log*",
	"*git show*",
	"*git branch*",
	"*git fetch*",
	"*git remote -v*",
	"*ls *",
	"*pwd*",
	"*cat *",
}

// MergeRuleDefaults 合并内置默认名单与用户规则：
//   - 用户规则中以 `!` 开头的条目表示移除同文默认规则（大小写不敏感），本身不进入结果；
//   - 其余用户规则追加在默认规则之后，重复项（大小写不敏感）去重。
func MergeRuleDefaults(defaults, user []string) []string {
	removed := make(map[string]bool)
	for _, r := range user {
		r = strings.TrimSpace(r)
		if strings.HasPrefix(r, "!") {
			removed[strings.ToLower(strings.TrimSpace(r[1:]))] = true
		}
	}
	seen := make(map[string]bool)
	out := make([]string, 0, len(defaults)+len(user))
	appendRule := func(r string) {
		r = strings.TrimSpace(r)
		key := strings.ToLower(r)
		if r == "" || strings.HasPrefix(r, "!") || removed[key] || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, r)
	}
	for _, r := range defaults {
		appendRule(r)
	}
	for _, r := range user {
		appendRule(r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

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
func matchGlob(rule, title string) bool {
	r := strings.ToLower(strings.TrimSpace(rule))
	t := strings.ToLower(strings.TrimSpace(title))
	if r == "" || t == "" {
		return false
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
