package gatewaymcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opennexus/internal/acp"
)

const (
	// toolNamePrefixSep 是"上游名 + 工具名"的分隔符。
	// go-sdk 的工具名只允许 [A-Za-z0-9_.-]，单下划线更简洁；
	// 路由按 exposedName 全名映射（findToolLocked），不反向解析，无歧义风险。
	toolNamePrefixSep = "_"

	// upstreamConnectTimeout 是连接单个上游并列举工具的超时。
	upstreamConnectTimeout = 8 * time.Second

	// toolCacheTTL 是工具快照的有效期。到期后下次请求会重新列举上游工具。
	// go-sdk 的 http 传输在本项目中禁用了 standalone SSE（见 acp.BuildMCPTransport），
	// 收不到 tools/list_changed 通知，因此用轮询兜底。
	toolCacheTTL = 30 * time.Second

	// upstreamRetryBackoff 是上游连接失败后的重试退避时长。
	// 没有退避的话，每次快照刷新都会为每个不可达上游重付一次连接超时。
	upstreamRetryBackoff = 60 * time.Second
)

// toolRef 是一条"暴露给下游的工具"到上游的映射。
type toolRef struct {
	exposedName string    // 暴露名：<上游名>_<原工具名>
	originName  string    // 上游的原始工具名
	upstream    string    // 上游名（mcp.json 中的 key）
	tool        *mcp.Tool // 暴露给下游的工具定义（已改名）
}

// upstream 是一个已解析的上游 MCP server 及其连接状态。
type upstream struct {
	name       string
	entry      acp.MCPServerEntry
	session    *mcp.ClientSession
	lastErr    error
	retryAfter time.Time // 连接失败后的退避截止时间
	source     string    // "mcp.json" | "custom"（供 Report 展示）
}

// fail 记录失败原因并进入退避窗口。
func (u *upstream) fail(err error, msg string) {
	u.lastErr = err
	u.retryAfter = time.Now().Add(upstreamRetryBackoff)
	slog.Warn(msg, "upstream", u.name, "err", err, "retry_after", upstreamRetryBackoff)
}

// UpstreamStatus 是单个上游在网关中的聚合状态（供设置页展示）。
type UpstreamStatus struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Source    string `json:"source"` // "mcp.json" | "custom" | "disabled"
	Connected bool   `json:"connected"`
	ToolCount int    `json:"tool_count"`
	Error     string `json:"error,omitempty"`
}

// SkippedEntry 是未被网关接管的配置条目及原因（供设置页解释"为什么没聚合"）。
type SkippedEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
	Disabled bool   `json:"disabled,omitempty"` // 被用户禁用（前端据此显示"启用"按钮）
}

// close 关闭上游会话并清空引用，使下次快照重新建连。
func (u *upstream) close() {
	if u.session != nil {
		_ = u.session.Close()
		u.session = nil
	}
}

// Aggregator 维护到所有上游 MCP server 的连接，并缓存聚合后的工具列表。
//
// 上游来源有两类：
//   - 全局 mcp.json（与 agent 会话注入、设置页"测试连接"同一份配置）
//   - 网关自定义上游（gateway-state.json 中的 custom_servers，不写入 mcp.json）
//
// 用户可在网关层面禁用某个上游（gateway-state.json 中的 disabled 列表），
// 被禁用的上游不会连接也不暴露工具，但不修改 mcp.json 原文。
//
// 配置文件变化（mtime/size）会自动触发全量重建。
type Aggregator struct {
	configPath string
	selfName   string // 网关自身在 mcp.json 中的条目名，跳过以避免自引用死循环
	state      *stateManager

	mu          sync.Mutex
	ups         map[string]*upstream
	tools       []toolRef
	skipped     []SkippedEntry
	refreshedAt time.Time
	cfgMtime    time.Time
	cfgSize     int64
}

