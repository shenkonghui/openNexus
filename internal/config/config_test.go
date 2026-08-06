package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_FromYAML(t *testing.T) {
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, 期望 9090", cfg.Server.Port)
	}
	if cfg.Database.Path != "./data/test.db" {
		t.Errorf("Database.Path = %q, 期望 ./data/test.db", cfg.Database.Path)
	}
	if cfg.JWT.AccessTTL != 15*time.Minute {
		t.Errorf("JWT.AccessTTL = %v, 期望 15m", cfg.JWT.AccessTTL)
	}
	if cfg.Password.BcryptCost != 10 {
		t.Errorf("Password.BcryptCost = %d, 期望 10", cfg.Password.BcryptCost)
	}
	if cfg.Server.WebDist != "./web/dist" {
		t.Errorf("Server.WebDist = %q, 期望 ./web/dist", cfg.Server.WebDist)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("JWT_SECRET", "env-secret-from-env-var-long-enough")
	t.Setenv("SERVER_PORT", "7070")
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.JWT.Secret != "env-secret-from-env-var-long-enough" {
		t.Errorf("JWT.Secret 未被环境变量覆盖: %q", cfg.JWT.Secret)
	}
	if cfg.Server.Port != 7070 {
		t.Errorf("Server.Port 未被环境变量覆盖: %d", cfg.Server.Port)
	}
}

func TestValidate_SecretTooShort(t *testing.T) {
	cfg := &Config{JWT: JWTConfig{Secret: "short"}}
	if err := cfg.Validate(); err == nil {
		t.Error("期望 secret 过短时返回错误，实际无错误")
	}
}

func TestValidate_OK(t *testing.T) {
	cfg := &Config{JWT: JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("期望校验通过，实际错误: %v", err)
	}
	if cfg.Server.WebDist != "./web/dist" {
		t.Errorf("WebDist 默认值 = %q, 期望 ./web/dist", cfg.Server.WebDist)
	}
	if cfg.Server.Mode != "debug" {
		t.Errorf("Mode 默认值 = %q, 期望 debug", cfg.Server.Mode)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level 默认值 = %q, 期望 info", cfg.Logging.Level)
	}
}

func TestLoad_WebDist_EnvOverride(t *testing.T) {
	t.Setenv("WEB_DIST", "/custom/web-dist")
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 错误: %v", err)
	}
	if cfg.Server.WebDist != "/custom/web-dist" {
		t.Errorf("WebDist 未被环境变量覆盖: %q", cfg.Server.WebDist)
	}
}

func TestLoad_AgentsConfig(t *testing.T) {
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.Agents.Workspace.DefaultMode != "temporary" {
		t.Errorf("Workspace.DefaultMode = %q, 期望 temporary", cfg.Agents.Workspace.DefaultMode)
	}
	if cfg.Agents.Workspace.TempDirPrefix != "test-" {
		t.Errorf("Workspace.TempDirPrefix = %q, 期望 test-", cfg.Agents.Workspace.TempDirPrefix)
	}
	if cfg.Agents.Workspace.SessionDir != "" {
		t.Errorf("Workspace.SessionDir 未设置时应为空，实际 %q", cfg.Agents.Workspace.SessionDir)
	}
	if !cfg.Agents.ClaudeCode.Enabled {
		t.Error("ClaudeCode.Enabled 期望 true")
	}
	if cfg.Agents.ClaudeCode.Command != "npx" {
		t.Errorf("ClaudeCode.Command = %q, 期望 npx", cfg.Agents.ClaudeCode.Command)
	}
	if cfg.Agents.ClaudeCode.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("ClaudeCode.APIKeyEnv = %q, 期望 ANTHROPIC_API_KEY", cfg.Agents.ClaudeCode.APIKeyEnv)
	}
	if cfg.Agents.ClaudeCode.Timeout != 60*time.Second {
		t.Errorf("ClaudeCode.Timeout = %v, 期望 60s", cfg.Agents.ClaudeCode.Timeout)
	}
}

func TestLoad_AgentsConfig_EnvOverride(t *testing.T) {
	t.Setenv("AGENTS_WORKSPACE_DEFAULT_MODE", "external")
	t.Setenv("CLAUDE_CODE_COMMAND", "/usr/local/bin/npx")
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.Agents.Workspace.DefaultMode != "external" {
		t.Errorf("DefaultMode 未被环境变量覆盖: %q", cfg.Agents.Workspace.DefaultMode)
	}
	if cfg.Agents.ClaudeCode.Command != "/usr/local/bin/npx" {
		t.Errorf("Command 未被环境变量覆盖: %q", cfg.Agents.ClaudeCode.Command)
	}
}

