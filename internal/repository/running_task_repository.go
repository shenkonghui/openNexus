package repository

import (
	"time"

	"gorm.io/gorm"

	"opennexus/internal/models"
)

// RunningTaskRepository 是进行中任务的持久化仓库，用于服务重启后的中断恢复。
type RunningTaskRepository struct {
	db *gorm.DB
}

// NewRunningTaskRepository 创建新的 RunningTaskRepository。
func NewRunningTaskRepository(db *gorm.DB) *RunningTaskRepository {
	return &RunningTaskRepository{db: db}
}

// Create 写入一条进行中任务记录。
func (r *RunningTaskRepository) Create(t *models.RunningTask) error {
	return r.db.Create(t).Error
}

// UpdateStatus 更新任务状态（可选设置 finished_at）。
func (r *RunningTaskRepository) UpdateStatus(id uint, status string, finishedAt *time.Time) error {
	return r.db.Model(&models.RunningTask{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": status, "finished_at": finishedAt}).Error
}

// UpdateLastSeq 更新任务最后处理的 sequence 值。
func (r *RunningTaskRepository) UpdateLastSeq(id uint, seq int) error {
	return r.db.Model(&models.RunningTask{}).Where("id = ?", id).
		Update("last_seq", seq).Error
}

// FindByID 按主键查询任务。
func (r *RunningTaskRepository) FindByID(id uint) (*models.RunningTask, error) {
	var t models.RunningTask
	if err := r.db.First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// FindLatestByDBSessionID 返回指定会话最近一次 prompt 的记录（按开始时间倒序取首条）。
// 用于消费方在消息流关闭后回查本轮真实终态；无记录时返回 (nil, nil)。
func (r *RunningTaskRepository) FindLatestByDBSessionID(dbSessionID uint) (*models.RunningTask, error) {
	var t models.RunningTask
	err := r.db.Where("db_session_id = ?", dbSessionID).
		Order("started_at DESC").Order("id DESC").First(&t).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// FindInterruptedByDBSessionID 返回指定会话下所有 interrupted 状态的任务。
func (r *RunningTaskRepository) FindInterruptedByDBSessionID(dbSessionID uint) ([]models.RunningTask, error) {
	var tasks []models.RunningTask
	err := r.db.Where("db_session_id = ? AND status = ?", dbSessionID, models.RunningTaskStatusInterrupted).
		Order("started_at ASC").Find(&tasks).Error
	return tasks, err
}

// MarkRunningAsInterrupted 将所有 running 状态的任务标记为 interrupted。
// 在服务启动时调用，用于处理上次未完成的 prompt。
func (r *RunningTaskRepository) MarkRunningAsInterrupted() error {
	return r.db.Model(&models.RunningTask{}).
		Where("status = ?", models.RunningTaskStatusRunning).
		Update("status", models.RunningTaskStatusInterrupted).Error
}

// DeleteByDBSessionID 删除指定会话下的全部任务记录（会话删除时清理）。
func (r *RunningTaskRepository) DeleteByDBSessionID(dbSessionID uint) error {
	return r.db.Where("db_session_id = ?", dbSessionID).Delete(&models.RunningTask{}).Error
}

// FindRunningDBSessionIDsByUser 返回指定用户下所有 status=running 的 db_session_id。
// 用于侧边栏展示「哪些会话正在运行」。
func (r *RunningTaskRepository) FindRunningDBSessionIDsByUser(userID uint) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.RunningTask{}).
		Where("user_id = ? AND status = ?", userID, models.RunningTaskStatusRunning).
		Distinct("db_session_id").
		Pluck("db_session_id", &ids).Error
	return ids, err
}

// FindInterruptedDBSessionIDs 返回所有 interrupted 任务对应的 db_session_id（去重）。
// 用于服务启动恢复：仅将这些会话标记为 error，避免误伤已正常完成的空闲会话。
func (r *RunningTaskRepository) FindInterruptedDBSessionIDs() ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.RunningTask{}).
		Where("status = ?", models.RunningTaskStatusInterrupted).
		Distinct("db_session_id").
		Pluck("db_session_id", &ids).Error
	return ids, err
}