// NewAggregator 创建聚合器。configPath 为全局 mcp.json 路径，selfName 为网关自身条目名。
// state 为持久化状态管理器（disabled + custom servers），可为 nil（禁用该功能）。
func NewAggregator(configPath, selfName string, state *stateManager) *Aggregator {
	return &Aggregator{
		configPath: configPath,
		selfName:   selfName,
		state:      state,
		ups:        make(map[string]*upstream),
	}
}

// Snapshot 返回当前聚合后的工具列表，必要时刷新缓存。
// 单个上游连接失败只影响该上游的工具，不影响整体（记日志后跳过）。
func (a *Aggregator) Snapshot(ctx context.Context) []toolRef {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureFreshLocked(ctx)
	return a.tools
}

// Report 返回各上游的聚合状态与被跳过的条目，供设置页展示。
func (a *Aggregator) Report(ctx context.Context) ([]UpstreamStatus, []SkippedEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureFreshLocked(ctx)

	counts := make(map[string]int, len(a.ups))
	for _, t := range a.tools {
		counts[t.upstream]++
	}
	names := make([]string, 0, len(a.ups))
	for name := range a.ups {
		names = append(names, name)
	}
	sort.Strings(names)

	list := make([]UpstreamStatus, 0, len(names))
	for _, name := range names {
		u := a.ups[name]
		st := UpstreamStatus{
			Name:      name,
			Type:      normalizedType(u.entry.Type),
			Source:    u.source,
			Connected: u.session != nil,
			ToolCount: counts[name],
		}
		if u.source == "" {
			st.Source = "mcp.json"
		}
		if u.lastErr != nil {
			st.Error = u.lastErr.Error()
		}
		list = append(list, st)
	}
	return list, append([]SkippedEntry(nil), a.skipped...)
}

// ensureFreshLocked 在配置变更或缓存过期时刷新快照。调用方须持锁。
func (a *Aggregator) ensureFreshLocked(ctx context.Context) {
	if a.configChangedLocked() {
		a.resetLocked()
	}
	if a.tools != nil && time.Since(a.refreshedAt) < toolCacheTTL {
		return
	}
	a.refreshLocked(ctx)
}

// Call 把一次 tools/call 转发到对应上游。
// 转发失败时关闭该上游连接，使下次快照自动重连。
func (a *Aggregator) Call(ctx context.Context, exposedName string, args any) (*mcp.CallToolResult, error) {
	a.mu.Lock()
	ref, ok := a.findToolLocked(exposedName)
	var sess *mcp.ClientSession
	if ok {
		if u := a.ups[ref.upstream]; u != nil {
			sess = u.session
		}
	}
	a.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("未知的工具 %q", exposedName)
	}
	if sess == nil {
		return nil, fmt.Errorf("上游 %q 当前不可用", ref.upstream)
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: ref.originName, Arguments: args})
	if err != nil {
		slog.Warn("MCP 网关转发失败", "upstream", ref.upstream, "tool", ref.originName, "err", err)
		a.mu.Lock()
		if u := a.ups[ref.upstream]; u != nil {
			u.close()
			u.lastErr = err
		}
		a.tools = nil // 强制下次快照重建
		a.mu.Unlock()
		return nil, fmt.Errorf("调用上游 %q 失败: %w", ref.upstream, err)
	}
	return res, nil
}

// Close 关闭全部上游连接（进程退出时调用）。
func (a *Aggregator) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resetLocked()
}

// invalidate 使工具缓存失效，下次 Snapshot/Report 时强制重建。
// 用于 disabled/custom servers 变更后立即生效。
func (a *Aggregator) invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools = nil
}

func (a *Aggregator) findToolLocked(exposedName string) (toolRef, bool) {
	for _, t := range a.tools {
		if t.exposedName == exposedName {
			return t, true
		}
	}
	return toolRef{}, false
}

// configChangedLocked 通过 mtime + size 判断 mcp.json 是否变化。文件不存在视为"空配置"。
func (a *Aggregator) configChangedLocked() bool {
	var mtime time.Time
	var size int64
	if fi, err := os.Stat(a.configPath); err == nil {
		mtime, size = fi.ModTime(), fi.Size()
	}
	if mtime.Equal(a.cfgMtime) && size == a.cfgSize {
		return false
	}
	a.cfgMtime, a.cfgSize = mtime, size
	return true
}

