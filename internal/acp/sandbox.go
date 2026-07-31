package acp

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
)

// 沙箱模式常量。
const (
	// SandboxModeAuto 平台不支持沙箱时降级为直通执行（记告警）。
	SandboxModeAuto = "auto"
	// SandboxModeEnforce 平台不支持沙箱时拒绝启动 agent。
	SandboxModeEnforce = "enforce"
)

// SandboxSettings 是全局沙箱开关（来自 config.yaml 的 sandbox 段）。
type SandboxSettings struct {
	Enabled bool
	Mode    string // auto | enforce
}

// globalSandbox 当前生效的全局沙箱设置。默认关闭；由 SetSandboxSettings 注入。
var globalSandbox atomic.Pointer[SandboxSettings]

// SetSandboxSettings 设置全局沙箱开关（启动时或设置页热更新调用）。
func SetSandboxSettings(s SandboxSettings) {
	if s.Mode != SandboxModeEnforce {
		s.Mode = SandboxModeAuto
	}
	globalSandbox.Store(&s)
}

// CurrentSandboxSettings 返回当前生效的沙箱设置（未设置时为关闭）。
func CurrentSandboxSettings() SandboxSettings {
	if s := globalSandbox.Load(); s != nil {
		return *s
	}
	return SandboxSettings{}
}

// SandboxProfile 描述一次 agent 进程的沙箱资源声明（与 agent 类型无关）。
// 语义：文件系统整体只读，仅 WriteDirs 可写；网络放行（agent 需访问 LLM API）。
type SandboxProfile struct {
	// WriteDirs 读写白名单目录（绝对路径）：工作区/worktree、会话数据目录、
	// agent 自身配置/登录态目录、npm 缓存、临时目录等。
	WriteDirs []string
}

// SandboxWrap 把任意 argv 包裹为沙箱内执行的 argv（stdio 原样透传，ACP 协议无感知）。
// 平台不支持或沙箱工具缺失时返回原 argv 且 degraded=true，由调用方按 Mode 决定降级或拒绝。
func SandboxWrap(argv []string, p SandboxProfile) (wrapped []string, degraded bool) {
	return sandboxWrapPlatform(argv, p)
}

// ConfigDirsProvider 是可选的后端接口：声明 agent 自身需要读写的配置/登录态目录。
// 未实现时按 agent 类型名推导默认目录（~/.<type> 等）。
type ConfigDirsProvider interface {
	ConfigDirs() []string
}

// BuildSandboxProfile 组装一次 agent 进程的沙箱资源声明：
// 工作目录 + ~/.openNexus（数据/会话/bridge socket）+ agent 配置目录 + npm 缓存 + 临时目录。
// extraWriteDirs 供调用方追加（如 bridge socket 目录不在默认位置时）。
func BuildSandboxProfile(backend Backend, workDir string, extraWriteDirs ...string) SandboxProfile {
	var dirs []string
	add := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" {
			return
		}
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		for _, exist := range dirs {
			if exist == d {
				return
			}
		}
		dirs = append(dirs, d)
	}

	add(workDir)
	add(os.TempDir())
	if home, err := os.UserHomeDir(); err == nil {
		// 数据/会话/二进制缓存/bridge socket 全在 ~/.openNexus 下
		add(filepath.Join(home, ".openNexus"))
		// npx / npm exec 运行 agent 需要写包缓存
		add(filepath.Join(home, ".npm"))
	}
	if backend != nil {
		if p, ok := backend.(ConfigDirsProvider); ok {
			for _, d := range p.ConfigDirs() {
				add(d)
			}
		} else {
			for _, d := range defaultAgentConfigDirs(backend.Name()) {
				add(d)
			}
		}
	}
	for _, d := range extraWriteDirs {
		add(d)
	}
	return SandboxProfile{WriteDirs: dirs}
}

// defaultAgentConfigDirs 按 agent 类型名推导其配置/登录态目录。
// 通用约定 ~/.<type> 与 ~/.config/<type>；已知别名（claude-code → ~/.claude）单独映射。
func defaultAgentConfigDirs(agentType string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(agentType))
	if name == "" {
		return nil
	}
	names := []string{name}
	// claude-code 的配置目录是 ~/.claude；其余 agent 同时尝试去掉 -agent/-code 后缀的短名
	for _, suffix := range []string{"-code", "-agent", "-cli"} {
		if short := strings.TrimSuffix(name, suffix); short != name {
			names = append(names, short)
		}
	}
	var dirs []string
	for _, n := range names {
		dirs = append(dirs,
			filepath.Join(home, "."+n),
			filepath.Join(home, ".config", n),
			filepath.Join(home, ".local", "share", n),
		)
	}
	return dirs
}

// credentialEnvPrefixes / credentialEnvSuffixes 是沙箱模式下从 agent 环境中剥离的
// 凭证类变量特征：镜像仓库、云厂商、代码托管凭证与 ssh-agent 通道。
// 注意 LLM API Key（如 ANTHROPIC_API_KEY）不在此列——backend.Env() 声明的变量
// 在剥离后追加，agent 正常工作所需的 key 由后端配置显式透传。
var credentialEnvPrefixes = []string{
	"AWS_", "DOCKER_", "GITHUB_", "GH_", "GITLAB_", "ALICLOUD_", "TENCENTCLOUD_",
	"AZURE_", "GOOGLE_APPLICATION_", "HARBOR_", "REGISTRY_",
}

var credentialEnvSuffixes = []string{
	"_TOKEN", "_SECRET", "_PASSWORD", "_PASSWD", "_CREDENTIALS",
}

var credentialEnvExact = []string{
	"KUBECONFIG", "SSH_AUTH_SOCK", "GIT_ASKPASS", "NPM_TOKEN",
}

// SanitizeEnvForSandbox 剥离环境中的凭证类变量并注入安全默认值。
// 仅在沙箱开启时使用：base 为父进程环境，返回净化后的环境
// （调用方随后追加 backend.Env()，声明的 API Key 等在其中显式透传）。
func SanitizeEnvForSandbox(base []string) []string {
	out := make([]string, 0, len(base))
	for _, e := range base {
		name, _, ok := strings.Cut(e, "=")
		if !ok || isCredentialEnv(name) {
			continue
		}
		out = append(out, e)
	}
	// 禁止 git 在无凭证时交互式提问挂住 agent
	out = append(out, "GIT_TERMINAL_PROMPT=0")
	return out
}

// isCredentialEnv 报告变量名是否命中凭证特征。
func isCredentialEnv(name string) bool {
	upper := strings.ToUpper(name)
	for _, exact := range credentialEnvExact {
		if upper == exact {
			return true
		}
	}
	for _, p := range credentialEnvPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	for _, s := range credentialEnvSuffixes {
		if strings.HasSuffix(upper, s) {
			return true
		}
	}
	return false
}

// logSandboxDegraded 记录沙箱降级为直通执行（auto 模式下平台不支持/工具缺失）。
func logSandboxDegraded(agent string) {
	slog.Warn("沙箱不可用，agent 降级为无隔离执行", "agent", agent, "os", runtime.GOOS)
}
