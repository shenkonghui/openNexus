package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Database    DatabaseConfig    `yaml:"database"`
	JWT         JWTConfig         `yaml:"jwt"`
	Auth        AuthConfig        `yaml:"auth"`
	Password    PasswordConfig    `yaml:"password"`
	Logging     LoggingConfig     `yaml:"logging"`
	Agents      AgentsConfig      `yaml:"agents"`
	Debug       DebugConfig       `yaml:"debug"`
	Permissions PermissionsConfig `yaml:"permissions"`
}

// 全局权限模式常量。
const (
	PermissionModeNormal = "normal" // 默认：按 allow/ask/deny 列表匹配，未命中则询问
	PermissionModeYolo   = "yolo"   // 全部自动放行（deny 命中除外）
)

// PermissionsConfig 是全局权限规则配置（白名单 / 询问名单 / 黑名单，对所有会话生效）。
// YOLO 改为会话级开关；Mode 保留兼容旧配置读写，裁决时忽略。
// 运行时按 agent 上报的工具调用标题匹配，每条规则支持 `*` 通配符（如 "Bash(git status *)"）。
// 优先级：deny > allow > ask > (会话 yolo→allow | ask)。
type PermissionsConfig struct {
	Mode  string   `yaml:"mode"`  // 兼容字段；裁决忽略
	Allow []string `yaml:"allow"` // 白名单：命中→放行
	Ask   []string `yaml:"ask"`   // 询问名单：命中→强制询问
	Deny  []string `yaml:"deny"`  // 黑名单：命中→拒绝（最高优先级）
}

// normalize 校正权限模式（空或非法值兜底为 normal）。
func (p *PermissionsConfig) normalize() {
	mode := strings.TrimSpace(p.Mode)
	if mode != PermissionModeNormal && mode != PermissionModeYolo {
		mode = PermissionModeNormal
	}
	p.Mode = mode
}

// DebugConfig 控制调试能力（如 ACP 协议报文捕获）。
type DebugConfig struct {
	ACP ACPDebugConfig `yaml:"acp"`
}

// ACPDebugConfig 控制 ACP JSON-RPC 原始报文与高层事件的本地记录。
type ACPDebugConfig struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"` // 默认 ~/.openNexus/acp-debug
}

// LoggingConfig 控制应用与 ACP 交互日志输出。
type LoggingConfig struct {
	// Level 日志等级：debug | info | warn | error。debug 时输出每次 agent 交互详情。
	Level string `yaml:"level"`
}

type AuthConfig struct {
	AutoLogin bool `yaml:"auto_login"`
}

