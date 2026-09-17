package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"opennexus/internal/config"
	"opennexus/internal/services"
)

// setupTunnelRouter 构造仅含隧道端点的测试路由（隧道接口在 AuthRequired 之后，测试直接绕过鉴权）。
func setupTunnelRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  port: 8008\njwt:\n  secret: \"test-secret-key-with-32-plus-bytes!!\"\n"), 0o644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	svc := services.NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeQuick}, 8008)
	h := NewTunnelHandler(cfgPath, svc)
	r := gin.New()
	g := r.Group("/api/v1/tunnel")
	g.GET("", h.Get)
	g.PUT("", h.Update)
	g.POST("/start", h.Start)
	g.POST("/stop", h.Stop)
	return r, cfgPath
}

func TestTunnelHandler_GetDefault(t *testing.T) {
	r, _ := setupTunnelRouter(t)
	w := doJSON(t, r, "GET", "/api/v1/tunnel", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			State    string `json:"state"`
			Mode     string `json:"mode"`
			Enabled  bool   `json:"enabled"`
			HasToken bool   `json:"has_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.State != "stopped" || resp.Data.Mode != "quick" || resp.Data.Enabled || resp.Data.HasToken {
		t.Fatalf("默认值异常: %+v", resp.Data)
	}
}

func TestTunnelHandler_UpdateAndGet(t *testing.T) {
	r, cfgPath := setupTunnelRouter(t)

	w := doJSON(t, r, "PUT", "/api/v1/tunnel", gin.H{
		"enabled": true, "mode": "token", "token": "tok-abc",
		"hostname": "https://nexus.example.com", "cloudflared_path": "/tmp/cloudflared",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
	}
	data, _ := os.ReadFile(cfgPath)
	text := string(data)
	for _, want := range []string{"tunnel:", "enabled: true", "mode: token", "tok-abc", "nexus.example.com", "/tmp/cloudflared"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.yaml 缺少 %q:\n%s", want, text)
		}
	}

	w = doJSON(t, r, "GET", "/api/v1/tunnel", nil)
	var resp struct {
		Data struct {
			Mode            string `json:"mode"`
			Enabled         bool   `json:"enabled"`
			HasToken        bool   `json:"has_token"`
			Hostname        string `json:"hostname"`
			CloudflaredPath string `json:"cloudflared_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.Enabled || resp.Data.Mode != "token" || !resp.Data.HasToken ||
		resp.Data.Hostname != "https://nexus.example.com" || resp.Data.CloudflaredPath != "/tmp/cloudflared" {
		t.Fatalf("GET 视图异常: %+v", resp.Data)
	}
}

// token 未传（null）时保留 config.yaml 现值。
func TestTunnelHandler_UpdateKeepsToken(t *testing.T) {
	r, cfgPath := setupTunnelRouter(t)
	doJSON(t, r, "PUT", "/api/v1/tunnel", gin.H{"enabled": false, "mode": "token", "token": "keep-me"})

	w := doJSON(t, r, "PUT", "/api/v1/tunnel", gin.H{"enabled": true, "mode": "token"})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "keep-me") {
		t.Fatalf("token 应被保留:\n%s", data)
	}
}

// token 模式下未配置 token 时保存应报错。
func TestTunnelHandler_TokenModeRequiresToken(t *testing.T) {
	r, _ := setupTunnelRouter(t)
	w := doJSON(t, r, "PUT", "/api/v1/tunnel", gin.H{"mode": "token"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 token 应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// ngrok 模式：token（authtoken）可选，ngrok_path 写回配置。
func TestTunnelHandler_UpdateNgrok(t *testing.T) {
	r, cfgPath := setupTunnelRouter(t)
	w := doJSON(t, r, "PUT", "/api/v1/tunnel", gin.H{
		"mode": "ngrok", "hostname": "nexus.ngrok-free.app", "ngrok_path": "/tmp/ngrok",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
	}
	data, _ := os.ReadFile(cfgPath)
	text := string(data)
	for _, want := range []string{"mode: ngrok", "nexus.ngrok-free.app", "ngrok_path", "/tmp/ngrok"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.yaml 缺少 %q:\n%s", want, text)
		}
	}

	w = doJSON(t, r, "GET", "/api/v1/tunnel", nil)
	var resp struct {
		Data struct {
			Mode      string `json:"mode"`
			NgrokPath string `json:"ngrok_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Mode != "ngrok" || resp.Data.NgrokPath != "/tmp/ngrok" {
		t.Fatalf("GET 视图异常: %+v", resp.Data)
	}
}

// vscode 模式：provider / vscode_path 写回配置并在 GET 视图中返回。
func TestTunnelHandler_UpdateVSCode(t *testing.T) {
	r, cfgPath := setupTunnelRouter(t)
	w := doJSON(t, r, "PUT", "/api/v1/tunnel", gin.H{
		"mode": "vscode", "provider": "microsoft", "hostname": "my-box", "vscode_path": "/tmp/code",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
	}
	data, _ := os.ReadFile(cfgPath)
	text := string(data)
	for _, want := range []string{"mode: vscode", "provider: microsoft", "my-box", "vscode_path", "/tmp/code"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.yaml 缺少 %q:\n%s", want, text)
		}
	}

	w = doJSON(t, r, "GET", "/api/v1/tunnel", nil)
	var resp struct {
		Data struct {
			Mode       string `json:"mode"`
			Provider   string `json:"provider"`
			VSCodePath string `json:"vscode_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Mode != "vscode" || resp.Data.Provider != "microsoft" || resp.Data.VSCodePath != "/tmp/code" {
		t.Fatalf("GET 视图异常: %+v", resp.Data)
	}
}
