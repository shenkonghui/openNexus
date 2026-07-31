package repository

import (
	"errors"
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
	return applyUpdateTx(r.db, dbSessionID, toolCallID, f)
}

// ToolCallBatchUpdate 是批量更新的单条条目。
type ToolCallBatchUpdate struct {
	ToolCallID string
	Fields     ToolCallUpdateFields
}

// ApplyUpdatesBatch 在单个事务中批量应用多条增量（flush 攒批时调用），
// 把「每 toolCallId 一次 sqlite 写事务」合并为一次，降低热路径落库开销。
func (r *ToolCallRecordRepository) ApplyUpdatesBatch(dbSessionID uint, updates []ToolCallBatchUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	if len(updates) == 1 {
		return applyUpdateTx(r.db, dbSessionID, updates[0].ToolCallID, updates[0].Fields)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, u := range updates {
			if err := applyUpdateTx(tx, dbSessionID, u.ToolCallID, u.Fields); err != nil {
				return err
			}
		}
		return nil
	})
}

func applyUpdateTx(tx *gorm.DB, dbSessionID uint, toolCallID string, f ToolCallUpdateFields) error {
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
	return tx.Model(&models.ToolCallRecord{}).
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

// FinishByCommand 终端退出时按命令文本对齐未关联终端的 execute 记录：
// agent 委托终端执行但未内嵌 terminal content 时，tool_call 记录无 terminal_id，
// 靠「同会话 + 相同命令 + 未关联终端 + 无退出码」匹配最近一条回填，避免兜底新建造成重复。
// 返回是否命中。
func (r *ToolCallRecordRepository) FinishByCommand(dbSessionID uint, command, terminalID string, exitCode *int, status string) (bool, error) {
	if command == "" {
		return false, nil
	}
	var rec models.ToolCallRecord
	err := r.db.
		Where("db_session_id = ? AND kind = ? AND command = ? AND (terminal_id = '' OR terminal_id IS NULL) AND exit_code IS NULL",
			dbSessionID, "execute", command).
		Order("id DESC").First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	now := time.Now()
	err = r.db.Model(&models.ToolCallRecord{}).Where("id = ?", rec.ID).
		Updates(map[string]any{"terminal_id": terminalID, "exit_code": exitCode, "status": status, "finished_at": &now}).Error
	return err == nil, err
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
