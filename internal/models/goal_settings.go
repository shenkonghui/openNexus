package models

import "time"

// GoalSettings 是通用 goal 循环（/goal 命令）的用户级配置。
// 会话轮次结束后由评估器判断 goal 是否达成，未达成自动续轮。
type GoalSettings struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"uniqueIndex;not null" json:"user_id"`
	// AgentType / ModelValue 指定评估器使用的 agent 与模型（RunPromptOnce 临时会话）。
	// AgentType 为空时使用会话自身的 agent 评估。
	AgentType  string `gorm:"size:64" json:"agent_type"`
	ModelValue string `gorm:"size:128" json:"model_value"`
	// MaxTurns 单个 goal 最大自动续轮次数，0=默认（20）。
	MaxTurns int `gorm:"not null;default:0" json:"max_turns"`
	// MaxDurationMinutes 单个 goal 最长持续时间（分钟），0=默认（60）。
	MaxDurationMinutes int       `gorm:"not null;default:0" json:"max_duration_minutes"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}
