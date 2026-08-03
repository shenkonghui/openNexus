package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"opennexus/internal/models"
)

func TestConfigBackend_BasicFields(t *testing.T) {
	enabled := true
	cfg := models.AgentConfig{
		Type:      "codebuddy",
		Command:   "codebuddy",
		Args:      `["--acp","--port","8080"]`,
		APIKeyEnv: "CODEBUDDY_API_KEY",
		Timeout:   "120s",
		Enabled:   &enabled,
	}
	b := NewConfigBackend(cfg)
	if b.Name() != "codebuddy" {
		t.Errorf("Name = %q", b.Name())
	}
	if b.Command() != "codebuddy" {
		t.Errorf("Command = %q", b.Command())
	}
	args := b.Args()
	if len(args) != 3 || args[0] != "--acp" {
		t.Errorf("Args = %+v", args)
	}
	if d := b.Timeout(); d != 120*time.Second {
		t.Errorf("Timeout = %v", d)
	}
}

func TestConfigBackend_Defaults(t *testing.T) {
	b := NewConfigBackend(models.AgentConfig{Type: "x", Command: "x"})
	if b.Args() != nil {
		t.Errorf("空 Args 期望 nil, 实际 %+v", b.Args())
	}
	if d := b.Timeout(); d != 300*time.Second {
		t.Errorf("空 Timeout 期望 300s, 实际 %v", d)
	}
}

func TestConfigBackend_InvalidArgsAndTimeout(t *testing.T) {
	b := NewConfigBackend(models.AgentConfig{
		Type:    "x",
		Command: "x",
		Args:    "not-json",
		Timeout: "bad",
	})
	if b.Args() != nil {
		t.Errorf("非法 Args 期望 nil, 实际 %+v", b.Args())
	}
	if d := b.Timeout(); d != 300*time.Second {
		t.Errorf("非法 Timeout 期望回退 300s, 实际 %v", d)
	}
}

func TestFixNpmExecArgs(t *testing.T) {
	in := []string{"exec", "--include=optional", "--yes", "@tencent-ai/codebuddy-code@2.106.7", "--acp"}
	want := []string{"exec", "--include=optional", "--yes", "@tencent-ai/codebuddy-code@2.106.7", "--", "--acp"}
	got := fixNpmExecArgs("npm", in)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
	// 已有 "--" 时不重复插入
	already := []string{"exec", "--include=optional", "--yes", "pkg", "--", "--acp"}
	if fixNpmExecArgs("npm", already) != nil {
		// slice compare - same content
		got2 := fixNpmExecArgs("npm", already)
		if len(got2) != len(already) {
			t.Errorf("已有 -- 时不应修改: %+v", got2)
		}
	}
	// 非 npm exec 不修改
	plain := []string{"--acp"}
	if fixNpmExecArgs("codebuddy", plain)[0] != "--acp" {
		t.Errorf("非 npm 命令不应修改 args")
	}
}

func TestConfigBackendFromParams(t *testing.T) {
	b, err := ConfigBackendFromParams("devin", "devin", []string{"--acp"}, "DEVIN_KEY", "90s")
	if err != nil {
		t.Fatalf("ConfigBackendFromParams 错误: %v", err)
	}
	if b.Name() != "devin" || b.Timeout() != 90*time.Second {
		t.Errorf("后端字段不正确: %+v", b)
	}
	// 验证 args 已正确编码
	var args []string
	_ = json.Unmarshal([]byte(b.cfg.Args), &args)
	if len(args) != 1 || args[0] != "--acp" {
		t.Errorf("args 编码不正确: %s", b.cfg.Args)
	}
}

// envInSlice 判断 envs 是否包含指定项。
func envInSlice(envs []string, want string) bool {
	for _, e := range envs {
		if e == want {
			return true
		}
	}
	return false
}

