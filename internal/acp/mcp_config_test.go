package acp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk"
)

func writeTempMCPConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入临时 MCP 配置失败: %v", err)
	}
	return path
}

func TestLoadMCPServers_FileNotExist(t *testing.T) {
	servers, err := LoadMCPServers(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("文件不存在应返回 nil,nil，实际错误: %v", err)
	}
	if servers != nil {
		t.Errorf("文件不存在应返回 nil，实际 %v", servers)
	}
}

func TestLoadMCPServers_EmptyFile(t *testing.T) {
	path := writeTempMCPConfig(t, `{}`)
	servers, err := LoadMCPServers(path)
	if err != nil {
		t.Fatalf("空配置应返回 nil,nil，实际错误: %v", err)
	}
	if servers != nil {
		t.Errorf("空配置应返回 nil，实际 %v", servers)
	}
}

func TestLoadMCPServers_InvalidJSON(t *testing.T) {
	path := writeTempMCPConfig(t, `{not valid json`)
	if _, err := LoadMCPServers(path); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}

func TestLoadMCPServers_Stdio(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": {
    "stdio-server": {
      "command": "npx",
      "args": ["-y", "@some/mcp-server"],
      "env": { "API_KEY": "secret", "DEBUG": "1" }
    }
  }
}`)
	servers, err := LoadMCPServers(path)
	if err != nil {
		t.Fatalf("LoadMCPServers 错误: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("期望 1 个 server，实际 %d", len(servers))
	}
	s := servers[0]
	if s.Stdio == nil {
		t.Fatal("期望 Stdio 传输")
	}
	if s.Stdio.Name != "stdio-server" {
		t.Errorf("Name = %q", s.Stdio.Name)
	}
	if s.Stdio.Command != "npx" {
		t.Errorf("Command = %q", s.Stdio.Command)
	}
	if len(s.Stdio.Args) != 2 || s.Stdio.Args[1] != "@some/mcp-server" {
		t.Errorf("Args = %v", s.Stdio.Args)
	}
	// env 应按 name 排序：API_KEY 在 DEBUG 之前
	if len(s.Stdio.Env) != 2 {
		t.Fatalf("Env = %v", s.Stdio.Env)
	}
	if s.Stdio.Env[0].Name != "API_KEY" || s.Stdio.Env[0].Value != "secret" {
		t.Errorf("Env[0] = %+v", s.Stdio.Env[0])
	}
	if s.Stdio.Env[1].Name != "DEBUG" || s.Stdio.Env[1].Value != "1" {
		t.Errorf("Env[1] = %+v", s.Stdio.Env[1])
	}
}

func TestLoadMCPServers_StdioExplicitType(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": {
    "explicit": { "type": "stdio", "command": "node", "args": ["server.js"] }
  }
}`)
	servers, err := LoadMCPServers(path)
	if err != nil {
		t.Fatalf("LoadMCPServers 错误: %v", err)
	}
	if len(servers) != 1 || servers[0].Stdio == nil {
		t.Fatalf("期望 1 个 stdio server，实际 %v", servers)
	}
}

func TestLoadMCPServers_Http(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": {
    "http-server": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer token", "X-Custom": "v" }
    }
  }
}`)
	servers, err := LoadMCPServers(path)
	if err != nil {
		t.Fatalf("LoadMCPServers 错误: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("期望 1 个 server，实际 %d", len(servers))
	}
	s := servers[0]
	if s.Http == nil {
		t.Fatal("期望 Http 传输")
	}
	if s.Http.Name != "http-server" {
		t.Errorf("Name = %q", s.Http.Name)
	}
	if s.Http.Type != "http" {
		t.Errorf("Type = %q", s.Http.Type)
	}
	if s.Http.Url != "https://example.com/mcp" {
		t.Errorf("Url = %q", s.Http.Url)
	}
	// headers 按 name 排序：Authorization 在 X-Custom 之前
	if len(s.Http.Headers) != 2 {
		t.Fatalf("Headers = %v", s.Http.Headers)
	}
	if s.Http.Headers[0].Name != "Authorization" || s.Http.Headers[0].Value != "Bearer token" {
		t.Errorf("Headers[0] = %+v", s.Http.Headers[0])
	}
	if s.Http.Headers[1].Name != "X-Custom" || s.Http.Headers[1].Value != "v" {
		t.Errorf("Headers[1] = %+v", s.Http.Headers[1])
	}
}

func TestLoadMCPServers_SSE(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": {
    "sse-server": { "type": "sse", "url": "https://example.com/sse" }
  }
}`)
	servers, err := LoadMCPServers(path)
	if err != nil {
		t.Fatalf("LoadMCPServers 错误: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("期望 1 个 server，实际 %d", len(servers))
	}
	if servers[0].Sse == nil {
		t.Fatal("期望 Sse 传输")
	}
	if servers[0].Sse.Url != "https://example.com/sse" {
		t.Errorf("Url = %q", servers[0].Sse.Url)
	}
}