type ServerConfig struct {
	Port          int    `yaml:"port"`
	Mode          string `yaml:"mode"`
	WebDist       string `yaml:"web_dist"`
	PublicBaseURL string `yaml:"public_base_url"` // 对外 Base URL，供 ACP 注入 Notes MCP；空则用 http://127.0.0.1:{port}
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type JWTConfig struct {
	Secret     string        `yaml:"secret"`
	AccessTTL  time.Duration `yaml:"access_ttl"`
	RefreshTTL time.Duration `yaml:"refresh_ttl"`
}

type PasswordConfig struct {
	BcryptCost int `yaml:"bcrypt_cost"`
}

// SelectorConfig 控制前端 agent+模型 合并下拉框的可见项。
// Filters 是正则列表，前端对 "agentType/模型值" 与 "agentType/模型显示名称" 做子串匹配
// （忽略大小写；模型尚未探测到时仅 "agentType"）。
// 为空则全部显示；非空时任一正则匹配即显示。
type SelectorConfig struct {
	Filters []string `yaml:"filters"`
}

// normalize 校验过滤正则合法性（启动时即报错，避免前端静默失效），并去除空项。
func (s *SelectorConfig) normalize() error {
	out := make([]string, 0, len(s.Filters))
	for _, f := range s.Filters {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, err := regexp.Compile(f); err != nil {
			return fmt.Errorf("agents.selector.filters 正则非法 %q: %w", f, err)
		}
		out = append(out, f)
	}
	s.Filters = out
	return nil
}

type AgentsConfig struct {
	Workspace  WorkspaceConfig  `yaml:"workspace"`
	Skills     SkillsConfig     `yaml:"skills"`
	Commands   CommandsConfig   `yaml:"commands"`
	Rules      RulesConfig      `yaml:"rules"`
	SubAgents  SubAgentsConfig  `yaml:"subagents"`
	MCP        MCPConfig        `yaml:"mcp"`
	ClaudeCode ClaudeCodeConfig `yaml:"claude_code"`
	// Selector 控制前端 agent+模型 合并下拉框的可见项。
	Selector SelectorConfig `yaml:"selector"`
	// PromptMaxDuration 单轮 prompt 的最大存活时间，超时强制结束并标记 interrupted，
	// 防 agent 卡死导致 goroutine 永久泄漏。0 或负值=默认 30min。
	PromptMaxDuration time.Duration `yaml:"prompt_max_duration"`
	// FailedTaskAutoRetryOnce 运行中 agent 崩溃/断连时，是否自动 ResumeSession（重建连接）
	// 并重发同一 prompt，仅一次。nil=默认 true（与历史硬编码行为一致）；显式 false 可关闭。
	FailedTaskAutoRetryOnce *bool `yaml:"failed_task_auto_retry_once"`
	// TerminalEnabled 是否向 agent 声明 ACP terminal 能力：开启后 agent 的 shell 执行
	// 由本服务代为执行（PTY），并在网页终端面板实时展示执行情况。
	// nil=默认 true；显式 false 回退为 agent 内部执行（仅聊天流展示摘要）。
	TerminalEnabled *bool `yaml:"terminal_enabled"`
}

// TerminalBridgeEnabled 返回是否向 agent 声明 ACP terminal 能力；未配置时默认 true。
func (a AgentsConfig) TerminalBridgeEnabled() bool {
	if a.TerminalEnabled == nil {
		return true
	}
	return *a.TerminalEnabled
}

// FailedTaskAutoRetryOnceEnabled 返回失败任务是否自动重试一次；未配置时默认 true。
func (a AgentsConfig) FailedTaskAutoRetryOnceEnabled() bool {
	if a.FailedTaskAutoRetryOnce == nil {
		return true
	}
	return *a.FailedTaskAutoRetryOnce
}

// SubAgentsConfig 配置 subagent 扫描目录（markdown 文件：frontmatter 含 name/description/model/tools，正文当 system_prompt）。
type SubAgentsConfig struct {
	// UserDirs 用户级 subagents 根目录（绝对路径或 ~/ 开头），默认 ~/.agents/agents。
	UserDirs []string `yaml:"user_dirs"`
	// ProjectDirs 项目级 subagents 相对工作区 cwd 的子目录，默认 .agents/agents。
	ProjectDirs []string `yaml:"project_dirs"`
}

// MCPConfig 配置全局共享的 MCP server 列表来源（标准 mcpServers JSON 格式）。
type MCPConfig struct {
	// ConfigPath 是 MCP server 配置文件路径，默认 ~/.agents/mcp.json。
	// 该文件中的 mcpServers 会注入给所有 agent 会话。
	ConfigPath string `yaml:"config_path"`
}

// SkillsConfig 配置 Agent Skills 扫描目录（agentskills.io 规范）。
type SkillsConfig struct {
	// UserDirs 用户级 skills 根目录（绝对路径或 ~/ 开头），默认 ~/.claude/skills。
	UserDirs []string `yaml:"user_dirs"`
	// ProjectDirs 项目级 skills 相对工作区 cwd 的子目录，默认 .claude/skills、.agents/skills。
	ProjectDirs []string `yaml:"project_dirs"`
}

// CommandsConfig 配置 Slash Command 扫描目录（Claude Code 规范：递归 *.md，支持子目录与符号链接）。
type CommandsConfig struct {
	// UserDirs 用户级 commands 根目录，默认 ~/.claude/commands。
	UserDirs []string `yaml:"user_dirs"`
	// ProjectDirs 项目级 commands 相对 cwd 的子目录，默认 .claude/commands。
	ProjectDirs []string `yaml:"project_dirs"`
}

// RulesConfig 配置 Rule 扫描路径（目录或单个文件，如 ~/.claude/CLAUDE.md）。
type RulesConfig struct {
	// UserDirs 用户级 rules 路径（目录或 .md/.mdc 文件），默认 ~/.cursor/rules、~/.claude/CLAUDE.md。
	UserDirs []string `yaml:"user_dirs"`
	// ProjectDirs 项目级 rules 相对 cwd 的路径，默认 .cursor/rules、CLAUDE.md。
	ProjectDirs []string `yaml:"project_dirs"`
}

type WorkspaceConfig struct {
	DefaultMode   string `yaml:"default_mode"`
	TempDirPrefix string `yaml:"temp_dir_prefix"`
	// SessionDir 是 temporary 模式会话工作区的存放根目录。
	// 默认 ~/.openNexus/session，仅在删除工作区时清理，不依赖系统清理临时目录。
	SessionDir string `yaml:"session_dir"`
	// MetaDir 是各工作区管理数据（tasks.json、执行记录、上传文件等）的存放根目录，
	// 与 agent 工作目录（cwd）分离，避免污染用户代码仓库。默认 ~/.openNexus/workspaces。
	MetaDir string `yaml:"meta_dir"`
}

type ClaudeCodeConfig struct {
	Enabled   bool          `yaml:"enabled"`
	Command   string        `yaml:"command"`
	Args      []string      `yaml:"args"`
	APIKeyEnv string        `yaml:"api_key_env"`
	Timeout   time.Duration `yaml:"timeout"`
}

// ResolveConfigPath 按优先级解析配置文件路径：
// CONFIG_PATH → ~/.openNexus/config.yaml（存在时）→ ./config.yaml
func ResolveConfigPath() string {
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".openNexus", "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "config.yaml"
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件: %w", err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件: %w", err)
	}
	cfg.applyEnv()
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("JWT_SECRET"); v != "" {
		c.JWT.Secret = v
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			c.Server.Port = port
		}
	}
	if v := os.Getenv("DATABASE_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("AGENTS_WORKSPACE_DEFAULT_MODE"); v != "" {
		c.Agents.Workspace.DefaultMode = v
	}
	if v := os.Getenv("AGENTS_WORKSPACE_SESSION_DIR"); v != "" {
		c.Agents.Workspace.SessionDir = v
	}
	if v := os.Getenv("AGENTS_WORKSPACE_META_DIR"); v != "" {
		c.Agents.Workspace.MetaDir = v
	}
	if v := os.Getenv("AGENTS_SKILLS_USER_DIRS"); v != "" {
		c.Agents.Skills.UserDirs = splitCommaList(v)
	}
	if v := os.Getenv("AGENTS_COMMANDS_USER_DIRS"); v != "" {
		c.Agents.Commands.UserDirs = splitCommaList(v)
	}
	if v := os.Getenv("AGENTS_RULES_USER_DIRS"); v != "" {
		c.Agents.Rules.UserDirs = splitCommaList(v)
	}
	if v := os.Getenv("AGENTS_SUBAGENTS_USER_DIRS"); v != "" {
		c.Agents.SubAgents.UserDirs = splitCommaList(v)
	}
	if v := os.Getenv("CLAUDE_CODE_COMMAND"); v != "" {
		c.Agents.ClaudeCode.Command = v
	}
	if v := os.Getenv("WEB_DIST"); v != "" {
		c.Server.WebDist = v
	}
	if v := os.Getenv("SERVER_MODE"); v != "" {
		c.Server.Mode = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
}