func TestValidate_WorkspaceDefaultMode_Invalid(t *testing.T) {
	cfg := &Config{
		JWT:    JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"},
		Agents: AgentsConfig{Workspace: WorkspaceConfig{DefaultMode: "invalid"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("期望 default_mode 非法时返回错误")
	}
}

func TestValidate_WorkspaceDefaultMode_OK(t *testing.T) {
	cfg := &Config{
		JWT:    JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"},
		Agents: AgentsConfig{Workspace: WorkspaceConfig{DefaultMode: "external"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("期望 external 校验通过，实际: %v", err)
	}
}

func TestValidate_WorkspaceSessionDir_Default(t *testing.T) {
	cfg := &Config{
		JWT:    JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"},
		Agents: AgentsConfig{Workspace: WorkspaceConfig{DefaultMode: "external"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("获取主目录失败: %v", err)
	}
	expected := filepath.Join(home, ".openNexus", "session")
	if cfg.Agents.Workspace.SessionDir != expected {
		t.Errorf("SessionDir = %q, 期望 %q", cfg.Agents.Workspace.SessionDir, expected)
	}
}

func TestValidate_WorkspaceMetaDir_Default(t *testing.T) {
	cfg := &Config{
		JWT:    JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"},
		Agents: AgentsConfig{Workspace: WorkspaceConfig{DefaultMode: "external"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("获取主目录失败: %v", err)
	}
	expected := filepath.Join(home, ".openNexus", "workspaces")
	if cfg.Agents.Workspace.MetaDir != expected {
		t.Errorf("MetaDir = %q, 期望 %q", cfg.Agents.Workspace.MetaDir, expected)
	}
}

func TestValidate_DatabasePath_Default(t *testing.T) {
	cfg := &Config{
		JWT:    JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"},
		Agents: AgentsConfig{Workspace: WorkspaceConfig{DefaultMode: "temporary"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".openNexus", "opennexus.db")
	if cfg.Database.Path != expected {
		t.Errorf("Database.Path = %q, 期望 %q", cfg.Database.Path, expected)
	}
}

func TestAgentsConfig_FailedTaskAutoRetryOnceEnabled(t *testing.T) {
	var unset AgentsConfig
	if !unset.FailedTaskAutoRetryOnceEnabled() {
		t.Error("未配置时期望默认 true")
	}
	off := false
	if (AgentsConfig{FailedTaskAutoRetryOnce: &off}).FailedTaskAutoRetryOnceEnabled() {
		t.Error("显式 false 时期望 false")
	}
	on := true
	if !(AgentsConfig{FailedTaskAutoRetryOnce: &on}).FailedTaskAutoRetryOnceEnabled() {
		t.Error("显式 true 时期望 true")
	}
}

func TestRulesConfig_PromptPrefixEnabled(t *testing.T) {
	var unset RulesConfig
	if !unset.PromptPrefixEnabled() {
		t.Error("未配置时期望默认 true")
	}
	off := false
	if (RulesConfig{PromptPrefix: &off}).PromptPrefixEnabled() {
		t.Error("显式 false 时期望 false")
	}
	on := true
	if !(RulesConfig{PromptPrefix: &on}).PromptPrefixEnabled() {
		t.Error("显式 true 时期望 true")
	}
}

func TestRulesConfig_MetaSystemPromptEnabled(t *testing.T) {
	var unset RulesConfig
	if !unset.MetaSystemPromptEnabled() {
		t.Error("未配置时期望默认 true")
	}
	off := false
	if (RulesConfig{MetaSystemPrompt: &off}).MetaSystemPromptEnabled() {
		t.Error("显式 false 时期望 false")
	}
	on := true
	if !(RulesConfig{MetaSystemPrompt: &on}).MetaSystemPromptEnabled() {
		t.Error("显式 true 时期望 true")
	}
}

func TestResolveConfigPath_Env(t *testing.T) {
	t.Setenv("CONFIG_PATH", "/tmp/custom-opennexus-config.yaml")
	if got := ResolveConfigPath(); got != "/tmp/custom-opennexus-config.yaml" {
		t.Errorf("ResolveConfigPath = %q, 期望 /tmp/custom-opennexus-config.yaml", got)
	}
}

func TestResolveConfigPath_Fallback(t *testing.T) {
	t.Setenv("CONFIG_PATH", "")
	got := ResolveConfigPath()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	userCfg := filepath.Join(home, ".openNexus", "config.yaml")
	if _, err := os.Stat(userCfg); err == nil {
		if got != userCfg {
			t.Errorf("ResolveConfigPath = %q, 期望 %q", got, userCfg)
		}
		return
	}
	if got != "config.yaml" {
		t.Errorf("ResolveConfigPath = %q, 期望 config.yaml", got)
	}
}

func TestValidate_SkillsUserDirsDefault(t *testing.T) {
	cfg := &Config{JWT: JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".claude", "skills")
	builtinDir := filepath.Join(home, ".openNexus", "builtin-skills")
	// 默认 UserDirs = [~/.claude/skills, ~/.openNexus/builtin-skills]（后者为内置 skill 目录，始终追加）
	if len(cfg.Agents.Skills.UserDirs) != 2 || cfg.Agents.Skills.UserDirs[0] != expected || cfg.Agents.Skills.UserDirs[1] != builtinDir {
		t.Errorf("UserDirs = %v, 期望 [%q, %q]", cfg.Agents.Skills.UserDirs, expected, builtinDir)
	}
	if len(cfg.Agents.Skills.ProjectDirs) != 2 {
		t.Errorf("ProjectDirs = %v, 期望 2 项", cfg.Agents.Skills.ProjectDirs)
	}
}

func TestValidate_CommandsUserDirsDefault(t *testing.T) {
	cfg := &Config{JWT: JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".claude", "commands")
	if len(cfg.Agents.Commands.UserDirs) != 1 || cfg.Agents.Commands.UserDirs[0] != expected {
		t.Errorf("Commands.UserDirs = %v, 期望 [%q]", cfg.Agents.Commands.UserDirs, expected)
	}
	if len(cfg.Agents.Commands.ProjectDirs) != 1 || cfg.Agents.Commands.ProjectDirs[0] != ".claude/commands" {
		t.Errorf("Commands.ProjectDirs = %v", cfg.Agents.Commands.ProjectDirs)
	}
}

func TestValidate_RulesUserDirsDefault(t *testing.T) {
	cfg := &Config{JWT: JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wantUser := []string{
		filepath.Join(home, ".cursor", "rules"),
		filepath.Join(home, ".claude", "CLAUDE.md"),
	}
	if len(cfg.Agents.Rules.UserDirs) != len(wantUser) {
		t.Fatalf("Rules.UserDirs = %v, 期望 %v", cfg.Agents.Rules.UserDirs, wantUser)
	}
	for i, w := range wantUser {
		if cfg.Agents.Rules.UserDirs[i] != w {
			t.Errorf("Rules.UserDirs[%d] = %q, 期望 %q", i, cfg.Agents.Rules.UserDirs[i], w)
		}
	}
	wantProject := []string{".cursor/rules", "CLAUDE.md"}
	if len(cfg.Agents.Rules.ProjectDirs) != len(wantProject) {
		t.Fatalf("Rules.ProjectDirs = %v, 期望 %v", cfg.Agents.Rules.ProjectDirs, wantProject)
	}
}

func TestLoad_LogLevel_EnvOverride(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 错误: %v", err)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level 未被环境变量覆盖: %q", cfg.Logging.Level)
	}
}

func TestValidate_WorkspaceSessionDir_EnvOverride(t *testing.T) {
	t.Setenv("JWT_SECRET", "env-secret-from-env-var-long-enough")
	t.Setenv("AGENTS_WORKSPACE_SESSION_DIR", "/custom/session-dir")
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 错误: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	if cfg.Agents.Workspace.SessionDir != "/custom/session-dir" {
		t.Errorf("SessionDir 未被环境变量覆盖: %q", cfg.Agents.Workspace.SessionDir)
	}
}

func TestValidate_WorkspaceMetaDir_EnvOverride(t *testing.T) {
	t.Setenv("JWT_SECRET", "env-secret-from-env-var-long-enough")
	t.Setenv("AGENTS_WORKSPACE_META_DIR", "/custom/meta-dir")
	cfg, err := Load("testdata/config_test.yaml")
	if err != nil {
		t.Fatalf("Load 错误: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	if cfg.Agents.Workspace.MetaDir != "/custom/meta-dir" {
		t.Errorf("MetaDir 未被环境变量覆盖: %q", cfg.Agents.Workspace.MetaDir)
	}
}

func TestValidate_MCPConfigPath_Default(t *testing.T) {
	cfg := &Config{JWT: JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".agents", "mcp.json")
	if cfg.Agents.MCP.ConfigPath != expected {
		t.Errorf("MCP.ConfigPath = %q, 期望 %q", cfg.Agents.MCP.ConfigPath, expected)
	}
}

func TestValidate_MCPConfigPath_RelativeExpand(t *testing.T) {
	cfg := &Config{
		JWT:    JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"},
		Agents: AgentsConfig{MCP: MCPConfig{ConfigPath: "~/custom/mcp.json"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 错误: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, "custom", "mcp.json")
	if cfg.Agents.MCP.ConfigPath != expected {
		t.Errorf("MCP.ConfigPath = %q, 期望 %q", cfg.Agents.MCP.ConfigPath, expected)
	}
}
