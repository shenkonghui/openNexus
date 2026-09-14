package services

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"opennexus/internal/config"
)

// fakeCloudflared 写一个可执行脚本冒充 cloudflared：向 stderr 输出指定行后挂起。
func fakeCloudflared(t *testing.T, lines string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell 脚本假 cloudflared 仅支持 unix")
	}
	bin := filepath.Join(t.TempDir(), "cloudflared")
	script := "#!/bin/sh\n" + lines + "\nsleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 cloudflared 失败: %v", err)
	}
	return bin
}

func waitState(t *testing.T, svc *TunnelService, want TunnelState) TunnelStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.Status()
		if st.State == want {
			return st
		}
		if st.State == TunnelStateError {
			t.Fatalf("隧道进入 error，期望 %s: %s", want, st.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待状态 %s 超时，当前 %s", want, svc.Status().State)
	return TunnelStatus{}
}

func TestTunnelService_QuickTunnelURL(t *testing.T) {
	bin := fakeCloudflared(t, "echo 'INF |  https://abc-def-123.trycloudflare.com  |' >&2")
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeQuick, CloudflaredPath: bin}, 8008)
	defer svc.Stop()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	st := waitState(t, svc, TunnelStateRunning)
	if st.URL != "https://abc-def-123.trycloudflare.com" {
		t.Fatalf("公网地址解析错误: %q", st.URL)
	}
	svc.Stop()
	if st := svc.Status(); st.State != TunnelStateStopped {
		t.Fatalf("Stop 后状态应为 stopped，实际 %s", st.State)
	}
}

func TestTunnelService_TokenModeReady(t *testing.T) {
	bin := fakeCloudflared(t, "echo 'INF Registered tunnel connection connIndex=0' >&2")
	svc := NewTunnelService(config.TunnelConfig{
		Mode:            config.TunnelModeToken,
		Token:           "fake-token",
		Hostname:        "https://nexus.example.com",
		CloudflaredPath: bin,
	}, 8008)
	defer svc.Stop()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	st := waitState(t, svc, TunnelStateRunning)
	if st.URL != "https://nexus.example.com" {
		t.Fatalf("token 模式 url 应取 hostname，实际 %q", st.URL)
	}
}

func TestTunnelService_TokenModeMissingToken(t *testing.T) {
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeToken}, 8008)
	if err := svc.Start(); err == nil {
		t.Fatal("缺 token 应启动失败")
	}
	if st := svc.Status(); st.State != TunnelStateError {
		t.Fatalf("状态应为 error，实际 %s", st.State)
	}
}

func TestTunnelService_BinaryNotFound(t *testing.T) {
	svc := NewTunnelService(config.TunnelConfig{
		Mode:            config.TunnelModeQuick,
		CloudflaredPath: filepath.Join(t.TempDir(), "nonexistent"),
	}, 8008)
	if err := svc.Start(); err == nil {
		t.Fatal("cloudflared 不存在应启动失败")
	}
	if st := svc.Status(); st.Installed {
		t.Fatal("Installed 应为 false")
	}
}

func TestTunnelService_ProcessExitMarksError(t *testing.T) {
	bin := fakeCloudflared(t, "echo 'ERR failed to start' >&2; exit 1\nexit 0")
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeQuick, CloudflaredPath: bin}, 8008)
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	st := waitState(t, svc, TunnelStateError)
	if st.Error == "" {
		t.Fatal("进程退出后应记录错误信息")
	}
}
