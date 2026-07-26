package gatewaymcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newUpstreamSession 起一个 in-memory 的假网关 server 并返回连到它的 client session。
func newUpstreamSession(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatalf("连接假网关失败: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-bridge-client", Version: "0"}, nil)
	sess, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func echoTool(name string) (*mcp.Tool, mcp.ToolHandler) {
	return &mcp.Tool{Name: name, Description: "echo", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + name}}}, nil
		}
}

func TestSyncBridgeTools_AddRemoveAndForward(t *testing.T) {
	ctx := context.Background()

	// 假网关：先注册 a、b 两个工具
	gw := mcp.NewServer(&mcp.Implementation{Name: "fake-gateway", Version: "0"}, nil)
	ta, ha := echoTool("srv__a")
	tb, hb := echoTool("srv__b")
	gw.AddTool(ta, ha)
	gw.AddTool(tb, hb)
	sess := newUpstreamSession(t, gw)

	bridge := mcp.NewServer(&mcp.Implementation{Name: GatewayMCPName, Version: "0"}, nil)
	known := map[string]bool{}
	if err := syncBridgeTools(ctx, bridge, sess, known); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	if len(known) != 2 || !known["srv__a"] || !known["srv__b"] {
		t.Fatalf("首次同步后 known = %v, 期望 srv__a/srv__b", known)
	}

	// 下游 client 连桥，应看到同样的工具并可透传调用
	downSess := newUpstreamSession(t, bridge)
	res, err := downSess.CallTool(ctx, &mcp.CallToolParams{Name: "srv__a"})
	if err != nil {
		t.Fatalf("透传调用失败: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("透传结果 Content = %+v", res.Content)
	}
	if txt, ok := res.Content[0].(*mcp.TextContent); !ok || txt.Text != "echo:srv__a" {
		t.Fatalf("透传结果不符: %+v", res.Content[0])
	}

	// 网关侧移除 b：再次同步后 known 收敛
	gw.RemoveTools("srv__b")
	if err := syncBridgeTools(ctx, bridge, sess, known); err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}
	if len(known) != 1 || !known["srv__a"] {
		t.Fatalf("移除后 known = %v, 期望仅 srv__a", known)
	}
}
