package acp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"opennexus/internal/models"
)

// fakeDirsBackend 实现 Backend + ConfigDirsProvider，用于测试 profile 组装。
type fakeDirsBackend struct {
	name string
	dirs []string
}

func (f *fakeDirsBackend) Name() string           { return f.name }
func (f *fakeDirsBackend) Command() string        { return "fake" }
func (f *fakeDirsBackend) Args() []string         { return nil }
func (f *fakeDirsBackend) Env() []string          { return nil }
func (f *fakeDirsBackend) Timeout() time.Duration { return time.Second }
func (f *fakeDirsBackend) ConfigDirs() []string   { return f.dirs }

func TestSandboxSettings_DefaultDisabledAndModeFallback(t *testing.T) {
	old := globalSandbox.Load()
	defer globalSandbox.Store(old)

	globalSandbox.Store(nil)
	if s := CurrentSandboxSettings(); s.Enabled {
		t.Fatalf("未设置时应默认关闭: %+v", s)
	}
	SetSandboxSettings(SandboxSettings{Enabled: true, Mode: "bogus"})
	if s := CurrentSandboxSettings(); !s.Enabled || s.Mode != SandboxModeAuto {
		t.Fatalf("非法 mode 应兜底 auto: %+v", s)
	}
	SetSandboxSettings(SandboxSettings{Enabled: true, Mode: SandboxModeEnforce})
	if s := CurrentSandboxSettings(); s.Mode != SandboxModeEnforce {
		t.Fatalf("enforce 应保留: %+v", s)
	}
}

func TestBuildSandboxProfile_ContainsCoreDirsAndDedup(t *testing.T) {
	work := t.TempDir()
	extra := t.TempDir()
	be := &fakeDirsBackend{name: "fake-agent", dirs: []string{extra, extra}}
	p := BuildSandboxProfile(be, work, work) // extraWriteDirs 重复 workDir 应去重

	want := []string{work, os.TempDir(), extra}
	for _, w := range want {
		found := false
		abs, _ := filepath.Abs(w)
		for _, d := range p.WriteDirs {
			if d == abs {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("WriteDirs 缺少 %s: %v", w, p.WriteDirs)
		}
	}
	seen := map[string]bool{}
	for _, d := range p.WriteDirs {
		if seen[d] {
			t.Fatalf("WriteDirs 存在重复项 %s: %v", d, p.WriteDirs)
		}
		seen[d] = true
	}
}

func TestDefaultAgentConfigDirs_ShortNameVariants(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无 home 目录")
	}
	dirs := defaultAgentConfigDirs("claude-code")
	wantOne := func(target string) {
		for _, d := range dirs {
			if d == target {
				return
			}
		}
		t.Fatalf("缺少推导目录 %s: %v", target, dirs)
	}
	wantOne(filepath.Join(home, ".claude-code"))
	wantOne(filepath.Join(home, ".claude")) // -code 后缀短名
	if got := defaultAgentConfigDirs("  "); got != nil {
		t.Fatalf("空类型名应返回 nil: %v", got)
	}
}

func TestConfigBackend_ConfigDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无 home 目录")
	}
	// 显式 config_dirs：JSON 解析 + ~ 展开
	b := NewConfigBackend(models.AgentConfig{
		Type:       "myagent",
		ConfigDirs: `["~/.myagent", "/opt/agent-data"]`,
	})
	dirs := b.ConfigDirs()
	if len(dirs) != 2 || dirs[0] != filepath.Join(home, ".myagent") || dirs[1] != "/opt/agent-data" {
		t.Fatalf("显式 config_dirs 解析错误: %v", dirs)
	}
	// 未配置回退按类型名推导
	b2 := NewConfigBackend(models.AgentConfig{Type: "myagent"})
	if dirs2 := b2.ConfigDirs(); len(dirs2) == 0 || dirs2[0] != filepath.Join(home, ".myagent") {
		t.Fatalf("缺省应回退推导目录: %v", dirs2)
	}
	// 非法 JSON 同样回退
	b3 := NewConfigBackend(models.AgentConfig{Type: "myagent", ConfigDirs: "not-json"})
	if dirs3 := b3.ConfigDirs(); len(dirs3) == 0 || dirs3[0] != filepath.Join(home, ".myagent") {
		t.Fatalf("非法 JSON 应回退推导目录: %v", dirs3)
	}
}

func TestSanitizeEnvForSandbox_StripsCredentialsKeepsBase(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"AWS_ACCESS_KEY_ID=xxx",
		"DOCKER_CONFIG=/root/.docker",
		"GITHUB_TOKEN=ghp_x",
		"MY_APP_TOKEN=t",
		"DB_PASSWORD=p",
		"KUBECONFIG=/root/.kube/config",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"ANTHROPIC_API_KEY=sk-ant", // LLM key 不剥离（由 backend.Env 声明透传的除外，这里保留）
	}
	out := SanitizeEnvForSandbox(base)
	joined := strings.Join(out, "\n")
	for _, kept := range []string{"PATH=/usr/bin", "HOME=/home/u", "ANTHROPIC_API_KEY=sk-ant", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(joined, kept) {
			t.Fatalf("应保留 %s: %v", kept, out)
		}
	}
	for _, stripped := range []string{"AWS_", "DOCKER_CONFIG", "GITHUB_TOKEN", "MY_APP_TOKEN", "DB_PASSWORD", "KUBECONFIG", "SSH_AUTH_SOCK"} {
		if strings.Contains(joined, stripped) {
			t.Fatalf("应剥离 %s: %v", stripped, out)
		}
	}
}

func TestSandboxWrap_PlatformBehavior(t *testing.T) {
	argv := []string{"/bin/echo", "hi"}
	wrapped, degraded := SandboxWrap(argv, SandboxProfile{WriteDirs: []string{t.TempDir()}})
	switch runtime.GOOS {
	case "darwin":
		// macOS 自带 sandbox-exec，不应降级；wrapper 前缀 + 原 argv 保留在尾部
		if degraded {
			t.Fatalf("darwin 不应降级")
		}
		if len(wrapped) < 3 || !strings.HasSuffix(wrapped[0], "sandbox-exec") {
			t.Fatalf("darwin 应以 sandbox-exec 包裹: %v", wrapped)
		}
		if wrapped[len(wrapped)-2] != argv[0] || wrapped[len(wrapped)-1] != argv[1] {
			t.Fatalf("原 argv 应保留在尾部: %v", wrapped)
		}
	default:
		// linux 依赖 bwrap 是否安装，两种结果都合法；仅验证不破坏 argv
		if degraded && (len(wrapped) != 2 || wrapped[0] != argv[0]) {
			t.Fatalf("降级时应返回原 argv: %v", wrapped)
		}
	}
}
