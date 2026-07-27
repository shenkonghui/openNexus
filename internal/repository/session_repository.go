package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"opennexus/internal/models"
)

var ErrSessionNotFound = errors.New("会话不存在")

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(s *models.Session) error {
	return r.db.Create(s).Error
}

func (r *SessionRepository) FindBySessionID(sessionID string) (*models.Session, error) {
	var s models.Session
	if err := r.db.Where("session_id = ?", sessionID).First(&s).Error; err != nil {
		return nil, ErrSessionNotFound
	}
	return &s, nil
}

func (r *SessionRepository) FindByUserID(userID uint) ([]models.Session, error) {
	var sessions []models.Session
	err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&sessions).Error
	return sessions, err
}

// FindByUserIDAndSource 返回指定用户指定来源的会话。source 为空时等价于 FindByUserID。
func (r *SessionRepository) FindByUserIDAndSource(userID uint, source string) ([]models.Session, error) {
	var sessions []models.Session
	q := r.db.Where("user_id = ?", userID)
	if source != "" {
		q = q.Where("source = ?", source)
	}
	err := q.Order("created_at DESC").Find(&sessions).Error
	return sessions, err
}

func (r *SessionRepository) UpdateStatus(id uint, status string, closedAt *time.Time) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": status, "closed_at": closedAt}).Error
}

func (r *SessionRepository) UpdateLastPrompt(id uint, prompt string) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).
		Update("last_prompt", prompt).Error
}

// UpdateTitle 更新会话标题。
func (r *SessionRepository) UpdateTitle(id uint, title string) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).
		Update("title", title).Error
}

// UpdateYolo 更新会话级 YOLO 开关。
func (r *SessionRepository) UpdateYolo(id uint, yolo bool) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).
		Update("yolo", yolo).Error
}

// UpdateTags 更新会话标签（JSON 数组字符串）。
func (r *SessionRepository) UpdateTags(id uint, tags string) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).
		Update("tags", tags).Error
}

func (r *SessionRepository) MarkActiveAsError() error {
	return r.db.Model(&models.Session{}).
		Where("status = ?", models.SessionStatusActive).
		Update("status", models.SessionStatusError).Error
}

// MarkSessionsErrorByIDs 仅将指定 ID 中处于 active 状态的会话标记为 error。
// 用于服务启动恢复：只标记真正被中断任务影响的会话，而非所有活跃会话。
// 空列表时直接返回，避免 WHERE id IN () 语法错误。
func (r *SessionRepository) MarkSessionsErrorByIDs(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Model(&models.Session{}).
		Where("id IN ? AND status = ?", ids, models.SessionStatusActive).
		Update("status", models.SessionStatusError).Error
}

// FindByID 按数据库主键查询会话（含关联 Workspace，供前端 @ 文件引用等使用）。
func (r *SessionRepository) FindByID(id uint) (*models.Session, error) {
	var s models.Session
	if err := r.db.Preload("Workspace").First(&s, id).Error; err != nil {
		return nil, ErrSessionNotFound
	}
	return &s, nil
}

// UpdateAgentSessionID 更新会话的 ACP agent session ID（激活/恢复时调用，不改 session_id）。
func (r *SessionRepository) UpdateAgentSessionID(id uint, agentSessionID string) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).
		Update("agent_session_id", agentSessionID).Error
}

// FindByAgentSessionID 按 ACP agent session ID 查询会话（terminal 事件路由用）。
// 同一 agent_session_id 理论上仅对应一个活跃会话；取最近更新的一条兼容残留。
func (r *SessionRepository) FindByAgentSessionID(agentSessionID string) (*models.Session, error) {
	var s models.Session
	if err := r.db.Where("agent_session_id = ?", agentSessionID).
		Order("updated_at DESC").First(&s).Error; err != nil {
		return nil, ErrSessionNotFound
	}
	return &s, nil
}

// UpdateModelValue 更新会话记录的模型值（创建时或会话内切换模型时调用），
// 使配置项回显始终为"实际使用/发送时选择的模型"。
func (r *SessionRepository) UpdateModelValue(id uint, modelValue string) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).
		Update("model_value", modelValue).Error
}

// FindByWorkspaceID 返回指定 workspace 下的所有 session。
func (r *SessionRepository) FindByWorkspaceID(workspaceID uint) ([]models.Session, error) {
	var sessions []models.Session
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&sessions).Error
	return sessions, err
}

// Delete 按主键物理删除会话记录。
func (r *SessionRepository) Delete(id uint) error {
	return r.db.Delete(&models.Session{}, id).Error
}

// FindChildSessions 返回指定父会话下的所有子会话（按创建时间降序）。
// 用于 MCP 工具创建的子会话分组查询与前端展示。
func (r *SessionRepository) FindChildSessions(parentID uint) ([]models.Session, error) {
	var sessions []models.Session
	err := r.db.Where("parent_session_id = ?", parentID).Order("created_at DESC").Find(&sessions).Error
	return sessions, err
}
