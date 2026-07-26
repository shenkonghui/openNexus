package acp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 探测超时：stdio 起子进程较慢，http/sse 较快。
const (
	mcpProbeStdioTimeout = 15 * time.Second
	mcpProbeHTTPTimeout  = 8 * time.Second
	mcpProbeSSETimeout   = 8 * time.Second
)

// MCPToolInfo 是探测到的单个 MCP 工具信息。
type MCPToolInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"` // 优先展示
	Description string `json:"description,omitempty"`
}

// MCPServerStatus 是单个 MCP server 的探测结果。
type MCPServerStatus struct {
	Name       string        `json:"name"`
	Type       string        `json:"type"` // stdio | http | sse
	Connected  bool          `json:"connected"`
	Error      string        `json:"error,omitempty"`
	ServerInfo string        `json:"server_info,omitempty"` // InitializeResult.ServerInfo.Name
	Tools      []MCPToolInfo `json:"tools"`
}

// ProbeMCPServers 并发探测给定的 MCP server 列表，返回每个 server 的连接状态与工具列表。
//
// 每个 server 使用独立的 context 超时（stdio 15s，http/sse 8s），
// 互不阻塞。探测后立即关闭连接（stdio 子进程会被回收）。
// 结果顺序与输入 entries 顺序一致。
func ProbeMCPServers(ctx context.Context, entries []NamedMCPServerEntry) []MCPServerStatus {
	if len(entries) == 0 {
		return nil
	}
	results := make([]MCPServerStatus, len(entries))
	var wg sync.WaitGroup
	for i, ne := range entries {
		wg.Add(1)
		go func(idx int, named NamedMCPServerEntry) {
			defer wg.Done()
			results[idx] = probeOne(ctx, named)
		}(i, ne)
	}
	wg.Wait()
	return results
}

// probeOne 探测单个 MCP server。
func probeOne(ctx context.Context, named NamedMCPServerEntry) MCPServerStatus {
	entry := named.Entry
	status := MCPServerStatus{Name: named.Name, Type: normalizedType(entry.Type)}

	// 配置非法（缺 command/url 或未知 type），直接标记失败，不发起探测。
	if err := validateEntry(named.Name, entry); err != nil {
		status.Error = err.Error()
		return status
	}

	timeout := probeTimeout(entry.Type)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport, err := buildTransport(entry)
	if err != nil {
		status.Error = fmt.Sprintf("构造传输失败: %v", err)
		return status
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "opennexus-mcp-probe", Version: "v1.0.0"}, nil)
	session, err := client.Connect(pctx, transport, nil)
	if err != nil {
		status.Error = fmt.Sprintf("连接失败: %v", err)
		return status
	}
	defer session.Close()

	status.Connected = true
	if res := session.InitializeResult(); res != nil && res.ServerInfo != nil {
		status.ServerInfo = res.ServerInfo.Name
	}

	// 用迭代器收集所有工具（自动处理分页）。
	var tools []MCPToolInfo
	for tool, err := range session.Tools(pctx, nil) {
		if err != nil {
			// 已连接但列举工具失败，保留已收集的部分，不覆盖 connected 状态。
			break
		}
		if tool == nil {
			continue
		}
		info := MCPToolInfo{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
		}
		tools = append(tools, info)
	}
	status.Tools = tools
	return status
}

// validateEntry 校验 entry 必需字段。
func validateEntry(name string, e MCPServerEntry) error {
	switch e.Type {
	case "", MCPTypeStdio:
		if e.Command == "" {
			return fmt.Errorf("stdio 类型缺少 command")
		}
	case MCPTypeHTTP, MCPTypeSSE:
		if e.Url == "" {
			return fmt.Errorf("%s 类型缺少 url", e.Type)
		}
	default:
		return fmt.Errorf("未知的 type %q", e.Type)
	}
	return nil
}

// normalizedType 返回对外展示的规范 type（空串归一为 stdio）。
func normalizedType(t string) string {
	if t == "" {
		return MCPTypeStdio
	}
	return t
}

// probeTimeout 按传输类型返回探测超时。
func probeTimeout(typ string) time.Duration {
	switch typ {
	case "", MCPTypeStdio:
		return mcpProbeStdioTimeout
	default:
		return mcpProbeHTTPTimeout // http 与 sse 共用
	}
}

// BuildMCPTransport 按 entry 的 type 构造对应的 go-sdk Transport，供包外复用（如 MCP 聚合网关）。
// 与探测共用同一套构造逻辑，保证网关连上游的行为与设置页"测试连接"完全一致。
func BuildMCPTransport(e MCPServerEntry) (mcp.Transport, error) {
	return buildTransport(e)
}

// buildTransport 按 entry 的 type 构造对应的 go-sdk Transport。
func buildTransport(e MCPServerEntry) (mcp.Transport, error) {
	switch e.Type {
	case "", MCPTypeStdio:
		cmd := exec.Command(e.Command, e.Args...)
		cmd.Env = mergeEnv(e.Env)
		return &mcp.CommandTransport{Command: cmd}, nil
	case MCPTypeHTTP:
		return &mcp.StreamableClientTransport{
			Endpoint:             e.Url,
			HTTPClient:           headerClient(e.Headers),
			DisableStandaloneSSE: true, // 探测无需持久 SSE 流
		}, nil
	case MCPTypeSSE:
		return &mcp.SSEClientTransport{
			Endpoint:   e.Url,
			HTTPClient: headerClient(e.Headers),
		}, nil
	}
	return nil, fmt.Errorf("未知的 type %q", e.Type)
}

// mergeEnv 将配置的 env 追加到当前进程环境。返回 nil 时子进程继承父进程环境。
// 注意：此处不处理同名变量覆盖，由子进程按环境列表顺序解析。
func mergeEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	base := os.Environ()
	for k, v := range env {
		base = append(base, k+"="+v)
	}
	return base
}

// headerClient 返回注入了自定义 headers 的 HTTP 客户端；无 headers 时返回默认客户端。
func headerClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &headerRoundTripper{
			headers: headers,
			base:    http.DefaultTransport,
		},
	}
}

// headerRoundTripper 在每条请求上附加配置的 headers。
type headerRoundTripper struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// 克隆请求避免修改共享对象。
	clone := req.Clone(req.Context())
	for k, v := range h.headers {
		clone.Header.Set(k, v)
	}
	return h.base.RoundTrip(clone)
}
