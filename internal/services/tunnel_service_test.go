package services

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"opennexus/internal/config"
)

// fakeBin 写一个可执行脚本冒充隧道子进程：输出指定行后挂起。
func fakeBin(t *testing.T, name, lines string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell 脚本假隧道进程仅支持 unix")
	}
	bin := filepath.Join(t.TempDir(), name)
	script := "#!/bin/sh\n" + lines + "\nsleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 %s 失败: %v", name, err)
	}
	return bin
}

// fakeCloudflared 冒充 cloudflared：向 stderr 输出指定行后挂起。
func fakeCloudflared(t *testing.T, lines string) string {
	return fakeBin(t, "cloudflared", lines)
}

// fakeNgrok 冒充 ngrok：--log=stdout 下日志（含 started tunnel 的 url=）写到 stdout。
func fakeNgrok(t *testing.T, lines string) string {
	return fakeBin(t, "ngrok", lines)
}

// fakeVSCode 冒充 code CLI：向 stdout 输出指定行后挂起。
func fakeVSCode(t *testing.T, lines string) string {
	return fakeBin(t, "code", lines)
}

// waitCond 轮询直到 cond 满足（用于等待 starting 期间的中间状态，如设备授权码）。
func waitCond(t *testing.T, svc *TunnelService, cond func(TunnelStatus) bool) TunnelStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.Status()
		if cond(st) {
			return st
		}
		if st.State == TunnelStateError {
			t.Fatalf("隧道进入 error: %s", st.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待条件超时，当前状态 %+v", svc.Status())
	return TunnelStatus{}
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

func TestTunnelService_GuardRefusesStart(t *testing.T) {
	bin := fakeCloudflared(t, "echo 'INF |  https://abc.trycloudflare.com  |' >&2")
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeQuick, CloudflaredPath: bin}, 8008)
	svc.SetGuard(func() error { return errors.New("auto_login 开启，拒绝启动") })
	if err := svc.Start(); err == nil {
		t.Fatal("guard 拒绝时 Start 应失败")
	}
	st := svc.Status()
	if st.State != TunnelStateError || st.Error == "" {
		t.Fatalf("guard 拒绝后应为 error 状态且带原因，实际 %s %q", st.State, st.Error)
	}
	// guard 解除后应能正常启动
	svc.SetGuard(nil)
	if err := svc.Start(); err != nil {
		t.Fatalf("解除 guard 后 Start 失败: %v", err)
	}
	defer svc.Stop()
	waitState(t, svc, TunnelStateRunning)
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

func TestTunnelService_NgrokURL(t *testing.T) {
	bin := fakeNgrok(t, "echo 't=2026-01-01T00:00:00Z lvl=info msg=\"started tunnel\" obj=tunnels addr=//localhost:8008 url=https://abc-123.ngrok-free.app'")
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeNgrok, Token: "fake-authtoken", NgrokPath: bin}, 8008)
	defer svc.Stop()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	st := waitState(t, svc, TunnelStateRunning)
	if st.URL != "https://abc-123.ngrok-free.app" {
		t.Fatalf("ngrok 公网地址解析错误: %q", st.URL)
	}
}

// ngrok 未配置 authtoken 时仍可启动（可用 ngrok 本地已保存的 authtoken）。
func TestTunnelService_NgrokNoTokenStillStarts(t *testing.T) {
	bin := fakeNgrok(t, "echo 'lvl=info msg=\"started tunnel\" url=https://x.ngrok-free.app'")
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeNgrok, NgrokPath: bin}, 8008)
	defer svc.Stop()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	waitState(t, svc, TunnelStateRunning)
}

func TestTunnelService_NgrokBinaryNotFound(t *testing.T) {
	svc := NewTunnelService(config.TunnelConfig{
		Mode:      config.TunnelModeNgrok,
		NgrokPath: filepath.Join(t.TempDir(), "nonexistent"),
	}, 8008)
	if err := svc.Start(); err == nil {
		t.Fatal("ngrok 不存在应启动失败")
	}
	if st := svc.Status(); st.Installed {
		t.Fatal("Installed 应为 false")
	}
}