func TestLoadMCPServers_MixedAndSorted(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": {
    "zeta": { "command": "z" },
    "alpha": { "command": "a" },
    "mid-http": { "type": "http", "url": "http://h" }
  }
}`)
	servers, err := LoadMCPServers(path)
	if err != nil {
		t.Fatalf("LoadMCPServers 错误: %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("期望 3 个 server，实际 %d", len(servers))
	}
	// 按 name 字典序：alpha < mid-http < zeta
	if got := serverName(servers[0]); got != "alpha" {
		t.Errorf("servers[0] name = %q, 期望 alpha", got)
	}
	if got := serverName(servers[1]); got != "mid-http" {
		t.Errorf("servers[1] name = %q, 期望 mid-http", got)
	}
	if got := serverName(servers[2]); got != "zeta" {
		t.Errorf("servers[2] name = %q, 期望 zeta", got)
	}
}

func TestLoadMCPServers_SkipsInvalid(t *testing.T) {
	// stdio 缺 command 跳过；http 缺 url 跳过；未知 type 跳过；有效项保留
	path := writeTempMCPConfig(t, `{
  "mcpServers": {
    "bad-stdio": { "command": "" },
    "bad-http": { "type": "http", "url": "" },
    "bad-type": { "type": "weird" },
    "good": { "command": "ok" }
  }
}`)
	servers, err := LoadMCPServers(path)
	if err != nil {
		t.Fatalf("LoadMCPServers 错误: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("期望 1 个有效 server（good），实际 %d", len(servers))
	}
	if got := serverName(servers[0]); got != "good" {
		t.Errorf("有效 server name = %q, 期望 good", got)
	}
}

func TestCountMCPServers(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": { "a": { "command": "a" }, "b": { "type": "http", "url": "http://b" } }
}`)
	if got := CountMCPServers(path); got != 2 {
		t.Errorf("CountMCPServers = %d, 期望 2", got)
	}
	// 不存在返回 0
	if got := CountMCPServers(filepath.Join(t.TempDir(), "nope.json")); got != 0 {
		t.Errorf("CountMCPServers 不存在文件 = %d, 期望 0", got)
	}
}