func TestConfigBackend_EnvInjection(t *testing.T) {
	t.Setenv("CODEBUDDY_API_KEY", "secret-key")
	enabled := true
	cfg := models.AgentConfig{
		Type:      "codebuddy",
		Command:   "codebuddy",
		APIKeyEnv: "CODEBUDDY_API_KEY",
		Enabled:   &enabled,
		Env:       `{"HTTPS_PROXY":"http://127.0.0.1:7890","HTTP_PROXY":"http://127.0.0.1:7890"}`,
	}
	b := NewConfigBackend(cfg)
	envs := b.Env()

	// API Key 与自定义环境变量都应被注入
	if !envInSlice(envs, "CODEBUDDY_API_KEY=secret-key") {
		t.Errorf("缺少 API Key 注入: %+v", envs)
	}
	if !envInSlice(envs, "HTTPS_PROXY=http://127.0.0.1:7890") {
		t.Errorf("缺少 HTTPS_PROXY 注入: %+v", envs)
	}
	if !envInSlice(envs, "HTTP_PROXY=http://127.0.0.1:7890") {
		t.Errorf("缺少 HTTP_PROXY 注入: %+v", envs)
	}
}

func TestConfigBackend_EnvInvalidJSON(t *testing.T) {
	// 非法 JSON 的 Env 不应 panic，且不影响其它注入。
	b := NewConfigBackend(models.AgentConfig{
		Type:    "x",
		Command: "x",
		Env:     "not-json",
	})
	envs := b.Env()
	if len(envs) != 0 {
		t.Errorf("非法 Env 应产生 0 个变量, 实际 %+v", envs)
	}
}

func TestConfigBackend_EnvEmpty(t *testing.T) {
	b := NewConfigBackend(models.AgentConfig{Type: "x", Command: "x", Env: ""})
	if got := b.Env(); len(got) != 0 {
		t.Errorf("空 Env 应返回空切片, 实际 %+v", got)
	}
}

// TestFindBinaryInPath 验证 binary agent 优先从 PATH 查找同名二进制：
//   - PATH 中存在 → 返回解析后的绝对路径（免下载）
//   - PATH 中不存在 → 返回空串（调用方据此降级为下载）
//   - 带子目录的 cmd（如 ./bin/devin）取 basename（devin）查找
func TestFindBinaryInPath(t *testing.T) {
	// 构造临时目录并放入一个可执行文件，加入 PATH
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-cli-x")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("写入临时二进制失败: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)
	defer t.Setenv("PATH", oldPath)

	// PATH 命中：basename 匹配
	if got := findBinaryInPath("./fake-cli-x"); got == "" {
		t.Errorf("PATH 中存在 fake-cli-x，期望返回路径，实际空串")
	} else if filepath.Base(got) != "fake-cli-x" {
		t.Errorf("期望 basename=fake-cli-x，实际 %s", got)
	}

	// 带子目录的 cmd 取 basename 查找
	if got := findBinaryInPath("./dist-package/fake-cli-x"); got == "" {
		t.Errorf("带子目录的 cmd 应取 basename 查找，期望命中，实际空串")
	}

	// PATH 不存在 → 空串
	if got := findBinaryInPath("./this-definitely-not-exists-12345"); got != "" {
		t.Errorf("PATH 中不存在的二进制，期望空串，实际 %s", got)
	}

	// 空/无效 cmd → 空串
	if got := findBinaryInPath(""); got != "" {
		t.Errorf("空 cmd 应返回空串，实际 %s", got)
	}
	if got := findBinaryInPath("./"); got != "" {
		t.Errorf("无效 cmd ./ 应返回空串，实际 %s", got)
	}
}

