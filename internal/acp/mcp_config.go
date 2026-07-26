package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/coder/acp-go-sdk"
)

// mcpServersFile 是标准 mcpServers JSON 格式的文件结构（Claude Desktop / ZCode 规范）。
type mcpServersFile struct {
	McpServers map[string]MCPServerEntry `json:"mcpServers"`
}

// MCPServerEntry 是单个 MCP server 的原始配置。不同传输类型字段不同，全部可选解析。
//   - stdio（默认或 type=="stdio"）：command / args / env
//   - http（type=="http"）：url / headers
//   - sse（type=="sse"）：url / headers
type MCPServerEntry struct {
	Type    string            `json:"type"`              // stdio | http | sse；空则按 stdio 处理
	Command string            `json:"command,omitempty"` // stdio
	Args    []string          `json:"args,omitempty"`    // stdio
	Env     map[string]string `json:"env,omitempty"`     // stdio
	Url     string            `json:"url,omitempty"`     // http / sse
	Headers map[string]string `json:"headers,omitempty"` // http / sse
}

// NamedMCPServerEntry 是带名称（mcpServers map 的 key）的 server 配置。
type NamedMCPServerEntry struct {
	Name  string
	Entry MCPServerEntry
}

// MCP 传输类型常量。
const (
	MCPTypeStdio = "stdio"
	MCPTypeHTTP  = "http"
	MCPTypeSSE   = "sse"
)

// LoadMCPServerEntries 从 path 读取并解析标准 mcpServers 格式的配置文件，
// 返回带名称的原始 server 配置列表（按 name 字典序排序）。
//
// 容错策略：
//   - 文件不存在：返回 (nil, nil)（不视为错误）
//   - 整体 JSON 非法：返回错误
//
// 注意：此函数返回所有配置项（含缺少 command/url 等无效项），由调用方按需校验。
func LoadMCPServerEntries(path string) ([]NamedMCPServerEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 MCP 配置文件: %w", err)
	}

	var file mcpServersFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析 MCP 配置文件: %w", err)
	}
	if len(file.McpServers) == 0 {
		return nil, nil
	}

	// 按 name 排序，保证顺序稳定。
	names := make([]string, 0, len(file.McpServers))
	for name := range file.McpServers {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]NamedMCPServerEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, NamedMCPServerEntry{Name: name, Entry: file.McpServers[name]})
	}
	return entries, nil
}

// LoadMCPServers 从 path 读取并解析标准 mcpServers 格式的配置文件，
// 转换为 ACP NewSession 所需的 []acp.McpServer。
//
// 容错策略：
//   - 文件不存在：返回 (nil, nil)（不视为错误）
//   - 单条 server 配置非法：跳过并 slog.Warn，不整体失败
//   - 整体 JSON 非法：返回错误
//
// 返回顺序按 server name 字典序，保证稳定。
func LoadMCPServers(path string) ([]acp.McpServer, error) {
	entries, err := LoadMCPServerEntries(path)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	return ConvertMCPServers(entries), nil
}

// ConvertMCPServers 把带名称的原始配置批量转换为 ACP server 列表，跳过非法项。
func ConvertMCPServers(entries []NamedMCPServerEntry) []acp.McpServer {
	servers := make([]acp.McpServer, 0, len(entries))
	for _, ne := range entries {
		server, ok := convertMcpServer(ne.Name, ne.Entry)
		if !ok {
			continue
		}
		servers = append(servers, server)
	}
	if len(servers) == 0 {
		return nil
	}
	return servers
}

// GatewayMCPName 是 MCP 聚合网关在 mcp.json 中的条目名。
// 定义在此处（而非 gateway 包）以便 acp / handlers 引用时不产生循环依赖。
const GatewayMCPName = "opennexus-gateway"

// GatewayCovers 判断某条配置是否由聚合网关代理。
// 网关只接管 http/sse：stdio server 的工作目录语义依赖 agent 会话 cwd，
// 交给常驻网关代管会改变行为，因此仍走 session/new 原路注入。
func GatewayCovers(e MCPServerEntry) bool {
	switch e.Type {
	case MCPTypeHTTP, MCPTypeSSE:
		return e.Url != ""
	default:
		return false
	}
}

