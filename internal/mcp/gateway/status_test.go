package gatewaymcp

import (
	"path/filepath"
	"testing"

	"opennexus/internal/acp"
)

func TestGateway_EnableDisableEntry(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()
	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
	})
	_, token, gw := setupGateway(t, configPath)
	gw.SetPublicBaseURL("http://127.0.0.1:8008")

	if gw.Status(t.Context(), 1).Enabled {
		t.Fatal("默认不应启用")
	}

	if err := gw.EnableEntry(); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	entry := gw.findEntry()
	if entry == nil {
		t.Fatal("启用后 mcp.json 应含网关条目")
	}
	if entry.Url != "http://127.0.0.1:8008/mcp/gateway" {
		t.Errorf("endpoint = %q", entry.Url)
	}
	if entry.Headers["Authorization"] != "Bearer "+token {
		t.Errorf("Authorization = %q", entry.Headers["Authorization"])
	}

	// 启用后会话注入应收敛为"仅网关"（alpha 已被网关代理）
	entries, err := acp.LoadMCPServerEntries(configPath)
	if err != nil {
		t.Fatal(err)
	}
	collapsed := acp.CollapseViaGateway(entries)
	if len(collapsed) != 1 || collapsed[0].Name != GatewayMCPName {
		t.Fatalf("收敛结果 = %+v, 期望仅网关", collapsed)
	}

	if err := gw.DisableEntry(); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if gw.findEntry() != nil {
		t.Fatal("停用后不应残留网关条目")
	}
}

func TestGateway_EnableWithoutToken(t *testing.T) {
	configPath := writeMCPConfig(t, map[string]any{})
	gw, err := New(configPath, NewDBAuthenticator(emptySettingsRepo(t)), filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	gw.SetPublicBaseURL("http://127.0.0.1:8008")
	if err := gw.EnableEntry(); err == nil {
		t.Fatal("无 token 时应拒绝启用（写了也连不上）")
	}
}

func TestGateway_SyncEntryOnlyHealsExisting(t *testing.T) {
	configPath := writeMCPConfig(t, map[string]any{})
	_, token, gw := setupGateway(t, configPath)
	gw.SetPublicBaseURL("http://127.0.0.1:8008")

	// 条目不存在：自愈不应主动写入（启用必须由用户显式触发）
	gw.SyncEntry()
	if gw.findEntry() != nil {
		t.Fatal("SyncEntry 不应主动创建网关条目")
	}

	// 条目存在但 token 过期：自愈应刷新
	stale := acp.MCPServerEntry{
		Type:    acp.MCPTypeHTTP,
		Url:     "http://old-host/mcp/gateway",
		Headers: map[string]string{"Authorization": "Bearer stale"},
	}
	if err := acp.UpsertMCPServerEntry(configPath, GatewayMCPName, stale); err != nil {
		t.Fatal(err)
	}
	gw.SyncEntry()
	entry := gw.findEntry()
	if entry == nil || entry.Url != "http://127.0.0.1:8008/mcp/gateway" {
		t.Fatalf("自愈后 entry = %+v", entry)
	}
	if entry.Headers["Authorization"] != "Bearer "+token {
		t.Errorf("token 未刷新: %q", entry.Headers["Authorization"])
	}
}

func TestGateway_StatusReportsUpstreamsAndSkipped(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()
	configPath := writeMCPConfig(t, map[string]any{
		"alpha":    map[string]any{"type": "http", "url": alphaURL},
		"dead":     map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp"},
		"local-fs": map[string]any{"command": "fs-server"},
	})
	_, _, gw := setupGateway(t, configPath)
	gw.SetPublicBaseURL("http://127.0.0.1:8008")

	st := gw.Status(t.Context(), 1)
	if st.ToolCount != 1 {
		t.Errorf("tool_count = %d, 期望 1", st.ToolCount)
	}
	if len(st.Upstreams) != 2 {
		t.Fatalf("upstreams = %+v, 期望 2 个（alpha + dead）", st.Upstreams)
	}
	byName := map[string]UpstreamStatus{}
	for _, u := range st.Upstreams {
		byName[u.Name] = u
	}
	if !byName["alpha"].Connected || byName["alpha"].ToolCount != 1 {
		t.Errorf("alpha = %+v", byName["alpha"])
	}
	if byName["dead"].Connected || byName["dead"].Error == "" {
		t.Errorf("dead 应为未连接且带错误: %+v", byName["dead"])
	}
	if len(st.Skipped) != 1 || st.Skipped[0].Name != "local-fs" || st.Skipped[0].Type != acp.MCPTypeStdio {
		t.Fatalf("skipped = %+v, 期望仅 local-fs(stdio)", st.Skipped)
	}
	if st.Skipped[0].Reason == "" {
		t.Error("跳过原因不应为空")
	}
}
