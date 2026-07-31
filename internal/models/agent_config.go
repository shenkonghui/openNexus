package models

import "time"

// AgentConfig 是通过设置页面添加的本地 ACP agent 配置（全局共享）。
// 与 config.yaml 内置 agent 互补，运行时动态注册到 registry 与 acp service。
type AgentConfig struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Type        string    `gorm:"uniqueIndex;size:64;not null" json:"type"`
	DisplayName string    `gorm:"size:128;not null" json:"display_name"`
	Description string    `gorm:"size:256" json:"description"`
	Command     string    `gorm:"size:256;not null" json:"command"`
	Args        string    `gorm:"type:text" json:"args"`  // JSON 编码的 []string
	Env         string    `gorm:"type:text" json:"env"`   // JSON 编码的 map[string]string，启动 agent 进程时注入（如 HTTPS_PROXY 等代理变量）
	APIKeyEnv   string    `gorm:"size:64" json:"api_key_env"`
	Timeout     string    `gorm:"size:32" json:"timeout"` // time.Duration 字符串，如 "300s"
	ConfigDirs  string    `gorm:"type:text" json:"config_dirs"` // JSON 编码的 []string，沙箱模式下 agent 配置/登录态可写目录（支持 ~ 前缀）
	Enabled     *bool     `gorm:"not null;default:false" json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