// HasGatewayEntry 判断配置中是否已存在聚合网关条目。
func HasGatewayEntry(entries []NamedMCPServerEntry) bool {
	for _, ne := range entries {
		if ne.Name == GatewayMCPName {
			return true
		}
	}
	return false
}

// CollapseViaGateway 在聚合网关已启用时收敛注入列表：
// 被网关代理的 http/sse 上游不再单独注入，只保留网关本身与网关不接管的条目（stdio 等），
// 避免同一批工具通过两条路径重复暴露给 agent。
//
// 配置中没有网关条目时原样返回。
func CollapseViaGateway(entries []NamedMCPServerEntry) []NamedMCPServerEntry {
	if !HasGatewayEntry(entries) {
		return entries
	}
	out := make([]NamedMCPServerEntry, 0, len(entries))
	for _, ne := range entries {
		if ne.Name != GatewayMCPName && GatewayCovers(ne.Entry) {
			continue
		}
		out = append(out, ne)
	}
	return out
}

// gatewayHTTPEntry 构造聚合网关的 http 形态条目（主程序默认注入形态）。
func gatewayHTTPEntry(endpoint, token string) NamedMCPServerEntry {
	return NamedMCPServerEntry{
		Name: GatewayMCPName,
		Entry: MCPServerEntry{
			Type:    MCPTypeHTTP,
			Url:     endpoint,
			Headers: map[string]string{"Authorization": "Bearer " + token},
		},
	}
}

// collapseWithGatewayEntry 与 CollapseViaGateway 行为一致，但不依赖 mcp.json 已有网关条目：
// 用传入的 gwEntry（http 或 stdio 桥形态）作为网关条目注入，同时收敛被网关代理的 http/sse 上游。
// 用于主程序"默认启用网关"路径——无需用户手动启用 mcp.json 中的网关条目。
// 若 entries 中已存在同名网关条目，用传入的 gwEntry 覆盖（以主程序运行时地址为准）。
func collapseWithGatewayEntry(entries []NamedMCPServerEntry, gwEntry NamedMCPServerEntry) []NamedMCPServerEntry {
	out := make([]NamedMCPServerEntry, 0, len(entries)+1)
	out = append(out, gwEntry)
	for _, ne := range entries {
		if ne.Name == GatewayMCPName {
			continue // 用主程序运行时地址覆盖 mcp.json 中的旧条目
		}
		if GatewayCovers(ne.Entry) {
			continue // 被网关代理的 http/sse 上游收敛掉
		}
		out = append(out, ne)
	}
	return out
}

