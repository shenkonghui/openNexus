package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"

	"opennexus/internal/models"
	"opennexus/internal/repository"
	"opennexus/internal/services"
)

// SchedulerManager 是 handler 操作定时任务所需的能力（*services.SchedulerService 实现该接口）。
type SchedulerManager interface {
	AddTask(cwd string, t *models.TaskManagerTask) error
	UpdateTask(cwd string, t *models.TaskManagerTask) error
	RemoveTask(cwd, taskID string) error
	RunTask(cwd, taskID string) error
}

// ScheduledTaskHandler 处理定时任务相关请求。
// 定时任务配置统一存储在 workspace cwd 下的 tasks.json 中。
type ScheduledTaskHandler struct {
	wsRepo *repository.WorkspaceRepository
	mgr    SchedulerManager
}

// NewScheduledTaskHandler 创建 ScheduledTaskHandler。
func NewScheduledTaskHandler(wsRepo *repository.WorkspaceRepository, mgr SchedulerManager) *ScheduledTaskHandler {
	return &ScheduledTaskHandler{wsRepo: wsRepo, mgr: mgr}
}

type createTaskRequest struct {
	Name           string `json:"name" binding:"required"`
	AgentType      string `json:"agent_type" binding:"required"`
	WorkspaceID    uint   `json:"workspace_id"`
	Cwd            string `json:"cwd"`
	Prompt         string `json:"prompt" binding:"required"`
	CronExpr       string `json:"cron_expr" binding:"required"`
	Enabled        *bool  `json:"enabled"`
	ModelValue     string `json:"model_value"`
	TimeoutMinutes *int   `json:"timeout_minutes"`
}

// Create POST /api/v1/scheduled-tasks
func (h *ScheduledTaskHandler) Create(c *gin.Context) {
	var req createTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if err := validateCron(req.CronExpr); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_CRON", err.Error())
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	ws, cwd, err := h.resolveWorkspace(uid, &req.WorkspaceID, &req.Cwd)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	timeout := 5
	if req.TimeoutMinutes != nil && *req.TimeoutMinutes > 0 {
		timeout = *req.TimeoutMinutes
	}

	id := slugify(req.Name)
	store := services.NewTaskStore(cwd)
	for i := 1; ; i++ {
		if _, err := store.FindTask(id); err != nil {
			break
		}
		id = slugify(req.Name) + "-" + strconv.Itoa(i)
	}
	task := &models.TaskManagerTask{
		ID:         id,
		Title:      strings.TrimSpace(req.Name),
		Detail:     req.Prompt,
		AgentType:  req.AgentType,
		ModelValue: strings.TrimSpace(req.ModelValue),
		Priority:   models.TaskPriorityP1,
		Schedule: &models.TaskSchedule{
			CronExpr:       req.CronExpr,
			Enabled:        enabled,
			TimeoutMinutes: timeout,
		},
	}
	if err := h.mgr.AddTask(cwd, task); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusCreated, scheduledTaskResponse(ws, cwd, task))
}

