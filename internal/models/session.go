package models

import "time"

const (
	SessionStatusActive  = "active"
	SessionStatusClosed  = "closed"
	SessionStatusError   = "error"
	SessionStatusPending = "pending"

	// 会话来源：手动创建、定时任务、笔记自动分类
	// SessionSourceOrchestration 用于任务助手的管理会话（编排页 AI 对话）：
	// 不登记 tasks.json、不作为普通任务展示，侧边栏以「编排对话」记录入口呈现。
	// 编排任务执行创建的会话仍为 manual。
	SessionSourceManual        = "manual"
	SessionSourceScheduled     = "scheduled"
	SessionSourceClassify      = "classify"
	SessionSourceOrchestration = "orchestration"
)

type Session struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	SessionID string `gorm:"uniqueIndex;size:128;not null" json:"session_id"`
	// AgentSessionID 是 ACP agent 返回的 sessionId；创建时为空，激活/恢复时写入，不覆盖 SessionID。
	AgentSessionID string `gorm:"size:128;index" json:"agent_session_id"`
	AgentType      string `gorm:"size:64;not null" json:"agent_type"`
	Cwd            string `gorm:"size:512;not null" json:"-"` // 废弃，cwd 从 Workspace 获取
	Status         string `gorm:"size:32;not null;default:active" json:"status"`
	UserID         uint   `gorm:"index" json:"user_id"`
	WorkspaceMode  string `gorm:"size:32;not null" json:"-"` // 废弃
	TempDir        string `gorm:"size:512" json:"-"`         // 废弃
	// WorkspaceID 关联的工作区 ID（可选，向后兼容旧数据）
	WorkspaceID *uint     `gorm:"index" json:"workspace_id"`
	Workspace   Workspace `gorm:"foreignKey:WorkspaceID" json:"workspace,omitempty"`
	LastPrompt  string    `gorm:"type:text" json:"last_prompt"`
	// Title 是会话显示标题，首次对话后从 prompt 提取前若干字符生成。
	Title string `gorm:"size:128" json:"title"`
	// Source 标识会话来源：manual（手动创建）或 scheduled（定时任务创建）。
	Source string `gorm:"size:32;not null;default:manual" json:"source"`
	// Tags 是会话标签 JSON 数组，如 ["后端","mysql"]，由任务自动分类写入。
	Tags       string `gorm:"type:text" json:"tags"`
	ModelValue string `gorm:"size:128" json:"-"` // 用户选择的模型，在激活会话时应用
	// ParentSessionID 指向父会话的主键，用于表达父子（主/子任务）关系。
	// nil 表示顶级独立会话。由 MCP create_session/run_session_task 工具按需写入。
	ParentSessionID *uint `gorm:"index" json:"parent_session_id,omitempty"`
	// Yolo 为本任务独立开启 YOLO：未命中黑/白/询问名单时自动放行；名单仍全局生效。
	Yolo      bool       `gorm:"not null;default:false" json:"yolo"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `gorm:"index" json:"closed_at"`
}