func (c *Config) Validate() error {
	if c.Server.Mode == "" {
		c.Server.Mode = "debug"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Server.WebDist == "" {
		c.Server.WebDist = "./web/dist"
	}
	if c.JWT.AccessTTL <= 0 {
		c.JWT.AccessTTL = 15 * time.Minute
	}
	if c.JWT.RefreshTTL <= 0 {
		c.JWT.RefreshTTL = 168 * time.Hour
	}
	if c.Password.BcryptCost == 0 {
		c.Password.BcryptCost = 12
	}
	if len(c.JWT.Secret) < 32 {
		return fmt.Errorf("JWT_SECRET 长度必须 >= 32 字节，当前 %d", len(c.JWT.Secret))
	}
	if c.Agents.Workspace.DefaultMode == "" {
		c.Agents.Workspace.DefaultMode = "temporary"
	}
	if c.Agents.Workspace.DefaultMode != "external" && c.Agents.Workspace.DefaultMode != "temporary" {
		return fmt.Errorf("agents.workspace.default_mode 必须是 external 或 temporary，当前 %q", c.Agents.Workspace.DefaultMode)
	}
	if c.Agents.Workspace.TempDirPrefix == "" {
		c.Agents.Workspace.TempDirPrefix = "opennexus-"
	}
	if err := c.resolveDataPaths(); err != nil {
		return err
	}
	if c.Agents.ClaudeCode.Command == "" {
		c.Agents.ClaudeCode.Command = "npx"
	}
	if len(c.Agents.ClaudeCode.Args) == 0 {
		c.Agents.ClaudeCode.Args = []string{"-y", "@agentclientprotocol/claude-agent-acp@latest"}
	}
	if c.Agents.ClaudeCode.APIKeyEnv == "" {
		c.Agents.ClaudeCode.APIKeyEnv = "ANTHROPIC_API_KEY"
	}
	if c.Agents.ClaudeCode.Timeout <= 0 {
		c.Agents.ClaudeCode.Timeout = 300 * time.Second
	}
	if err := c.Agents.Skills.normalize(); err != nil {
		return err
	}
	if err := c.Agents.Commands.normalize(); err != nil {
		return err
	}
	if err := c.Agents.Rules.normalize(); err != nil {
		return err
	}
	if err := c.Agents.SubAgents.normalize(); err != nil {
		return err
	}
	if err := c.Agents.MCP.normalize(); err != nil {
		return err
	}
	if err := c.Agents.Selector.normalize(); err != nil {
		return err
	}
	if err := c.Debug.ACP.normalize(); err != nil {
		return err
	}
	c.Permissions.normalize()
	return nil
}

// normalize 填充 ACP debug 目录默认值并展开 ~。
func (c *ACPDebugConfig) normalize() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录以设置 acp-debug 路径: %w", err)
	}
	if c.Dir == "" {
		c.Dir = filepath.Join(home, ".openNexus", "acp-debug")
		return nil
	}
	abs, err := expandPath(c.Dir)
	if err != nil {
		return fmt.Errorf("debug.acp.dir 无效: %w", err)
	}
	c.Dir = abs
	return nil
}

