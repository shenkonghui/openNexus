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

// FindByCwd 按 cwd 查找工作区（不限制用户，用于定时任务从文件路径反查）。
func (r *WorkspaceRepository) FindByCwd(cwd string) (*models.Workspace, error) {
	var ws models.Workspace
	err := r.db.Where("cwd = ?", cwd).First(&ws).Error
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

// FindDefaultByUserID 查找用户的默认工作区（按名称"默认工作区"匹配，与 mode 解耦）。
// 默认工作区现为 persistent + 固定 cwd，故不再以 mode 作为筛选条件。
func (r *WorkspaceRepository) FindDefaultByUserID(userID uint) (*models.Workspace, error) {
	var ws models.Workspace
	err := r.db.Where("user_id = ? AND name = ?", userID, "默认工作区").Order("created_at ASC").First(&ws).Error
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