// resetLocked 关闭并丢弃所有上游连接与缓存。
func (a *Aggregator) resetLocked() {
	for _, u := range a.ups {
		u.close()
	}
	a.ups = make(map[string]*upstream)
	a.tools = nil
	a.skipped = nil
}

// refreshLocked 重新解析配置、补齐缺失连接并重建工具快照。
func (a *Aggregator) refreshLocked(ctx context.Context) {
	entries, err := acp.LoadMCPServerEntries(a.configPath)
	if err != nil {
		slog.Warn("MCP 网关加载配置失败", "path", a.configPath, "err", err)
		a.tools = []toolRef{}
		a.skipped = nil
		a.refreshedAt = time.Now()
		return
	}

	// 合并自定义上游（gateway-state.json 中的 custom_servers）。
	// 自定义上游与 mcp.json 同名时，mcp.json 优先（自定义被忽略并记 skipped）。
	var customServers []acp.NamedMCPServerEntry
	var disabled map[string]bool
	if a.state != nil {
		customServers, _ = a.state.CustomServers()
		disabled, _ = a.state.DisabledSet()
	}
	if disabled == nil {
		disabled = map[string]bool{}
	}
	seen := make(map[string]bool, len(entries))
	for _, ne := range entries {
		seen[ne.Name] = true
	}
	for _, cs := range customServers {
		if !seen[cs.Name] {
			entries = append(entries, cs)
		}
	}

	wanted := make(map[string]bool, len(entries))
	sources := make(map[string]string, len(entries)) // name -> "mcp.json" | "custom"
	var targets []*upstream
	var skipped []SkippedEntry
	for _, ne := range entries {
		if disabled[ne.Name] {
			skipped = append(skipped, SkippedEntry{Name: ne.Name, Type: normalizedType(ne.Entry.Type), Reason: "已被用户禁用", Disabled: true})
			continue
		}
		if reason := a.skipReason(ne); reason != "" {
			skipped = append(skipped, SkippedEntry{Name: ne.Name, Type: normalizedType(ne.Entry.Type), Reason: reason})
			continue
		}
		wanted[ne.Name] = true
		sources[ne.Name] = upstreamSource(ne, customServers)
		targets = append(targets, a.prepareUpstreamLocked(ne))
	}
	a.skipped = skipped

	// 清理已从配置中移除或被禁用的上游。
	for name, u := range a.ups {
		if !wanted[name] {
			u.close()
			delete(a.ups, name)
		}
	}

	// 并发建连 + 列举工具：串行会让每个不可达上游各阻塞一个连接超时，
	// 且整个刷新过程持锁，会拖垮所有正在进行的 tools/call。
	// 每个 goroutine 只访问自己的 *upstream，调用方持锁保证无并发刷新。
	results := make([][]toolRef, len(targets))
	var wg sync.WaitGroup
	for i, u := range targets {
		wg.Add(1)
		go func(idx int, up *upstream) {
			defer wg.Done()
			if !connectUpstream(ctx, up) {
				return
			}
			results[idx] = listUpstreamTools(ctx, up)
		}(i, u)
	}
	wg.Wait()

	var tools []toolRef
	for _, r := range results {
		tools = append(tools, r...)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].exposedName < tools[j].exposedName })
	a.tools = tools
	// 把 source 记到 upstream 上，供 Report 使用。
	for name, src := range sources {
		if u, ok := a.ups[name]; ok {
			u.source = src
		}
	}
	a.refreshedAt = time.Now()
}

// upstreamSource 判断一条上游来自 mcp.json 还是 custom。
func upstreamSource(ne acp.NamedMCPServerEntry, custom []acp.NamedMCPServerEntry) string {
	for _, cs := range custom {
		if cs.Name == ne.Name {
			return "custom"
		}
	}
	return "mcp.json"
}