// resolveDataPaths 将 database.path、session_dir 与 meta_dir 默认到 ~/.openNexus 并展开 ~。
func (c *Config) resolveDataPaths() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录以设置数据路径: %w", err)
	}
	if c.Database.Path == "" {
		c.Database.Path = filepath.Join(home, ".openNexus", "opennexus.db")
	} else if c.Database.Path != ":memory:" {
		abs, err := expandPath(c.Database.Path)
		if err != nil {
			return fmt.Errorf("database.path 无效: %w", err)
		}
		c.Database.Path = abs
	}
	if c.Agents.Workspace.SessionDir == "" {
		c.Agents.Workspace.SessionDir = filepath.Join(home, ".openNexus", "session")
	} else {
		abs, err := expandPath(c.Agents.Workspace.SessionDir)
		if err != nil {
			return fmt.Errorf("session_dir 无效: %w", err)
		}
		c.Agents.Workspace.SessionDir = abs
	}
	if c.Agents.Workspace.MetaDir == "" {
		c.Agents.Workspace.MetaDir = filepath.Join(home, ".openNexus", "workspaces")
	} else {
		abs, err := expandPath(c.Agents.Workspace.MetaDir)
		if err != nil {
			return fmt.Errorf("meta_dir 无效: %w", err)
		}
		c.Agents.Workspace.MetaDir = abs
	}
	return nil
}

