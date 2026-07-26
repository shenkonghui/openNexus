package gatewaymcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opennexus/internal/database"
	"opennexus/internal/repository"
)

// startUpstream 启动一个本地 streamable HTTP MCP server，暴露一个 echo 工具，
// 其返回内容带 tag 前缀，便于断言"调用确实打到了这个上游"。
func startUpstream(t *testing.T, tag string) (string, func()) {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: tag, Version: "1.0.0"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo back"},
			func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
				return nil, echoOut{Reply: tag + ":" + in.Msg}, nil
			})
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	return ts.URL, ts.Close
}

type echoIn struct {
	Msg string `json:"msg"`
}

type echoOut struct {
	Reply string `json:"reply"`
}

// writeMCPConfig 把 mcpServers 配置写入临时目录，返回文件路径。
func writeMCPConfig(t *testing.T, servers map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	data, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// emptySettingsRepo 返回一个没有任何 MCP token 的设置仓库。
func emptySettingsRepo(t *testing.T) *repository.NoteSettingsRepository {
	t.Helper()
	db, err := database.Connect("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("DELETE FROM note_settings")
	return repository.NewNoteSettingsRepository(db)
}

// setupGateway 建库、生成 token 并启动网关 HTTP 服务，返回 endpoint 与 token。
func setupGateway(t *testing.T, configPath string) (string, string, *Gateway) {
	t.Helper()
	settingsRepo := emptySettingsRepo(t)
	const token = "gw-test-token"
	if err := settingsRepo.SetMCPTokenOnce(1, token); err != nil {
		t.Fatal(err)
	}

	gw, err := New(configPath, NewDBAuthenticator(settingsRepo), filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(func() {
		ts.Close()
		gw.Close()
	})
	return ts.URL, token, gw
}

// connectGateway 用 MCP 客户端连上网关（带 Bearer）。
func connectGateway(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "gw-test-client", Version: "1.0.0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: token}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

type bearerRoundTripper struct {
	token string
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func TestGateway_Unauthorized(t *testing.T) {
	endpoint, _, _ := setupGateway(t, writeMCPConfig(t, map[string]any{}))
	req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d, 期望 401", resp.StatusCode)
	}
}

func TestGateway_AggregateAndForward(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()
	betaURL, closeBeta := startUpstream(t, "beta")
	defer closeBeta()

	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
		"beta":  map[string]any{"type": "http", "url": betaURL},
		// stdio 第一阶段不接管（cwd 语义依赖会话），应被跳过而不是报错
		"local-fs": map[string]any{"command": "echo", "args": []string{"hi"}},
		// 自引用条目必须跳过，否则会死循环
		GatewayMCPName: map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp"},
	})
	endpoint, token, _ := setupGateway(t, configPath)
	sess := connectGateway(t, endpoint, token)

	names := listToolNames(t, sess)
	want := []string{"alpha__echo", "beta__echo"}
	if len(names) != len(want) {
		t.Fatalf("工具列表 = %v, 期望 %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("工具列表 = %v, 期望 %v", names, want)
		}
	}

	// 转发到指定上游：返回值带 tag 前缀，可验证路由正确
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "beta__echo",
		Arguments: map[string]any{"msg": "hello"},
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if got := structuredReply(t, res); got != "beta:hello" {
		t.Fatalf("reply = %q, 期望 beta:hello", got)
	}
}

func TestGateway_ConfigChangeRebuilds(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()
	betaURL, closeBeta := startUpstream(t, "beta")
	defer closeBeta()

	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
	})
	endpoint, token, _ := setupGateway(t, configPath)

	sess := connectGateway(t, endpoint, token)
	if names := listToolNames(t, sess); len(names) != 1 || names[0] != "alpha__echo" {
		t.Fatalf("初始工具列表 = %v", names)
	}

	// 追加一个上游后，无需重启即可生效（mtime/size 变化触发重建）
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
		"beta":  map[string]any{"type": "http", "url": betaURL},
	}})
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	sess2 := connectGateway(t, endpoint, token)
	if names := listToolNames(t, sess2); len(names) != 2 {
		t.Fatalf("重载后工具列表 = %v, 期望 2 个", names)
	}
}

func TestGateway_UpstreamDownDoesNotBreakOthers(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()

	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
		"dead":  map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp"},
	})
	endpoint, token, _ := setupGateway(t, configPath)
	sess := connectGateway(t, endpoint, token)

	names := listToolNames(t, sess)
	if len(names) != 1 || names[0] != "alpha__echo" {
		t.Fatalf("工具列表 = %v, 期望仅 alpha__echo", names)
	}
}

func listToolNames(t *testing.T, sess *mcp.ClientSession) []string {
	t.Helper()
	res, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func structuredReply(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("上游返回错误: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out echoOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 structuredContent 失败: %v (%s)", err, raw)
	}
	if out.Reply == "" {
		t.Fatalf("structuredContent 为空: %s", raw)
	}
	return out.Reply
}