// TestIsExecutableForCurrentPlatform 校验异平台原生二进制会被识别为不兼容。
// 这是修复「容器挂载宿主机 ~/.local，PATH 命中 macOS Mach-O，Linux 容器内 Exec format error」
// 的关键防线：无法运行的二进制必须被 findBinaryInPath 拒绝。
func TestIsExecutableForCurrentPlatform(t *testing.T) {
	tmp := t.TempDir()

	// --- 当前平台原生二进制：应放行 ---
	// 用当前进程自身二进制（始终是当前平台原生格式）
	if self, err := os.Executable(); err == nil {
		if !isExecutableForCurrentPlatform(self) {
			t.Errorf("当前进程二进制应判定为当前平台兼容，实际拒绝")
		}
	}

	// --- 脚本（shebang）：应放行（测试 fixture 与 shell 脚本型 binary 走这里）---
	scriptPath := filepath.Join(tmp, "my-script")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	if !isExecutableForCurrentPlatform(scriptPath) {
		t.Errorf("shebang 脚本应放行，实际拒绝")
	}

	// --- Mach-O 二进制（macOS LE arm64 magic: CF FA ED FE）---
	machoPath := filepath.Join(tmp, "macos-bin")
	if err := os.WriteFile(machoPath, []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 0, 0, 0}, 0o755); err != nil {
		t.Fatalf("写入 Mach-O 失败: %v", err)
	}
	// --- ELF 二进制（Linux magic: 7F 45 4C 46）---
	elfPath := filepath.Join(tmp, "linux-bin")
	if err := os.WriteFile(elfPath, []byte{0x7f, 0x45, 0x4c, 0x46, 0, 0, 0, 0}, 0o755); err != nil {
		t.Fatalf("写入 ELF 失败: %v", err)
	}

	// 当前平台只能接受自己的原生格式；另一个平台的二进制必须被拒绝。
	// 这样无论测试在哪个平台跑，都能验证「异平台二进制被拒绝」这一核心不变量。
	switch runtime.GOOS {
	case "darwin", "ios":
		if !isExecutableForCurrentPlatform(machoPath) {
			t.Errorf("darwin 上 Mach-O 应放行，实际拒绝")
		}
		if isExecutableForCurrentPlatform(elfPath) {
			t.Errorf("darwin 上 ELF 应拒绝（异平台原生二进制），实际放行")
		}
	case "linux", "android", "freebsd", "netbsd", "openbsd", "dragonfly", "solaris":
		if !isExecutableForCurrentPlatform(elfPath) {
			t.Errorf("%s 上 ELF 应放行，实际拒绝", runtime.GOOS)
		}
		if isExecutableForCurrentPlatform(machoPath) {
			t.Errorf("%s 上 Mach-O 应拒绝（异平台原生二进制），实际放行", runtime.GOOS)
		}
	}

	// --- 太短的文件（<4字节）：放行（保守，不拦截）---
	shortPath := filepath.Join(tmp, "short")
	if err := os.WriteFile(shortPath, []byte{0x7f, 0x45}, 0o755); err != nil {
		t.Fatalf("写入短文件失败: %v", err)
	}
	if !isExecutableForCurrentPlatform(shortPath) {
		t.Errorf("过短文件应放行（保守不拦截），实际拒绝")
	}
}

// TestFindBinaryInPath_RejectsForeignBinary 校验：当 PATH 命中的二进制是异平台原生二进制时，
// findBinaryInPath 必须返回空串（让其降级走下载），而不是命中一个跑不了的二进制。
func TestFindBinaryInPath_RejectsForeignBinary(t *testing.T) {
	tmp := t.TempDir()
	// 构造一个异平台二进制：当前是 darwin 就用 ELF，当前是 *nix 就用 Mach-O
	var content []byte
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		content = []byte{0x7f, 0x45, 0x4c, 0x46} // ELF，在 darwin 上不可执行
	} else {
		content = []byte{0xcf, 0xfa, 0xed, 0xfe} // Mach-O arm64，在非 darwin 上不可执行
	}
	binPath := filepath.Join(tmp, "foreign-cli-x")
	if err := os.WriteFile(binPath, content, 0o755); err != nil {
		t.Fatalf("写入异平台二进制失败: %v", err)
	}
	t.Setenv("PATH", tmp)

	if got := findBinaryInPath("./foreign-cli-x"); got != "" {
		t.Errorf("异平台二进制应被拒绝（返回空串走下载），实际命中 %s", got)
	}
}
