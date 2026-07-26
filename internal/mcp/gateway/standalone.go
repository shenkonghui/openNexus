package gatewaymcp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// StandaloneConfig 是独立二进制模式（cmd/gateway）的配置文件结构，
// 默认从 ~/.openNexus/gateway.yaml 读取。与主程序 config.yaml 解耦，
// 使网关可以脱离主 server 单独部署。
type StandaloneConfig struct {
	// Listen 监听地址，默认 :8090。
	Listen string `yaml:"listen"`
	// PublicBaseURL 对外可访问的 Base URL，用于拼 endpoint 与写入 mcp.json 条目。
	// 例如 http://192.168.1.10:8090。未设置时网关仍能跑，但 endpoint 字段为空。
	PublicBaseURL string `yaml:"public_base_url"`
	// Token 调用网关所需的 Bearer token。必填，未设置时启动失败。
	// 可用 `openssl rand -hex 32` 生成。
	Token string `yaml:"token"`
	// MCPConfigPath 全局 mcp.json 路径，默认 ~/.agents/mcp.json。
	// 网关从这里发现上游 server，并把自身条目写回此文件（若启用写入）。
	MCPConfigPath string `yaml:"mcp_config_path"`
	// AutoEnable 是否启动时自动把 opennexus-gateway 条目写入 mcp.json。
	// 默认 false：独立模式通常不需要再写回 mcp.json（mcp.json 可能由主程序管理）。
	// 设为 true 时，每次启动都会刷新 mcp.json 中的网关条目（url + token）。
	AutoEnable bool `yaml:"auto_enable"`
}

// DefaultStandaloneConfigPath 返回默认配置文件路径 ~/.openNexus/gateway.yaml。
func DefaultStandaloneConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户主目录: %w", err)
	}
	return filepath.Join(home, ".openNexus", "gateway.yaml"), nil
}

// LoadStandaloneConfig 从 path 读取并归一化独立模式配置。path 为空时用默认路径。
//
// 文件不存在时自动生成一份带随机 token 的模板到该路径，并返回 ErrConfigBootstrap：
// 调用方应提示用户"已生成模板，编辑后再次启动"。这样首次运行开箱即用，
// 不必让用户手敲 openssl rand -hex 32 再写文件。
func LoadStandaloneConfig(path string) (*StandaloneConfig, error) {
	if path == "" {
		var err error
		path, err = DefaultStandaloneConfigPath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if bootErr := bootstrapStandaloneConfig(path); bootErr != nil {
				return nil, fmt.Errorf("生成默认配置失败 (%s): %w", path, bootErr)
			}
			return nil, &ConfigBootstrapError{Path: path}
		}
		return nil, fmt.Errorf("读取网关配置失败 (%s): %w", path, err)
	}
	var cfg StandaloneConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析网关配置失败 (%s): %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ConfigBootstrapError 表示配置文件不存在已自动生成模板，需用户编辑后重启。
type ConfigBootstrapError struct{ Path string }

func (e *ConfigBootstrapError) Error() string {
	return "已生成默认配置模板，请编辑后再次启动: " + e.Path
}

// bootstrapStandaloneConfig 在 path 写入一份带随机 token 的配置模板。
// 父目录不存在会自动创建。已存在则不覆盖（调用方保证仅在 IsNotExist 时进入）。
func bootstrapStandaloneConfig(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建配置目录失败: %w", err)
		}
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	defaultMCP := filepath.Join(home, ".agents", "mcp.json")
	tmpl := strings.Join([]string{
		"# opennexus-gateway 独立模式配置",
		"# 首次启动自动生成；按需修改后再次运行 opennexus-gateway",
		"",
		"# 监听地址",
		"listen: \":8090\"",
		"",
		"# 对外可访问的 Base URL（用于拼 endpoint 写入 mcp.json）",
		"# 远程部署时改成 http://你的IP:8090；本机用可留空或 http://127.0.0.1:8090",
		"public_base_url: \"http://127.0.0.1:8090\"",
		"",
		"# 调用网关所需的 Bearer token（已自动生成，可自行替换）",
		"token: \"" + token + "\"",
		"",
		"# 全局 mcp.json 路径（网关从这里发现上游 server）",
		"mcp_config_path: \"" + defaultMCP + "\"",
		"",
		"# 是否启动时自动把 opennexus-gateway 条目写入 mcp.json",
		"# false: 仅当条目已存在时刷新（默认，避免与主程序互相覆盖）",
		"# true:  每次启动都写入/刷新条目",
		"auto_enable: false",
		"",
	}, "\n")
	return os.WriteFile(path, []byte(tmpl), 0o600)
}

// randomToken 生成 32 字节 hex 编码的随机 token。
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机 token 失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// normalize 校正缺省值并校验必填项。
func (c *StandaloneConfig) normalize() error {
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("gateway.yaml 中 token 未配置：请生成一个随机 token（如 openssl rand -hex 32）后写入")
	}
	c.Token = strings.TrimSpace(c.Token)
	if c.Listen == "" {
		c.Listen = ":8090"
	}
	if c.MCPConfigPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("无法定位用户主目录以默认 mcp_config_path: %w", err)
		}
		c.MCPConfigPath = filepath.Join(home, ".agents", "mcp.json")
	}
	c.PublicBaseURL = strings.TrimRight(strings.TrimSpace(c.PublicBaseURL), "/")
	return nil
}
