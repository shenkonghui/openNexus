package models

import "time"

// TaskSettings 是用户任务自动打标签 / 自动生成标题的配置。
type TaskSettings struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	UserID      uint   `gorm:"uniqueIndex;not null" json:"user_id"`
	AutoTagEnabled   bool   `gorm:"not null;default:false" json:"auto_tag_enabled"`
	AutoTitleEnabled bool   `gorm:"not null;default:true" json:"auto_title_enabled"`
	// AgentType / ModelValue 用于分类和生成标题的临时会话（RunPromptOnce）。
	AgentType  string `gorm:"size:64" json:"agent_type"`
	ModelValue string `gorm:"size:128" json:"model_value"`
	// Tags 是预定义标签 JSON 数组，如 ["后端","前端","mysql"]。
	Tags string `gorm:"type:text" json:"tags"`
	// TagPrompt / TitlePrompt 是自定义提示词模板（空则用默认）。
	TagPrompt   string `gorm:"type:text" json:"tag_prompt"`
	TitlePrompt string `gorm:"type:text" json:"title_prompt"`
	// DocEditPrompt 是文档编辑助手的 system prompt 模板（空则用 services.DefaultDocEditPrompt）。
	// 支持占位符 {{docPath}}（当前文档绝对路径）。
	DocEditPrompt string `gorm:"type:text" json:"doc_edit_prompt"`
	// Review 全局默认配置：编排任务执行完后由 reviewer agent 审查是否真正完成，
	// 未通过则把审查意见追加回原会话继续修复；单个任务可用 TaskReviewConfig 覆盖。
	ReviewEnabled    bool   `gorm:"not null;default:false" json:"review_enabled"`
	ReviewAgentType  string `gorm:"size:64" json:"review_agent_type"`
	ReviewModelValue string `gorm:"size:128" json:"review_model_value"`
	// ReviewMaxRounds 最大 review 轮数；<=0 视为默认值 2。
	ReviewMaxRounds int `gorm:"not null;default:0" json:"review_max_rounds"`
	// ReviewPrompt 是审查提示词模板（空则用 services.DefaultTaskReviewPrompt）。
	ReviewPrompt string `gorm:"type:text" json:"review_prompt"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
