package models

import "time"

// 定时任务最近执行状态
// 注：TaskStatusRunning / TaskStatusFailed 已统一在 taskmanager_task.go 定义（语义一致），
// 此处仅保留定时任务独有的成功/跳过状态。
const (
	TaskStatusSuccess = "success"
	TaskStatusSkipped = "skipped"
)

// ScheduledTask 是定时任务配置。每个任务关联一个 Session（首次执行时创建），
// 每次 cron 触发在该 session 内追加一轮对话（用 execution_id 标记执行块）。
type ScheduledTask struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Name      string `gorm:"size:128;not null" json:"name"`
	AgentType   string `gorm:"size:64;not null" json:"agent_type"`
	WorkspaceID uint   `gorm:"index" json:"workspace_id"`
	Cwd         string `gorm:"size:512;not null" json:"cwd"`
	Prompt    string `gorm:"type:text;not null" json:"prompt"`
	CronExpr  string `gorm:"size:128;not null" json:"cron_expr"`
	Enabled   bool   `gorm:"not null;default:true" json:"enabled"`
	UserID    uint   `gorm:"index;not null" json:"user_id"`
	// TimeoutMinutes 是单次执行超时时间（分钟），超时则标记为失败。默认 5。
	TimeoutMinutes int `gorm:"not null;default:5" json:"timeout_minutes"`
	// ModelValue 是可选的模型配置值，执行时设置到会话的 model config option。
	// 为空则使用 agent 默认模型。
	ModelValue  string     `gorm:"size:128" json:"model_value"`
	SessionID   string     `gorm:"size:128" json:"session_id"`
	DBSessionID uint       `gorm:"index" json:"db_session_id"`
	LastRunAt   *time.Time `json:"last_run_at"`
	LastStatus  string     `gorm:"size:32" json:"last_status"`
	LastError   string     `gorm:"type:text" json:"last_error"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TaskExecution 记录定时任务单次执行的元数据（状态、时间、错误信息）。
type TaskExecution struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	TaskID      uint      `gorm:"index;not null" json:"task_id"`
	ExecutionID uint      `gorm:"not null" json:"execution_id"`
	Status      string    `gorm:"size:32;not null" json:"status"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	Error       string    `gorm:"type:text" json:"error"`
	CreatedAt   time.Time `json:"created_at"`
}
