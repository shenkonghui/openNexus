package repository

import (
	"gorm.io/gorm"

	"opennexus/internal/models"
)

type WorkspaceRepository struct {
	db *gorm.DB
}

func NewWorkspaceRepository(db *gorm.DB) *WorkspaceRepository {
	return &WorkspaceRepository{db: db}
}

func (r *WorkspaceRepository) Create(ws *models.Workspace) error {
	return r.db.Create(ws).Error
}

func (r *WorkspaceRepository) FindByID(id uint) (*models.Workspace, error) {
	var ws models.Workspace
	if err := r.db.First(&ws, id).Error; err != nil {
		return nil, err
	}
	return &ws, nil
}

func (r *WorkspaceRepository) FindByUserID(userID uint) ([]models.Workspace, error) {
	var workspaces []models.Workspace
	err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&workspaces).Error
	return workspaces, err
}

// ListCwds 返回全部工作区的 cwd（去重前的原始列表），供编排 RecoverAll 等启动恢复使用。
func (r *WorkspaceRepository) ListCwds() ([]string, error) {
	var cwds []string
	err := r.db.Model(&models.Workspace{}).Where("cwd != ''").Pluck("cwd", &cwds).Error
	return cwds, err
}

func (r *WorkspaceRepository) FindByUserIDAndCwd(userID uint, cwd string) (*models.Workspace, error) {
	var ws models.Workspace
	err := r.db.Where("user_id = ? AND cwd = ? AND mode = ?", userID, cwd, models.WorkspaceModePersistent).First(&ws).Error
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

func (r *WorkspaceRepository) Update(id uint, updates map[string]interface{}) error {
	return r.db.Model(&models.Workspace{}).Where("id = ?", id).Updates(updates).Error
}

func (r *WorkspaceRepository) Delete(id uint) error {
	return r.db.Delete(&models.Workspace{}, id).Error
}

// FindDefaultByUserID 查找用户的默认 temporary workspace。
func (r *WorkspaceRepository) FindDefaultByUserID(userID uint) (*models.Workspace, error) {
	var ws models.Workspace
	err := r.db.Where("user_id = ? AND mode = ?", userID, models.WorkspaceModeTemporary).First(&ws).Error
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

// SessionCount 统计 workspace 下的 session 数。
func (r *WorkspaceRepository) SessionCount(workspaceID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Session{}).Where("workspace_id = ?", workspaceID).Count(&count).Error
	return count, err
}

// CountByUserID 统计用户的 workspace 总数。
func (r *WorkspaceRepository) CountByUserID(userID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Workspace{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}