// skipReason 返回该条配置不纳入网关的原因；返回空串表示接管。
// 第一阶段只接管 http/sse：stdio server 的工作目录语义依赖 agent 会话 cwd，
// 交由网关代管会改变行为，因此仍走 session/new 原路注入。
func (a *Aggregator) skipReason(ne acp.NamedMCPServerEntry) string {
	if ne.Name == a.selfName {
		return "网关自身条目，跳过以避免自引用"
	}
	if acp.GatewayCovers(ne.Entry) {
		return ""
	}
	switch ne.Entry.Type {
	case "", acp.MCPTypeStdio:
		return "stdio 上游的工作目录依赖会话 cwd，仍由 session/new 直接注入"
	case acp.MCPTypeHTTP, acp.MCPTypeSSE:
		return "缺少 url"
	default:
		return "未知的 type " + ne.Entry.Type
	}
}

// normalizedType 把空 type 归一为 stdio（与设置页探测的展示保持一致）。
func normalizedType(t string) string {
	if t == "" {
		return acp.MCPTypeStdio
	}
	return t
}

// prepareUpstreamLocked 取出（或新建）该条目对应的 upstream，并在配置变更时断开旧连接。
// 只做簿记，不发起网络请求——建连由 connectUpstream 并发完成。
func (a *Aggregator) prepareUpstreamLocked(ne acp.NamedMCPServerEntry) *upstream {
	u, ok := a.ups[ne.Name]
	if !ok {
		u = &upstream{name: ne.Name, entry: ne.Entry}
		a.ups[ne.Name] = u
		return u
	}
	// 配置内容变了（如 token 轮换）→ 断开重连，并清掉退避
	if !entryEqual(u.entry, ne.Entry) {
		u.close()
		u.entry = ne.Entry
		u.retryAfter = time.Time{}
	}
	return u
}

// connectUpstream 确保上游已连接，返回是否可用。
// 连接失败的上游进入退避窗口，避免每次快照刷新都为它付一次连接超时。
func connectUpstream(ctx context.Context, u *upstream) bool {
	if u.session != nil {
		return true
	}
	if !u.retryAfter.IsZero() && time.Now().Before(u.retryAfter) {
		return false
	}

	transport, err := acp.BuildMCPTransport(u.entry)
	if err != nil {
		u.fail(err, "MCP 网关构造上游传输失败")
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, upstreamConnectTimeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "opennexus-mcp-gateway", Version: "1.0.0"}, nil)
	sess, err := client.Connect(cctx, transport, nil)
	if err != nil {
		u.fail(err, "MCP 网关连接上游失败")
		return false
	}
	u.session = sess
	u.lastErr = nil
	u.retryAfter = time.Time{}
	slog.Info("MCP 网关已连接上游", "upstream", u.name, "type", u.entry.Type)
	return true
}

// listUpstreamTools 列举单个上游的工具并转换为带前缀的 toolRef。
func listUpstreamTools(ctx context.Context, u *upstream) []toolRef {
	lctx, cancel := context.WithTimeout(ctx, upstreamConnectTimeout)
	defer cancel()

	var refs []toolRef
	for tool, err := range u.session.Tools(lctx, nil) {
		if err != nil {
			slog.Warn("MCP 网关列举上游工具失败", "upstream", u.name, "err", err)
			break
		}
		if tool == nil {
			continue
		}
		exposed := u.name + toolNamePrefixSep + tool.Name
		copied := *tool
		copied.Name = exposed
		if copied.InputSchema == nil {
			copied.InputSchema = map[string]any{"type": "object"}
		}
		refs = append(refs, toolRef{
			exposedName: exposed,
			originName:  tool.Name,
			upstream:    u.name,
			tool:        &copied,
		})
	}
	return refs
}

// entryEqual 比较两条配置是否等价（用于检测 token/url 变更触发重连）。
func entryEqual(a, b acp.MCPServerEntry) bool {
	if a.Type != b.Type || a.Url != b.Url || len(a.Headers) != len(b.Headers) {
		return false
	}
	for k, v := range a.Headers {
		if b.Headers[k] != v {
			return false
		}
	}
	return true
}