func TestUpsertMCPServerEntry_NewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp.json")
	entry := MCPServerEntry{Type: MCPTypeHTTP, Url: "http://x/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}
	if err := UpsertMCPServerEntry(path, "foo", entry); err != nil {
		t.Fatalf("UpsertMCPServerEntry 错误: %v", err)
	}
	// 文件应被创建（含父目录）
	entries, err := LoadMCPServerEntries(path)
	if err != nil {
		t.Fatalf("LoadMCPServerEntries 错误: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "foo" {
		t.Fatalf("期望 1 个 foo 条目，实际 %+v", entries)
	}
	if entries[0].Entry.Url != "http://x/mcp" {
		t.Errorf("Url = %q", entries[0].Entry.Url)
	}
}

func TestUpsertMCPServerEntry_PreservesExisting(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": { "existing": { "command": "node", "args": ["s.js"] } }
}`)
	// 插入新条目，existing 应保留
	if err := UpsertMCPServerEntry(path, "new", MCPServerEntry{Command: "echo"}); err != nil {
		t.Fatalf("Upsert 错误: %v", err)
	}
	entries, err := LoadMCPServerEntries(path)
	if err != nil {
		t.Fatalf("Load 错误: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("期望 2 个条目，实际 %d", len(entries))
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["existing"] || !names["new"] {
		t.Errorf("缺少条目: %+v", names)
	}
}

func TestUpsertMCPServerEntry_OverwriteSameName(t *testing.T) {
	path := writeTempMCPConfig(t, `{
  "mcpServers": { "foo": { "command": "old" } }
}`)
	// 同名条目应被覆盖
	if err := UpsertMCPServerEntry(path, "foo", MCPServerEntry{Command: "new-cmd"}); err != nil {
		t.Fatalf("Upsert 错误: %v", err)
	}
	entries, err := LoadMCPServerEntries(path)
	if err != nil {
		t.Fatalf("Load 错误: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("期望 1 个条目，实际 %d", len(entries))
	}
	if entries[0].Entry.Command != "new-cmd" {
		t.Errorf("Command = %q, 期望 new-cmd", entries[0].Entry.Command)
	}
}

func TestUpsertMCPServerEntry_InvalidJSON(t *testing.T) {
	path := writeTempMCPConfig(t, `{not valid json`)
	// 现有文件非法 JSON 时应返回错误，保护数据不被覆盖
	if err := UpsertMCPServerEntry(path, "foo", MCPServerEntry{Command: "x"}); err == nil {
		t.Error("期望非法 JSON 时返回错误")
	}
}

func TestRemoveMCPServerEntry(t *testing.T) {
	path := writeTempMCPConfig(t, `{"mcpServers":{"a":{"command":"x"},"b":{"type":"http","url":"http://h/mcp"}}}`)
	if err := RemoveMCPServerEntry(path, "b"); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadMCPServerEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "a" {
		t.Fatalf("剩余条目 = %+v, 期望仅 a", entries)
	}
	// 幂等：再删一次不报错
	if err := RemoveMCPServerEntry(path, "b"); err != nil {
		t.Fatalf("重复删除应幂等: %v", err)
	}
	// 文件不存在也不报错
	if err := RemoveMCPServerEntry(filepath.Join(t.TempDir(), "missing.json"), "b"); err != nil {
		t.Fatalf("文件不存在应视为成功: %v", err)
	}
}

func TestCollapseViaGateway(t *testing.T) {
	entries := []NamedMCPServerEntry{
		{Name: "local-fs", Entry: MCPServerEntry{Command: "fs-server"}},
		{Name: "remote-a", Entry: MCPServerEntry{Type: MCPTypeHTTP, Url: "http://a/mcp"}},
		{Name: "remote-b", Entry: MCPServerEntry{Type: MCPTypeSSE, Url: "http://b/sse"}},
	}

	// 无网关条目：原样返回
	if got := CollapseViaGateway(entries); len(got) != 3 {
		t.Fatalf("无网关时应原样返回，实际 %d 条", len(got))
	}

	// 有网关条目：http/sse 收敛掉，只留网关 + stdio
	withGW := append(entries, NamedMCPServerEntry{
		Name:  GatewayMCPName,
		Entry: MCPServerEntry{Type: MCPTypeHTTP, Url: "http://gw/mcp/gateway"},
	})
	got := CollapseViaGateway(withGW)
	names := make([]string, 0, len(got))
	for _, ne := range got {
		names = append(names, ne.Name)
	}
	if len(names) != 2 || names[0] != "local-fs" || names[1] != GatewayMCPName {
		t.Fatalf("收敛结果 = %v, 期望 [local-fs %s]", names, GatewayMCPName)
	}
}

func TestCollapseWithGatewayEntry(t *testing.T) {
	entries := []NamedMCPServerEntry{
		{Name: "local-fs", Entry: MCPServerEntry{Command: "fs-server"}},
		{Name: "remote-a", Entry: MCPServerEntry{Type: MCPTypeHTTP, Url: "http://a/mcp"}},
		{Name: "remote-b", Entry: MCPServerEntry{Type: MCPTypeSSE, Url: "http://b/sse"}},
	}

	// 默认启用：不依赖 mcp.json 已有网关条目，直接注入 endpoint+token 的 http 形态条目
	got := collapseWithGatewayEntry(entries, gatewayHTTPEntry("http://gw:8090/mcp/gateway", "tok"))
	names := make([]string, 0, len(got))
	for _, ne := range got {
		names = append(names, ne.Name)
	}
	// 期望：网关 + local-fs（stdio 不收敛）；remote-a/remote-b 被收敛
	if len(names) != 2 {
		t.Fatalf("收敛结果 = %v, 期望 2 条", names)
	}
	// 网关条目应在列表中，且 endpoint 为传入值
	var gwEntry *NamedMCPServerEntry
	for i := range got {
		if got[i].Name == GatewayMCPName {
			gwEntry = &got[i]
		}
	}
	if gwEntry == nil {
		t.Fatalf("收敛结果缺网关条目: %v", names)
	}
	if gwEntry.Entry.Url != "http://gw:8090/mcp/gateway" {
		t.Errorf("网关 url = %q", gwEntry.Entry.Url)
	}
	if gwEntry.Entry.Headers["Authorization"] != "Bearer tok" {
		t.Errorf("网关 Authorization = %q", gwEntry.Entry.Headers["Authorization"])
	}

	// mcp.json 中已有旧网关条目时，用传入 endpoint 覆盖
	withOld := append(entries, NamedMCPServerEntry{
		Name:  GatewayMCPName,
		Entry: MCPServerEntry{Type: MCPTypeHTTP, Url: "http://old-host/mcp/gateway"},
	})
	got2 := collapseWithGatewayEntry(withOld, gatewayHTTPEntry("http://gw:8090/mcp/gateway", "tok"))
	for _, ne := range got2 {
		if ne.Name == GatewayMCPName && ne.Entry.Url != "http://gw:8090/mcp/gateway" {
			t.Errorf("旧网关条目未被覆盖: %q", ne.Entry.Url)
		}
	}

	// stdio 桥形态的网关条目：http/sse 上游同样收敛，网关自身为 stdio 条目
	bridge := NamedMCPServerEntry{
		Name: GatewayMCPName,
		Entry: MCPServerEntry{
			Type:    MCPTypeStdio,
			Command: "/usr/local/bin/opennexus",
			Args:    []string{"mcp-bridge"},
			Env:     map[string]string{"OPENNEXUS_GATEWAY_URL": "http://gw:8090/mcp/gateway", "OPENNEXUS_GATEWAY_TOKEN": "tok"},
		},
	}
	got3 := collapseWithGatewayEntry(entries, bridge)
	if len(got3) != 2 || got3[0].Name != GatewayMCPName || got3[0].Entry.Command != "/usr/local/bin/opennexus" {
		t.Fatalf("stdio 桥形态收敛结果不符: %+v", got3)
	}
}

func TestFilterByMcpCapabilities(t *testing.T) {
	servers := []acp.McpServer{
		{Stdio: &acp.McpServerStdio{Name: "local-fs", Command: "fs-server"}},
		{Http: &acp.McpServerHttpInline{Name: "remote-http", Type: MCPTypeHTTP, Url: "http://a/mcp"}},
		{Sse: &acp.McpServerSseInline{Name: "remote-sse", Type: MCPTypeSSE, Url: "http://b/sse"}},
	}

	// 全部支持：原样返回
	if got := filterByMcpCapabilities(servers, acp.McpCapabilities{Http: true, Sse: true}); len(got) != 3 {
		t.Fatalf("全能力时应原样返回，实际 %d 条", len(got))
	}

	// http:false, sse:false（如 devin）：只剩 stdio
	got := filterByMcpCapabilities(servers, acp.McpCapabilities{})
	if len(got) != 1 || got[0].Stdio == nil || got[0].Stdio.Name != "local-fs" {
		t.Fatalf("无 http/sse 能力时应只剩 stdio，实际 %+v", got)
	}

	// 仅 http:true：sse 被过滤
	got = filterByMcpCapabilities(servers, acp.McpCapabilities{Http: true})
	if len(got) != 2 {
		t.Fatalf("仅 http 能力时应剩 2 条，实际 %d 条", len(got))
	}
	for _, sv := range got {
		if sv.Sse != nil {
			t.Errorf("sse 条目未被过滤: %+v", sv)
		}
	}

	// 全被过滤时返回 nil
	onlyHTTP := []acp.McpServer{{Http: &acp.McpServerHttpInline{Name: "h", Type: MCPTypeHTTP, Url: "http://h"}}}
	if got := filterByMcpCapabilities(onlyHTTP, acp.McpCapabilities{}); got != nil {
		t.Fatalf("全被过滤时应返回 nil，实际 %+v", got)
	}
}

// serverName 从 acp.McpServer（tagged union）中提取 name。
func serverName(s acp.McpServer) string {
	switch {
	case s.Stdio != nil:
		return s.Stdio.Name
	case s.Http != nil:
		return s.Http.Name
	case s.Sse != nil:
		return s.Sse.Name
	case s.Acp != nil:
		return s.Acp.Name
	}
	return ""
}