// vscode 模式：未登录先跑 user login（GitHub 设备授权提示透出为 login_url + device_code），
// 授权成功退出后接力拉起 code tunnel，就绪后取 vscode.dev 地址。
func TestTunnelService_VSCodeDeviceAuthThenRunning(t *testing.T) {
	bin := fakeVSCode(t, `
[ "$3" = show ] && exit 1   # tunnel user show → 未登录
if [ "$2" = user ]; then    # tunnel user login → 设备授权后退出
  [ "$4" = --provider ] && [ "$5" = github ] || { echo "bad login args: $*" >&2; exit 1; }
  echo 'To grant access to the server, please log into https://github.com/login/device and use code ABCD-1234'
  sleep 2   # 模拟等待用户授权，期间 login_url/device_code 应可从 status 读到
  exit 0
fi
[ "$2" = --accept-server-license-terms ] || { echo "bad tunnel args: $*" >&2; exit 1; }
echo 'Open this link in your browser https://vscode.dev/tunnel/my-machine'
`)
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeVSCode, VSCodePath: bin}, 8008)
	defer svc.Stop()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	st := waitCond(t, svc, func(s TunnelStatus) bool { return s.LoginURL != "" })
	if st.LoginURL != "https://github.com/login/device" || st.DeviceCode != "ABCD-1234" {
		t.Fatalf("设备授权信息解析错误: %q %q", st.LoginURL, st.DeviceCode)
	}
	st = waitState(t, svc, TunnelStateRunning)
	if st.URL != "https://vscode.dev/tunnel/my-machine" {
		t.Fatalf("vscode 隧道地址解析错误: %q", st.URL)
	}
	if st.LoginURL != "" || st.DeviceCode != "" {
		t.Fatalf("running 后设备授权信息应清空，实际 %q %q", st.LoginURL, st.DeviceCode)
	}
}

// vscode 模式：microsoft provider 传入 user login，--name 传入隧道进程。
func TestTunnelService_VSCodeArgsAndMicrosoftAuth(t *testing.T) {
	bin := fakeVSCode(t, `
[ "$3" = show ] && exit 1
if [ "$2" = user ]; then
  [ "$3" = login ] && [ "$4" = --provider ] && [ "$5" = microsoft ] || { echo "bad login args: $*" >&2; exit 1; }
  echo 'To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code F9E4C2D7A to authenticate.'
  exit 0
fi
[ "$2" = --accept-server-license-terms ] && [ "$3" = --name ] && [ "$4" = my-box ] || { echo "bad tunnel args: $*" >&2; exit 1; }
echo 'https://vscode.dev/tunnel/my-box'
`)
	svc := NewTunnelService(config.TunnelConfig{
		Mode:       config.TunnelModeVSCode,
		Provider:   "microsoft",
		Hostname:   "my-box",
		VSCodePath: bin,
	}, 8008)
	defer svc.Stop()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	st := waitState(t, svc, TunnelStateRunning)
	if st.URL != "https://vscode.dev/tunnel/my-box" {
		t.Fatalf("vscode 隧道地址解析错误: %q", st.URL)
	}
}

// vscode 模式已有凭据（user show 退出码 0）时跳过授权前置步骤，直接拉起隧道。
func TestTunnelService_VSCodeLoggedInSkipsLogin(t *testing.T) {
	bin := fakeVSCode(t, `
[ "$3" = show ] && exit 0   # 已登录
[ "$2" = user ] && { echo '不应再跑 login' >&2; exit 1; }
echo 'https://vscode.dev/tunnel/x'
`)
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeVSCode, VSCodePath: bin}, 8008)
	defer svc.Stop()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	st := waitState(t, svc, TunnelStateRunning)
	if st.URL != "https://vscode.dev/tunnel/x" {
		t.Fatalf("vscode 隧道地址解析错误: %q", st.URL)
	}
}

// vscode 模式跳过 guard：Remote Tunnel 由账号端到端鉴权，auto_login 检查不适用。
func TestTunnelService_VSCodeSkipsGuard(t *testing.T) {
	bin := fakeVSCode(t, `
[ "$3" = show ] && exit 0
echo 'https://vscode.dev/tunnel/x'
`)
	svc := NewTunnelService(config.TunnelConfig{Mode: config.TunnelModeVSCode, VSCodePath: bin}, 8008)
	svc.SetGuard(func() error { return errors.New("auto_login 开启，拒绝启动") })
	if err := svc.Start(); err != nil {
		t.Fatalf("vscode 模式不应被 guard 拦截: %v", err)
	}
	defer svc.Stop()
	waitState(t, svc, TunnelStateRunning)
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
