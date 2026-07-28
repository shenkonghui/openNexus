package gatewaymcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticAuthenticator(t *testing.T) {
	auth := NewStaticAuthenticator("my-token")

	// 正确 token
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer my-token")
	uid, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("正确 token 校验失败: %v", err)
	}
	if uid != 1 {
		t.Errorf("uid = %d, 期望 1", uid)
	}

	// 错误 token
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.Header.Set("Authorization", "Bearer wrong")
	if _, err := auth.Authenticate(req2); err == nil {
		t.Fatal("错误 token 应返回 err")
	}

	// 缺失 Authorization
	req3 := httptest.NewRequest(http.MethodPost, "/", nil)
	if _, err := auth.Authenticate(req3); err == nil {
		t.Fatal("缺失 Authorization 应返回 err")
	}

	if auth.TokenForUser(0) != "my-token" || auth.SharedToken() != "my-token" {
		t.Fatal("TokenForUser / SharedToken 应返回固定 token")
	}
}

func TestLoadStandaloneConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")

	// 缺 token 应失败（normalize 阶段）
	if err := os.WriteFile(path, []byte("listen: :9000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStandaloneConfig(path); err == nil {
		t.Fatal("缺 token 应返回错误")
	}

	// 完整配置
	cfgYaml := strings.Join([]string{
		"listen: :9000",
		"public_base_url: http://192.168.1.10:9000/",
		"token: abc123",
		"mcp_config_path: " + filepath.Join(dir, "mcp.json"),
		"auto_enable: true",
	}, "\n")
	if err := os.WriteFile(path, []byte(cfgYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadStandaloneConfig(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Listen != ":9000" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.PublicBaseURL != "http://192.168.1.10:9000" {
		t.Errorf("PublicBaseURL 末尾 / 应被去掉: %q", cfg.PublicBaseURL)
	}
	if cfg.Token != "abc123" || !cfg.AutoEnable {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadStandaloneConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	// 只写 token，其余走默认
	if err := os.WriteFile(path, []byte("token: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadStandaloneConfig(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Listen != ":8090" {
		t.Errorf("默认 Listen = %q, 期望 :8090", cfg.Listen)
	}
	if cfg.MCPConfigPath == "" {
		t.Error("默认 MCPConfigPath 不应为空")
	}
}

func TestLoadStandaloneConfig_BootstrapWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "gateway.yaml")

	// 文件不存在：应自动生成模板并返回 ConfigBootstrapError
	_, err := LoadStandaloneConfig(path)
	var bootErr *ConfigBootstrapError
	if !errors.As(err, &bootErr) {
		t.Fatalf("期望 ConfigBootstrapError, 实际 %T: %v", err, err)
	}
	if bootErr.Path != path {
		t.Errorf("Path = %q", bootErr.Path)
	}
	// 文件应已生成，且含随机 token
	data, statErr := os.ReadFile(path)
	if statErr != nil {
		t.Fatalf("模板未生成: %v", statErr)
	}
	if !strings.Contains(string(data), "token:") {
		t.Errorf("模板缺 token 字段:\n%s", data)
	}
	// 再次加载应能成功（模板已带随机 token）
	cfg, err := LoadStandaloneConfig(path)
	if err != nil {
		t.Fatalf("模板生成后再次加载失败: %v", err)
	}
	if cfg.Token == "" {
		t.Error("模板 token 不应为空")
	}
}

func TestGateway_StaticAuthenticator(t *testing.T) {
	// 验证独立模式（StaticAuthenticator）下网关能正常鉴权与转发
	alphaURL, closeAlpha := startUpstream(t, "alpha")
	defer closeAlpha()
	configPath := writeMCPConfig(t, map[string]any{
		"alpha": map[string]any{"type": "http", "url": alphaURL},
	})

	const token = "standalone-token"
	gw, err := New(configPath, NewStaticAuthenticator(token), filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(func() {
		ts.Close()
		gw.Close()
	})

	// 无 token → 401
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 token status=%d, 期望 401", resp.StatusCode)
	}

	// 带 token → 能列到工具
	sess := connectGateway(t, ts.URL, token)
	names := listToolNames(t, sess)
	if len(names) != 1 || names[0] != "alpha_echo" {
		t.Fatalf("工具列表 = %v, 期望 alpha_echo", names)
	}

	// Status 返回的 token 应是固定 token
	st := gw.Status(t.Context(), 1)
	if st.Token != token {
		t.Errorf("Status.Token = %q, 期望 %q", st.Token, token)
	}
}
