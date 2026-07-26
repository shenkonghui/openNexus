package gatewaymcp

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opennexus/internal/acp"
)

// bridgeRefreshInterval 是桥接端同步网关工具列表的周期。
// 与网关侧工具快照 TTL（toolCacheTTL=30s）对齐：上游增删后桥接端最迟一个周期跟进。
const bridgeRefreshInterval = 30 * time.Second

// RunStdioBridge 把聚合网关的 Streamable HTTP endpoint 桥接为 stdio MCP server。
//
// 用途：部分 ACP agent（如 devin）握手声明 mcpCapabilities http:false，
// 无法直接消费网关的 http 条目；stdio 是 ACP 基线能力，主程序对这类 agent
// 注入 `opennexus mcp-bridge` 形态的 stdio 条目，本函数即该子进程的主体。
//
// 阻塞运行直到 ctx 取消或 stdin 关闭（agent 结束 MCP server 时关闭管道）。
func RunStdioBridge(ctx context.Context, url, token string) error {
	entry := acp.MCPServerEntry{
		Type:    acp.MCPTypeHTTP,
		Url:     url,
		Headers: map[string]string{"Authorization": "Bearer " + token},
	}
	transport, err := acp.BuildMCPTransport(entry)
	if err != nil {
		return fmt.Errorf("构造网关传输失败: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "opennexus-mcp-bridge", Version: "1.0.0"}, nil)
	cctx, cancel := context.WithTimeout(ctx, upstreamConnectTimeout)
	sess, err := client.Connect(cctx, transport, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("连接聚合网关 %s 失败: %w", url, err)
	}
	defer func() { _ = sess.Close() }()

	srv := mcp.NewServer(&mcp.Implementation{Name: GatewayMCPName, Version: "1.0.0"}, &mcp.ServerOptions{
		Instructions: "openNexus MCP 聚合网关（stdio 桥）：工具以 <server>__<tool> 命名，来自全局 MCP 配置中的各个上游 server。",
	})

	// 首次同步失败直接退出：一个工具都拿不到时保持进程存活没有意义，
	// 让 agent 感知失败并走自身的 MCP 失败处理。
	known := map[string]bool{}
	if err := syncBridgeTools(ctx, srv, sess, known); err != nil {
		return fmt.Errorf("列举网关工具失败: %w", err)
	}

	// 周期同步：网关上游增删（mcp.json 变更、上游重连）后，
	// 通过 AddTool/RemoveTools 触发 list_changed 通知下游 agent。
	go func() {
		ticker := time.NewTicker(bridgeRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := syncBridgeTools(ctx, srv, sess, known); err != nil {
					slog.Warn("stdio 桥同步网关工具失败", "err", err)
				}
			}
		}
	}()

	return srv.Run(ctx, &mcp.StdioTransport{})
}

// syncBridgeTools 从网关会话列举工具并与本地已注册集合做差量同步。
// known 为已注册工具名集合，由调用方持有（仅在单 goroutine 中访问）。
func syncBridgeTools(ctx context.Context, srv *mcp.Server, sess *mcp.ClientSession, known map[string]bool) error {
	lctx, cancel := context.WithTimeout(ctx, upstreamConnectTimeout)
	defer cancel()

	seen := map[string]bool{}
	for tool, err := range sess.Tools(lctx, nil) {
		if err != nil {
			return err
		}
		if tool == nil {
			continue
		}
		seen[tool.Name] = true
		if known[tool.Name] {
			continue
		}
		copied := *tool
		if copied.InputSchema == nil {
			copied.InputSchema = map[string]any{"type": "object"}
		}
		srv.AddTool(&copied, forwardToGateway(sess, tool.Name))
		known[tool.Name] = true
	}

	// 移除网关侧已消失的工具。
	var gone []string
	for name := range known {
		if !seen[name] {
			gone = append(gone, name)
		}
	}
	if len(gone) > 0 {
		sort.Strings(gone)
		srv.RemoveTools(gone...)
		for _, name := range gone {
			delete(known, name)
		}
	}
	return nil
}

// forwardToGateway 返回把调用原样透传到网关的 handler（含 structuredContent / isError）。
func forwardToGateway(sess *mcp.ClientSession, toolName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args any
		if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
			args = req.Params.Arguments
		}
		return sess.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
	}
}
