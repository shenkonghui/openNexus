// Package gatewaymcp 实现 MCP 聚合网关：把全局 mcp.json 中配置的多个 MCP server
// 汇聚成单一 Streamable HTTP endpoint 对外暴露。
//
// 动机：相当一部分 ACP agent 并不实现 session/new 里的 mcpServers 参数，
// 导致我们注入的 MCP server 被静默忽略。网关把 N 个 server 收敛成 1 个，
// 这样即便需要在 agent 自身的原生配置里手工配置，也只需配置一次——
// 之后增删任何 MCP server 都不必再改 agent 配置。
//
// 工具名以 <上游名>__<原工具名> 暴露，避免多个上游之间的命名冲突。
package gatewaymcp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opennexus/internal/acp"
)

// GatewayMCPName 是网关在全局 mcp.json 中的条目名（也用于跳过自引用）。
const GatewayMCPName = acp.GatewayMCPName

// GatewayPath 是网关的 HTTP 路径。
const GatewayPath = "/mcp/gateway"

// Gateway 持有聚合器与 HTTP handler。多个路由应共享同一个实例，
// 以便复用上游连接池与工具缓存。
type Gateway struct {
	configPath    string
	publicBaseURL string
	agg           *Aggregator
	auth          Authenticator
	state         *stateManager
	handler       http.Handler
}

// New 创建网关。configPath 为全局 mcp.json 路径；auth 负责 token 校验与获取，
// 主程序用 NewDBAuthenticator，独立二进制用 NewStaticAuthenticator。
// statePath 为网关运行时状态文件路径（disabled + custom servers），空则用默认。
func New(configPath string, auth Authenticator, statePath string) (*Gateway, error) {
	state, err := newStateManager(statePath)
	if err != nil {
		return nil, fmt.Errorf("初始化网关状态管理器失败: %w", err)
	}
	g := &Gateway{
		configPath: configPath,
		agg:        NewAggregator(configPath, GatewayMCPName, state),
		auth:       auth,
		state:      state,
	}
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return g.newServer(r.Context())
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	g.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := g.auth.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r.WithContext(withUserID(r.Context(), uid)))
	})
	return g, nil
}

// DisableUpstream 在网关层面禁用指定上游（不修改 mcp.json），并使缓存失效。
func (g *Gateway) DisableUpstream(name string) error {
	if err := g.state.SetDisabled(name, true); err != nil {
		return err
	}
	g.agg.invalidate()
	return nil
}

// EnableUpstream 解除禁用，并使缓存失效。
func (g *Gateway) EnableUpstream(name string) error {
	if err := g.state.SetDisabled(name, false); err != nil {
		return err
	}
	g.agg.invalidate()
	return nil
}

// AddCustomServer 在网关层面添加自定义上游（不写入 mcp.json），并使缓存失效。
func (g *Gateway) AddCustomServer(name string, entry acp.MCPServerEntry) error {
	if err := g.state.AddCustomServer(name, entry); err != nil {
		return err
	}
	g.agg.invalidate()
	return nil
}

// RemoveCustomServer 移除自定义上游，并使缓存失效。
func (g *Gateway) RemoveCustomServer(name string) error {
	if err := g.state.RemoveCustomServer(name); err != nil {
		return err
	}
	g.agg.invalidate()
	return nil
}

// Handler 返回带 Bearer 鉴权的 Streamable HTTP Handler。
func (g *Gateway) Handler() http.Handler { return g.handler }

// Close 关闭所有上游连接。
func (g *Gateway) Close() { g.agg.Close() }

// newServer 基于当前工具快照构建一个 MCP server（Stateless 模式下每请求一个）。
// 快照命中缓存时开销极小，只是把已知工具注册成转发 handler。
func (g *Gateway) newServer(ctx context.Context) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: GatewayMCPName, Version: "1.0.0"}, &mcp.ServerOptions{
		Instructions: "openNexus MCP 聚合网关：工具以 <server>__<tool> 命名，来自全局 MCP 配置中的各个上游 server。",
	})
	for _, ref := range g.agg.Snapshot(ctx) {
		srv.AddTool(ref.tool, g.forward(ref.exposedName))
	}
	return srv
}

// forward 返回把调用透传到上游的 handler。上游结果原样返回（含 structuredContent / isError）。
func (g *Gateway) forward(exposedName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, ok := userIDFrom(ctx); !ok {
			return nil, fmt.Errorf("未认证")
		}
		var args any
		if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
			args = req.Params.Arguments
		}
		return g.agg.Call(ctx, exposedName, args)
	}
}
