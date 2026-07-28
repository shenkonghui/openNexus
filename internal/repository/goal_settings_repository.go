package repository

import (
	"errors"

	"gorm.io/gorm"

	"opennexus/internal/models"
)

// GoalSettingsRepository 管理通用 goal 循环设置（评估 agent/模型 + 限制条件）。
type GoalSettingsRepository struct {
	db *gorm.DB
}

func NewGoalSettingsRepository(db *gorm.DB) *GoalSettingsRepository {
	return &GoalSettingsRepository{db: db}
}

// FindByUserID 返回用户的 goal 设置，不存在时返回零值记录。
func (r *GoalSettingsRepository) FindByUserID(userID uint) (*models.GoalSettings, error) {
	var s models.GoalSettings
	err := r.db.Where("user_id = ?", userID).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &models.GoalSettings{UserID: userID}, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Upsert 创建或更新用户 goal 设置。
func (r *GoalSettingsRepository) Upsert(s *models.GoalSettings) error {
	var existing models.GoalSettings
	err := r.db.Where("user_id = ?", s.UserID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(s).Error
	}
	if err != nil {
		return err
	}
	s.ID = existing.ID
	s.CreatedAt = existing.CreatedAt
	return r.db.Save(s).Error
}
