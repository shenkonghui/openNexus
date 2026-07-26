package gatewaymcp

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"opennexus/internal/acp"
)

// Status 是网关对外暴露的整体状态（设置页展示用）。
type Status struct {
	Enabled   bool             `json:"enabled"`  // mcp.json 中是否已写入网关条目
	Endpoint  string           `json:"endpoint"` // 网关 endpoint 绝对 URL
	Path      string           `json:"path"`     // 全局 mcp.json 路径
	Token     string           `json:"token"`    // 调用网关所需的 Bearer token（当前用户）
	ToolCount int              `json:"tool_count"`
	Upstreams []UpstreamStatus `json:"upstreams"`
	Skipped   []SkippedEntry   `json:"skipped"`
}

// SetPublicBaseURL 设置对外 Base URL，用于拼 endpoint 与写入 mcp.json 条目。
func (g *Gateway) SetPublicBaseURL(baseURL string) {
	g.publicBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

// Endpoint 返回网关的对外 endpoint；未设置 Base URL 时返回空串。
func (g *Gateway) Endpoint() string {
	if g.publicBaseURL == "" {
		return ""
	}
	return g.publicBaseURL + GatewayPath
}

// SyncEntry 启动自愈：仅当 mcp.json 中已存在网关条目时，刷新其 url 与 token。
//
// 刻意不主动写入——启用网关会改变所有会话的 MCP 注入方式（其余 http/sse 上游
// 收敛到网关之后，见 acp.CollapseViaGateway），必须由用户在设置页显式开启。
func (g *Gateway) SyncEntry() {
	if g.findEntry() == nil {
		return
	}
	if err := g.EnableEntry(); err != nil {
		slog.Error("同步 MCP 网关条目失败", "path", g.configPath, "err", err)
	}
}

// EnableEntry 把 opennexus-gateway 条目写入全局 mcp.json，启用聚合网关。
// 复用内置 MCP server 的全局共享 token；尚无任何 token 时返回错误
// （此时网关无法鉴权，写了也连不上）。已存在且内容一致时为空操作。
func (g *Gateway) EnableEntry() error {
	endpoint := g.Endpoint()
	if g.configPath == "" || endpoint == "" || g.auth == nil {
		return errors.New("网关未配置 endpoint 或鉴权器")
	}
	token := g.SharedToken()
	if token == "" {
		return errors.New("尚未生成 MCP Token，请先在笔记设置中生成")
	}
	want := acp.MCPServerEntry{
		Type:    acp.MCPTypeHTTP,
		Url:     endpoint,
		Headers: map[string]string{"Authorization": "Bearer " + token},
	}
	if existing := g.findEntry(); existing != nil && entryEqual(*existing, want) {
		return nil
	}
	if err := acp.UpsertMCPServerEntry(g.configPath, GatewayMCPName, want); err != nil {
		return err
	}
	slog.Info("已启用 MCP 聚合网关", "name", GatewayMCPName, "path", g.configPath, "endpoint", endpoint)
	return nil
}

// DisableEntry 从 mcp.json 中移除网关条目，恢复"各上游直接注入会话"的行为。
func (g *Gateway) DisableEntry() error {
	if g.configPath == "" {
		return errors.New("未配置 MCP 配置文件路径")
	}
	if err := acp.RemoveMCPServerEntry(g.configPath, GatewayMCPName); err != nil {
		return err
	}
	slog.Info("已停用 MCP 聚合网关", "name", GatewayMCPName, "path", g.configPath)
	return nil
}

// Status 汇总网关当前状态。userID 为当前登录用户，用于返回其可用的 Bearer token。
func (g *Gateway) Status(ctx context.Context, userID uint) Status {
	upstreams, skipped := g.agg.Report(ctx)
	total := 0
	for _, u := range upstreams {
		total += u.ToolCount
	}
	return Status{
		Enabled:   g.findEntry() != nil,
		Endpoint:  g.Endpoint(),
		Path:      g.configPath,
		Token:     g.auth.TokenForUser(userID),
		ToolCount: total,
		Upstreams: upstreams,
		Skipped:   skipped,
	}
}

// findEntry 读取 mcp.json 中的网关条目；不存在或读取失败返回 nil。
func (g *Gateway) findEntry() *acp.MCPServerEntry {
	entries, err := acp.LoadMCPServerEntries(g.configPath)
	if err != nil {
		return nil
	}
	for _, ne := range entries {
		if ne.Name == GatewayMCPName {
			e := ne.Entry
			return &e
		}
	}
	return nil
}

// SharedToken 返回用于写入 mcp.json 条目与注入会话的全局 token（经 Authenticator 获取）。
func (g *Gateway) SharedToken() string {
	return g.auth.SharedToken()
}
