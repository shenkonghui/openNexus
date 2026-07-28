package gatewaymcp

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"opennexus/internal/acp"
)

func TestGateway_DisableUpstream(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()
	betaURL, closeBeta := startUpstream(t, "beta")
	defer closeBeta()

	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
		"beta":  map[string]any{"type": "http", "url": betaURL},
	})
	statePath := filepath.Join(t.TempDir(), "state.json")
	gw, err := New(configPath, NewStaticAuthenticator("tok"), statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)

	// 初始两个上游都可用
	sess := connectGateway(t, mustServeURL(t, gw), "tok")
	names := listToolNames(t, sess)
	if len(names) != 2 {
		t.Fatalf("初始工具数 = %d, 期望 2", len(names))
	}

	// 禁用 alpha
	if err := gw.DisableUpstream("alpha"); err != nil {
		t.Fatal(err)
	}
	sess2 := connectGateway(t, mustServeURL(t, gw), "tok")
	names2 := listToolNames(t, sess2)
	if len(names2) != 1 || names2[0] != "beta_echo" {
		t.Fatalf("禁用 alpha 后工具 = %v, 期望仅 beta_echo", names2)
	}

	// 状态中 alpha 应出现在 skipped（原因：已被用户禁用）
	st := gw.Status(t.Context(), 1)
	found := false
	for _, s := range st.Skipped {
		if s.Name == "alpha" && s.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("禁用的上游应出现在 skipped 列表")
	}

	// 重新启用
	if err := gw.EnableUpstream("alpha"); err != nil {
		t.Fatal(err)
	}
	sess3 := connectGateway(t, mustServeURL(t, gw), "tok")
	names3 := listToolNames(t, sess3)
	if len(names3) != 2 {
		t.Fatalf("启用后工具数 = %d, 期望 2", len(names3))
	}
}

func TestGateway_CustomServer(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()

	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
	})
	statePath := filepath.Join(t.TempDir(), "state.json")
	gw, err := New(configPath, NewStaticAuthenticator("tok"), statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)

	// 初始只有 alpha
	sess := connectGateway(t, mustServeURL(t, gw), "tok")
	if len(listToolNames(t, sess)) != 1 {
		t.Fatal("初始应只有 1 个上游")
	}

	// 添加自定义上游 beta
	betaURL, closeBeta := startUpstream(t, "beta")
	defer closeBeta()
	if err := gw.AddCustomServer("beta", acp.MCPServerEntry{
		Type: acp.MCPTypeHTTP,
		Url:  betaURL,
	}); err != nil {
		t.Fatal(err)
	}

	sess2 := connectGateway(t, mustServeURL(t, gw), "tok")
	names := listToolNames(t, sess2)
	if len(names) != 2 {
		t.Fatalf("添加自定义后工具 = %v, 期望 2 个", names)
	}

	// Status 中 beta 的 source 应为 "custom"
	st := gw.Status(t.Context(), 1)
	var betaSource string
	for _, u := range st.Upstreams {
		if u.Name == "beta" {
			betaSource = u.Source
		}
	}
	if betaSource != "custom" {
		t.Errorf("beta source = %q, 期望 custom", betaSource)
	}

	// 移除自定义上游
	if err := gw.RemoveCustomServer("beta"); err != nil {
		t.Fatal(err)
	}
	sess3 := connectGateway(t, mustServeURL(t, gw), "tok")
	if len(listToolNames(t, sess3)) != 1 {
		t.Fatal("移除自定义后应回到 1 个上游")
	}

	// 不能用网关自身名称添加自定义上游
	if err := gw.AddCustomServer(GatewayMCPName, acp.MCPServerEntry{}); err == nil {
		t.Fatal("不应允许用网关自身名称添加自定义上游")
	}
}

// TestGateway_CustomServerWithAuth 验证带自定义 headers（如 Bearer token）的外部 server
// 能被网关代理且 headers 跨实例持久化。
func TestGateway_CustomServerWithAuth(t *testing.T) {
	// 用 startUpstream 模拟外部 server（无鉴权），重点验证带 headers 的 entry 能持久化与转发
	betaURL, closeBeta := startUpstream(t, "ext-beta")
	defer closeBeta()
	configPath := writeMCPConfig(t, map[string]any{})
	statePath := filepath.Join(t.TempDir(), "state.json")
	gw, err := New(configPath, NewStaticAuthenticator("tok"), statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)

	// 添加带自定义 headers 的外部 server
	if err := gw.AddCustomServer("ext-beta", acp.MCPServerEntry{
		Type:    acp.MCPTypeHTTP,
		Url:     betaURL,
		Headers: map[string]string{"Authorization": "Bearer some-token", "X-Custom": "val"},
	}); err != nil {
		t.Fatal(err)
	}

	sess := connectGateway(t, mustServeURL(t, gw), "tok")
	names := listToolNames(t, sess)
	if len(names) != 1 || names[0] != "ext-beta_echo" {
		t.Fatalf("带鉴权的外部 server 工具列表 = %v, 期望 ext-beta_echo", names)
	}

	// 验证 headers 持久化：重新加载 state 应保留 headers
	gw2, err := New(configPath, NewStaticAuthenticator("tok"), statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw2.Close)
	customs, err := gw2.state.CustomServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(customs) != 1 || customs[0].Entry.Headers["Authorization"] != "Bearer some-token" {
		t.Errorf("headers 未持久化: %+v", customs)
	}
}

func TestGateway_StatePersistsAcrossInstances(t *testing.T) {
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()
	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
	})
	statePath := filepath.Join(t.TempDir(), "state.json")

	// 实例 1：禁用 alpha
	gw1, err := New(configPath, NewStaticAuthenticator("tok"), statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := gw1.DisableUpstream("alpha"); err != nil {
		t.Fatal(err)
	}
	gw1.Close()

	// 实例 2：用同一 state 文件，alpha 应仍被禁用
	gw2, err := New(configPath, NewStaticAuthenticator("tok"), statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw2.Close)
	sess := connectGateway(t, mustServeURL(t, gw2), "tok")
	if len(listToolNames(t, sess)) != 0 {
		t.Fatal("禁用状态应跨实例持久化：alpha 应仍被禁用")
	}
}

// mustServeURL 启动网关的 httptest server 并返回 URL，cleanup 自动注册。
func mustServeURL(t *testing.T, gw *Gateway) string {
	t.Helper()
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}