// List GET /api/v1/scheduled-tasks?workspace_id=123
func (h *ScheduledTaskHandler) List(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	wsIDStr := c.Query("workspace_id")
	var out []gin.H
	if wsIDStr != "" {
		wsID, parseErr := strconv.ParseUint(wsIDStr, 10, 64)
		if parseErr != nil {
			Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "workspace_id 参数无效")
			return
		}
		ws, err := h.wsRepo.FindByID(uint(wsID))
		if err != nil || ws.UserID != uid {
			Fail(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "工作区不存在")
			return
		}
		tasks, err := services.NewTaskStore(ws.Cwd).ListScheduledTasks()
		if err != nil {
			Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		for i := range tasks {
			out = append(out, scheduledTaskResponse(ws, ws.Cwd, &tasks[i]))
		}
	} else {
		wss, err := h.wsRepo.FindByUserID(uid)
		if err != nil {
			Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		for _, ws := range wss {
			tasks, err := services.NewTaskStore(ws.Cwd).ListScheduledTasks()
			if err != nil {
				continue
			}
			for i := range tasks {
				out = append(out, scheduledTaskResponse(&ws, ws.Cwd, &tasks[i]))
			}
		}
	}
	Success(c, http.StatusOK, gin.H{"tasks": out})
}

// Get GET /api/v1/scheduled-tasks/:id
func (h *ScheduledTaskHandler) Get(c *gin.Context) {
	_, task, ws, cwd, ok := h.loadOwnedTask(c)
	if !ok {
		return
	}
	Success(c, http.StatusOK, scheduledTaskResponse(ws, cwd, task))
}

type updateTaskRequest struct {
	Name           *string `json:"name"`
	AgentType      *string `json:"agent_type"`
	WorkspaceID    *uint   `json:"workspace_id"`
	Cwd            *string `json:"cwd"`
	Prompt         *string `json:"prompt"`
	CronExpr       *string `json:"cron_expr"`
	Enabled        *bool   `json:"enabled"`
	ModelValue     *string `json:"model_value"`
	TimeoutMinutes *int    `json:"timeout_minutes"`
}

// Update PUT /api/v1/scheduled-tasks/:id
func (h *ScheduledTaskHandler) Update(c *gin.Context) {
	uid, task, ws, cwd, ok := h.loadOwnedTask(c)
	if !ok {
		return
	}
	var req updateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if req.CronExpr != nil {
		if err := validateCron(*req.CronExpr); err != nil {
			Fail(c, http.StatusBadRequest, "INVALID_CRON", err.Error())
			return
		}
	}
	// 如果请求切换了工作区，先迁移任务到新 cwd
	if req.WorkspaceID != nil || req.Cwd != nil {
		newWS, newCwd, err := h.resolveWorkspace(uid, req.WorkspaceID, req.Cwd)
		if err != nil {
			Fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		if newCwd != cwd {
			// 先在新位置新增，再从旧位置删除
			if err := services.NewTaskStore(newCwd).UpsertTask(*task); err != nil {
				Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
				return
			}
			_ = services.NewTaskStore(cwd).DeleteTask(task.ID)
			cwd = newCwd
			ws = newWS
		}
	}
	if req.Name != nil {
		task.Title = strings.TrimSpace(*req.Name)
	}
	if req.AgentType != nil {
		task.AgentType = *req.AgentType
	}
	if req.Prompt != nil {
		task.Detail = *req.Prompt
	}
	if req.CronExpr != nil {
		task.Schedule.CronExpr = *req.CronExpr
	}
	if req.Enabled != nil {
		task.Schedule.Enabled = *req.Enabled
	}
	if req.ModelValue != nil {
		task.ModelValue = strings.TrimSpace(*req.ModelValue)
	}
	if req.TimeoutMinutes != nil && *req.TimeoutMinutes > 0 {
		task.Schedule.TimeoutMinutes = *req.TimeoutMinutes
	}
	if err := h.mgr.UpdateTask(cwd, task); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, scheduledTaskResponse(ws, cwd, task))
}

// Delete DELETE /api/v1/scheduled-tasks/:id
func (h *ScheduledTaskHandler) Delete(c *gin.Context) {
	_, task, _, cwd, ok := h.loadOwnedTask(c)
	if !ok {
		return
	}
	if err := h.mgr.RemoveTask(cwd, task.ID); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

// Run POST /api/v1/scheduled-tasks/:id/run — 手动触发一次执行。
func (h *ScheduledTaskHandler) Run(c *gin.Context) {
	_, task, _, cwd, ok := h.loadOwnedTask(c)
	if !ok {
		return
	}
	if err := h.mgr.RunTask(cwd, task.ID); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

// Executions GET /api/v1/scheduled-tasks/:id/executions — 任务关联会话的执行块历史。
func (h *ScheduledTaskHandler) Executions(c *gin.Context) {
	_, task, _, _, ok := h.loadOwnedTask(c)
	if !ok {
		return
	}
	if task.SessionID == "" {
		Success(c, http.StatusOK, gin.H{"executions": []models.TaskExecutionRecord{}})
		return
	}
	Success(c, http.StatusOK, gin.H{"executions": task.Executions})
}

// loadOwnedTask 加载 :id 对应任务并校验归属；失败时已写入错误响应。
// 返回 (userID, task, workspace, cwd, ok)。
func (h *ScheduledTaskHandler) loadOwnedTask(c *gin.Context) (uint, *models.TaskManagerTask, *models.Workspace, string, bool) {
	taskID := c.Param("id")
	if taskID == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "无效的任务 ID")
		return 0, nil, nil, "", false
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return 0, nil, nil, "", false
	}
	wss, err := h.wsRepo.FindByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return 0, nil, nil, "", false
	}
	for i := range wss {
		ws := &wss[i]
		t, err := services.NewTaskStore(ws.Cwd).FindTask(taskID)
		if err == nil {
			return uid, t, ws, ws.Cwd, true
		}
	}
	Fail(c, http.StatusNotFound, "TASK_NOT_FOUND", "定时任务不存在")
	return 0, nil, nil, "", false
}

// resolveWorkspace 校验工作区归属并返回 workspace 与 cwd。
func (h *ScheduledTaskHandler) resolveWorkspace(uid uint, workspaceID *uint, fallbackCwd *string) (*models.Workspace, string, error) {
	if workspaceID != nil && *workspaceID > 0 {
		ws, err := h.wsRepo.FindByID(*workspaceID)
		if err != nil {
			return nil, "", errors.New("工作区不存在")
		}
		if ws.UserID != uid {
			return nil, "", errors.New("工作区不存在")
		}
		return ws, ws.Cwd, nil
	}
	if fallbackCwd != nil && *fallbackCwd != "" {
		ws, err := h.wsRepo.FindByUserIDAndCwd(uid, *fallbackCwd)
		if err != nil {
			return nil, "", errors.New("工作区不存在")
		}
		return ws, ws.Cwd, nil
	}
	return nil, "", errors.New("缺少 workspace_id 或 cwd")
}

// scheduledTaskResponse 把 TaskManagerTask 映射为前端兼容的 ScheduledTask JSON。
func scheduledTaskResponse(ws *models.Workspace, cwd string, t *models.TaskManagerTask) gin.H {
	resp := gin.H{
		"id":            t.ID,
		"name":          t.Title,
		"agent_type":    t.AgentType,
		"workspace_id":  ws.ID,
		"cwd":           cwd,
		"prompt":        t.Detail,
		"model_value":   t.ModelValue,
		"session_id":    t.SessionID,
		"db_session_id": t.DBSessionID,
	}
	if t.Schedule != nil {
		resp["cron_expr"] = t.Schedule.CronExpr
		resp["enabled"] = t.Schedule.Enabled
		resp["timeout_minutes"] = t.Schedule.TimeoutMinutes
		resp["last_run_at"] = t.Schedule.LastRunAt
		resp["last_status"] = t.Schedule.LastStatus
		resp["last_error"] = t.Schedule.LastError
	}
	return resp
}

// slugify 把任务名转成适合作为文件 id 的字符串。
func slugify(name string) string {
	s := strings.TrimSpace(name)
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '/' || r == '.' {
			b.WriteRune('-')
		}
	}
	id := b.String()
	if id == "" {
		id = "task"
	}
	return strings.Trim(id, "-")
}

// validateCron 校验 cron 表达式。
func validateCron(expr string) error {
	_, err := cron.ParseStandard(expr)
	return err
}
