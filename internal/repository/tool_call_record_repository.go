package repository

import (
	"time"

	"gorm.io/gorm"

	"opennexus/internal/models"
)

// ToolCallRecordRepository 管理工具调用历史记录。
type ToolCallRecordRepository struct {
	db *gorm.DB
}

func NewToolCallRecordRepository(db *gorm.DB) *ToolCallRecordRepository {
	return &ToolCallRecordRepository{db: db}
}

func (r *ToolCallRecordRepository) Create(rec *models.ToolCallRecord) error {
	return r.db.Create(rec).Error
}

// ToolCallUpdateFields 是 tool_call_update 增量合并后的可更新字段（nil/空 表示本轮无变化）。
type ToolCallUpdateFields struct {
	Status     string
	ExitCode   *int
	TerminalID string
	// Title / Command：部分 agent 在 update 里才补全 rawInput
	Title   string
	Command string
}

// ApplyUpdate 按 (dbSessionID, toolCallID) 更新记录；终态时写 FinishedAt。
func (r *ToolCallRecordRepository) ApplyUpdate(dbSessionID uint, toolCallID string, f ToolCallUpdateFields) error {
	updates := map[string]any{}
	if f.Status != "" {
		updates["status"] = f.Status
		if f.Status == models.ToolCallStatusCompleted || f.Status == models.ToolCallStatusFailed {
			now := time.Now()
			updates["finished_at"] = &now
		}
	}
	if f.ExitCode != nil {
		updates["exit_code"] = f.ExitCode
	}
	if f.TerminalID != "" {
		updates["terminal_id"] = f.TerminalID
	}
	if f.Title != "" {
		updates["title"] = f.Title
	}
	if f.Command != "" {
		updates["command"] = f.Command
	}
	if len(updates) == 0 {
		return nil
	}
	return r.db.Model(&models.ToolCallRecord{}).
		Where("db_session_id = ? AND tool_call_id = ?", dbSessionID, toolCallID).
		Updates(updates).Error
}

// FinishByTerminal 终端退出时按 terminal_id 回填退出码与终态；返回命中的行数。
// 命中 0 行说明该终端没有关联的 tool_call 记录（agent 未内嵌 terminal content），
// 调用方可兜底创建独立记录。
func (r *ToolCallRecordRepository) FinishByTerminal(dbSessionID uint, terminalID string, exitCode *int, status string) (int64, error) {
	now := time.Now()
	res := r.db.Model(&models.ToolCallRecord{}).
		Where("db_session_id = ? AND terminal_id = ?", dbSessionID, terminalID).
		Updates(map[string]any{"exit_code": exitCode, "status": status, "finished_at": &now})
	return res.RowsAffected, res.Error
}

// ToolCallRecordWithSession 是列表查询结果：记录 + 所属会话摘要信息。
type ToolCallRecordWithSession struct {
	models.ToolCallRecord
	SessionTitle string `json:"session_title"`
	AgentType    string `json:"agent_type"`
}

// ToolCallListOptions 是列表过滤条件。
type ToolCallListOptions struct {
	Kind        string // 为空不过滤
	DBSessionID uint   // 0 不过滤
	Limit       int
	Offset      int
}

// ListByUser 按用户分页查询工具调用记录（倒序），并联查会话标题与 agent 类型。
func (r *ToolCallRecordRepository) ListByUser(userID uint, opts ToolCallListOptions) ([]ToolCallRecordWithSession, int64, error) {
	q := r.db.Model(&models.ToolCallRecord{}).Where("tool_call_records.user_id = ?", userID)
	if opts.Kind != "" {
		q = q.Where("tool_call_records.kind = ?", opts.Kind)
	}
	if opts.DBSessionID != 0 {
		q = q.Where("tool_call_records.db_session_id = ?", opts.DBSessionID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	var rows []ToolCallRecordWithSession
	err := q.
		Select("tool_call_records.*, sessions.title AS session_title, sessions.agent_type AS agent_type").
		Joins("LEFT JOIN sessions ON sessions.id = tool_call_records.db_session_id").
		Order("tool_call_records.id DESC").
		Limit(limit).Offset(opts.Offset).
		Find(&rows).Error
	return rows, total, err
}