// filterByMcpCapabilities 按 agent 握手声明的 MCP 传输能力过滤注入列表：
// 不支持的传输类型直接丢弃并告警，避免 session/new 注入被 agent 静默忽略甚至报错。
// stdio 是 ACP 基线能力，始终保留。
func filterByMcpCapabilities(servers []acp.McpServer, caps acp.McpCapabilities) []acp.McpServer {
	if caps.Http && caps.Sse {
		return servers
	}
	out := make([]acp.McpServer, 0, len(servers))
	for _, sv := range servers {
		switch {
		case sv.Http != nil && !caps.Http:
			slog.Warn("跳过 MCP server 注入：agent 不支持 http 传输", "name", sv.Http.Name)
		case sv.Sse != nil && !caps.Sse:
			slog.Warn("跳过 MCP server 注入：agent 不支持 sse 传输", "name", sv.Sse.Name)
		default:
			out = append(out, sv)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// convertMcpServer 将单条配置转换为 acp.McpServer。配置非法时返回 (zero, false) 并记日志。
func convertMcpServer(name string, e MCPServerEntry) (acp.McpServer, bool) {
	switch e.Type {
	case "", MCPTypeStdio:
		if e.Command == "" {
			slog.Warn("跳过 MCP server：stdio 类型缺少 command", "name", name)
			return acp.McpServer{}, false
		}
		return acp.McpServer{
			Stdio: &acp.McpServerStdio{
				Name:    name,
				Command: e.Command,
				Args:    e.Args,
				Env:     toEnvVariables(e.Env),
			},
		}, true
	case MCPTypeHTTP:
		if e.Url == "" {
			slog.Warn("跳过 MCP server：http 类型缺少 url", "name", name)
			return acp.McpServer{}, false
		}
		return acp.McpServer{
			Http: &acp.McpServerHttpInline{
				Name:    name,
				Type:    MCPTypeHTTP,
				Url:     e.Url,
				Headers: toHttpHeaders(e.Headers),
			},
		}, true
	case MCPTypeSSE:
		if e.Url == "" {
			slog.Warn("跳过 MCP server：sse 类型缺少 url", "name", name)
			return acp.McpServer{}, false
		}
		return acp.McpServer{
			Sse: &acp.McpServerSseInline{
				Name:    name,
				Type:    MCPTypeSSE,
				Url:     e.Url,
				Headers: toHttpHeaders(e.Headers),
			},
		}, true
	default:
		slog.Warn("跳过 MCP server：未知的 type", "name", name, "type", e.Type)
		return acp.McpServer{}, false
	}
}

// toEnvVariables 将 map 转换为按 name 排序的 []EnvVariable（顺序稳定）。
func toEnvVariables(env map[string]string) []acp.EnvVariable {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]acp.EnvVariable, 0, len(keys))
	for _, k := range keys {
		out = append(out, acp.EnvVariable{Name: k, Value: env[k]})
	}
	return out
}

// toHttpHeaders 将 map 转换为按 name 排序的 []HttpHeader（顺序稳定）。
func toHttpHeaders(headers map[string]string) []acp.HttpHeader {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]acp.HttpHeader, 0, len(keys))
	for _, k := range keys {
		out = append(out, acp.HttpHeader{Name: k, Value: headers[k]})
	}
	return out
}

// CountMCPServers 解析 path 并返回其中配置的 server 数量（含被跳过的非法项）。
// 文件不存在或解析失败返回 0。
func CountMCPServers(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var file mcpServersFile
	if err := json.Unmarshal(data, &file); err != nil {
		return 0
	}
	return len(file.McpServers)
}

// UpsertMCPServerEntry 向 path 的 mcpServers 中插入或更新名为 name 的 server 条目，
// 保留其它已有条目与格式（2 空格缩进）。文件或父目录不存在时自动创建。
//
// 容错策略：
//   - 文件不存在：按空配置起步
//   - 文件内容非法 JSON：返回错误，避免覆盖用户数据
func UpsertMCPServerEntry(path, name string, entry MCPServerEntry) error {
	file := mcpServersFile{McpServers: map[string]MCPServerEntry{}}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("解析现有 MCP 配置文件失败，已中止写入以保护数据: %w", err)
		}
	}
	if file.McpServers == nil {
		file.McpServers = map[string]MCPServerEntry{}
	}
	file.McpServers[name] = entry

	return writeMCPServersFile(path, file)
}

// RemoveMCPServerEntry 从 path 的 mcpServers 中删除名为 name 的条目，保留其它条目。
// 文件不存在或条目本就不存在时视为成功（幂等）。
func RemoveMCPServerEntry(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取 MCP 配置文件: %w", err)
	}
	var file mcpServersFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("解析现有 MCP 配置文件失败，已中止写入以保护数据: %w", err)
	}
	if _, ok := file.McpServers[name]; !ok {
		return nil
	}
	delete(file.McpServers, name)
	return writeMCPServersFile(path, file)
}

// writeMCPServersFile 以 2 空格缩进写回配置文件，父目录不存在时自动创建。
func writeMCPServersFile(path string, file mcpServersFile) error {
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 MCP 配置失败: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建 MCP 配置目录失败: %w", err)
		}
	}
	return os.WriteFile(path, out, 0o644)
}
