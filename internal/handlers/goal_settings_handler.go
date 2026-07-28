package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	acplocal "opennexus/internal/acp"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// GoalSettingsHandler 处理通用 goal 循环设置（评估 agent/模型 + 限制条件）。
type GoalSettingsHandler struct {
	settingsRepo *repository.GoalSettingsRepository
	// goalRoles 返回当前扫描到的评估角色 + 主用户目录（通常是 acp.Service.GoalRolesSnapshot），
	// 在 main 中通过 SetGoalRolesProvider 注入；未注入时 Roles 接口返回空列表。
	goalRoles func(cwd string) ([]acplocal.GoalRoleDef, string)
}

func NewGoalSettingsHandler(settingsRepo *repository.GoalSettingsRepository) *GoalSettingsHandler {
	return &GoalSettingsHandler{settingsRepo: settingsRepo}
}

// SetGoalRolesProvider 注入评估角色扫描器（独立 setter，不动构造函数签名）。
func (h *GoalSettingsHandler) SetGoalRolesProvider(fn func(cwd string) ([]acplocal.GoalRoleDef, string)) {
	h.goalRoles = fn
}

type goalSettingsItem struct {
	AgentType          string `json:"agent_type"`
	ModelValue         string `json:"model_value"`
	MaxTurns           int    `json:"max_turns"`
	MaxDurationMinutes int    `json:"max_duration_minutes"`
}

func goalSettingsToItem(s *models.GoalSettings) goalSettingsItem {
	return goalSettingsItem{
		AgentType:          s.AgentType,
		ModelValue:         s.ModelValue,
		MaxTurns:           s.MaxTurns,
		MaxDurationMinutes: s.MaxDurationMinutes,
	}
}

// GetSettings GET /api/v1/goal/settings
func (h *GoalSettingsHandler) GetSettings(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	s, err := h.settingsRepo.FindByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询 goal 设置失败")
		return
	}
	Success(c, http.StatusOK, goalSettingsToItem(s))
}

// UpdateSettings PUT /api/v1/goal/settings
func (h *GoalSettingsHandler) UpdateSettings(c *gin.Context) {
	var req goalSettingsItem
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	if req.MaxTurns < 0 || req.MaxDurationMinutes < 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "限制条件不能为负数")
		return
	}
	s := &models.GoalSettings{
		UserID:             uid,
		AgentType:          strings.TrimSpace(req.AgentType),
		ModelValue:         strings.TrimSpace(req.ModelValue),
		MaxTurns:           req.MaxTurns,
		MaxDurationMinutes: req.MaxDurationMinutes,
	}
	if err := h.settingsRepo.Upsert(s); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "保存 goal 设置失败")
		return
	}
	saved, err := h.settingsRepo.FindByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询 goal 设置失败")
		return
	}
	Success(c, http.StatusOK, goalSettingsToItem(saved))
}

// Roles GET /api/v1/goal/roles?path=...
// 扫描 goal 评估角色定义文件（path 可选：指定工作区时连同 project 角色一起返回），
// 并附带主用户目录供前端新建角色文件。文件的增删改复用 /filesystem/file|create|entry。
func (h *GoalSettingsHandler) Roles(c *gin.Context) {
	if h.goalRoles == nil {
		Success(c, http.StatusOK, gin.H{"roles": []acplocal.GoalRoleDef{}, "user_dir": ""})
		return
	}
	roles, userDir := h.goalRoles(strings.TrimSpace(c.Query("path")))
	if roles == nil {
		roles = []acplocal.GoalRoleDef{}
	}
	Success(c, http.StatusOK, gin.H{"roles": roles, "user_dir": userDir})
}