// normalize 填充 skills 默认值并展开路径。
func (s *SkillsConfig) normalize() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录以设置 skills 路径: %w", err)
	}
	if len(s.UserDirs) == 0 {
		s.UserDirs = []string{filepath.Join(home, ".claude", "skills")}
	} else {
		resolved := make([]string, 0, len(s.UserDirs))
		for _, p := range s.UserDirs {
			abs, err := expandPath(p)
			if err != nil {
				return fmt.Errorf("skills.user_dirs 路径 %q 无效: %w", p, err)
			}
			resolved = append(resolved, abs)
		}
		s.UserDirs = resolved
	}
	if len(s.ProjectDirs) == 0 {
		s.ProjectDirs = []string{".claude/skills", ".agents/skills"}
	}
	return nil
}

// normalize 填充 commands 默认值并展开路径（Claude Code：~/.claude/commands、.claude/commands）。
func (c *CommandsConfig) normalize() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录以设置 commands 路径: %w", err)
	}
	if len(c.UserDirs) == 0 {
		c.UserDirs = []string{filepath.Join(home, ".claude", "commands")}
	} else {
		resolved := make([]string, 0, len(c.UserDirs))
		for _, p := range c.UserDirs {
			abs, err := expandPath(p)
			if err != nil {
				return fmt.Errorf("commands.user_dirs 路径 %q 无效: %w", p, err)
			}
			resolved = append(resolved, abs)
		}
		c.UserDirs = resolved
	}
	if len(c.ProjectDirs) == 0 {
		c.ProjectDirs = []string{".claude/commands"}
	}
	return nil
}

// normalize 填充 rules 默认值并展开路径。
func (r *RulesConfig) normalize() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录以设置 rules 路径: %w", err)
	}
	if len(r.UserDirs) == 0 {
		r.UserDirs = []string{
			filepath.Join(home, ".cursor", "rules"),
			filepath.Join(home, ".claude", "CLAUDE.md"),
		}
	} else {
		resolved := make([]string, 0, len(r.UserDirs))
		for _, p := range r.UserDirs {
			abs, err := expandPath(p)
			if err != nil {
				return fmt.Errorf("rules.user_dirs 路径 %q 无效: %w", p, err)
			}
			resolved = append(resolved, abs)
		}
		r.UserDirs = resolved
	}
	if len(r.ProjectDirs) == 0 {
		r.ProjectDirs = []string{".cursor/rules", "CLAUDE.md"}
	}
	return nil
}

// normalize 填充 subagents 默认值并展开路径。
func (s *SubAgentsConfig) normalize() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录以设置 subagents 路径: %w", err)
	}
	if len(s.UserDirs) == 0 {
		s.UserDirs = []string{filepath.Join(home, ".agents", "agents")}
	} else {
		resolved := make([]string, 0, len(s.UserDirs))
		for _, p := range s.UserDirs {
			abs, err := expandPath(p)
			if err != nil {
				return fmt.Errorf("subagents.user_dirs 路径 %q 无效: %w", p, err)
			}
			resolved = append(resolved, abs)
		}
		s.UserDirs = resolved
	}
	if len(s.ProjectDirs) == 0 {
		s.ProjectDirs = []string{".agents/agents"}
	}
	return nil
}

// normalize 填充 MCP 配置文件路径默认值并展开 ~。
func (m *MCPConfig) normalize() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户主目录以设置 MCP 配置路径: %w", err)
	}
	if m.ConfigPath == "" {
		m.ConfigPath = filepath.Join(home, ".agents", "mcp.json")
		return nil
	}
	abs, err := expandPath(m.ConfigPath)
	if err != nil {
		return fmt.Errorf("mcp.config_path 路径 %q 无效: %w", m.ConfigPath, err)
	}
	m.ConfigPath = abs
	return nil
}

func expandPath(p string) (string, error) {
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if len(p) >= 2 && p[0] == '~' && (p[1] == '/' || p[1] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[2:])
	}
	return filepath.Abs(p)
}

func splitCommaList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
