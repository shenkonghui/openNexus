package handlers

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/models"
	"opennexus/internal/repository"
	"opennexus/internal/services"
)

// TaskManagerWorkspaceStore 解析编排关联的工作区。
type TaskManagerWorkspaceStore interface {
	FindWorkspaceByID(id uint) (*models.Workspace, error)
}

// TaskManagerHandler 处理任务管理相关请求。
type TaskManagerHandler struct {
	svc     *services.TaskManagerService
	wsStore TaskManagerWorkspaceStore
	// settingsRepo 提供归档保留天数配置（可为 nil，此时用默认天数）。
	settingsRepo *repository.TaskSettingsRepository
}

// NewTaskManagerHandler 创建 TaskManagerHandler。
func NewTaskManagerHandler(svc *services.TaskManagerService, wsStore TaskManagerWorkspaceStore) *TaskManagerHandler {
	return &TaskManagerHandler{svc: svc, wsStore: wsStore}
}

// SetSettingsRepo 注入任务设置仓库，用于读取用户配置的归档保留天数。
func (h *TaskManagerHandler) SetSettingsRepo(repo *repository.TaskSettingsRepository) {
	h.settingsRepo = repo
}

// archiveRetentionDays 返回当前用户配置的归档保留天数（未配置/异常时取默认 3 天）。
func (h *TaskManagerHandler) archiveRetentionDays(c *gin.Context) int {
	if h.settingsRepo != nil {
		if uid, ok := currentUserID(c); ok {
			if s, err := h.settingsRepo.FindByUserID(uid); err == nil && s.ArchiveRetentionDays > 0 {
				return s.ArchiveRetentionDays
			}
		}
	}
	return models.DefaultArchiveRetentionDays
}

// resolveCwd 通过 workspace_id 解析 cwd，并校验归属当前用户。
func (h *TaskManagerHandler) resolveCwd(c *gin.Context) (string, uint, bool) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return "", 0, false
	}
	wsIDStr := c.Query("workspace_id")
	if wsIDStr == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 workspace_id")
		return "", 0, false
	}
	wsID, err := strconv.ParseUint(wsIDStr, 10, 64)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "workspace_id 参数无效")
		return "", 0, false
	}
	if h.wsStore == nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "工作区服务未配置")
		return "", 0, false
	}
	ws, werr := h.wsStore.FindWorkspaceByID(uint(wsID))
	if werr != nil || ws == nil || ws.UserID != uid {
		Fail(c, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "工作区不存在")
		return "", 0, false
	}
	if strings.TrimSpace(ws.Cwd) == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "工作区未配置 cwd")
		return "", 0, false
	}
	return ws.Cwd, ws.ID, true
}

// Get GET /api/v1/taskmanager?workspace_id=123 — 读取 tasks.json
func (h *TaskManagerHandler) Get(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	def, err := h.svc.Load(cwd)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, def)
}

type saveDefRequest struct {
	MaxParallel int                      `json:"max_parallel"`
	Tasks       []models.TaskManagerTask `json:"tasks"`
}

// Save PUT /api/v1/taskmanager?workspace_id=123 — 整体覆盖保存任务定义。
func (h *TaskManagerHandler) Save(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req saveDefRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	def := &models.TaskManagerDef{MaxParallel: req.MaxParallel, Tasks: req.Tasks}
	if err := h.svc.Save(cwd, def); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, def)
}

type upsertTaskRequest struct {
	ID         string   `json:"id" binding:"required"`
	Title      string   `json:"title" binding:"required"`
	Detail     string   `json:"detail" binding:"required"`
	AgentType  string   `json:"agent_type" binding:"required"`
	ModelValue string   `json:"model_value"`
	Priority   string   `json:"priority"`
	DependsOn  []string `json:"depends_on"`
	// GoalCondition 为空时沿用已有任务的条件（见 TaskStore.UpsertTask 合并逻辑）。
	GoalCondition string `json:"goal_condition"`
}

// UpsertTask POST /api/v1/taskmanager/tasks?workspace_id=123 — 新增/更新单个任务。
func (h *TaskManagerHandler) UpsertTask(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req upsertTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	task := models.TaskManagerTask{
		ID:            strings.TrimSpace(req.ID),
		Title:         strings.TrimSpace(req.Title),
		Detail:        req.Detail,
		AgentType:     req.AgentType,
		ModelValue:    strings.TrimSpace(req.ModelValue),
		Priority:      models.NormalizeTaskPriority(req.Priority),
		DependsOn:     req.DependsOn,
		GoalCondition: strings.TrimSpace(req.GoalCondition),
	}
	if task.ID == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "任务 id 不能为空")
		return
	}
	if err := h.svc.UpsertTask(cwd, task); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	// 重新读取持久化后的任务（包含默认 status 等运行时字段）返回给前端。
	saved, err := h.svc.Load(cwd)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	for i := range saved.Tasks {
		if saved.Tasks[i].ID == task.ID {
			Success(c, http.StatusOK, saved.Tasks[i])
			return
		}
	}
	Success(c, http.StatusOK, task)
}

// DeleteTask DELETE /api/v1/taskmanager/tasks/:task_id?workspace_id=123
func (h *TaskManagerHandler) DeleteTask(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	taskID := c.Param("task_id")
	if taskID == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 task_id")
		return
	}
	if err := h.svc.DeleteTask(cwd, taskID); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"deleted": taskID})
}

type setMaxParallelRequest struct {
	MaxParallel int `json:"max_parallel" binding:"required"`
}

// SetMaxParallel PUT /api/v1/taskmanager/max-parallel?workspace_id=123
func (h *TaskManagerHandler) SetMaxParallel(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req setMaxParallelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if err := h.svc.SetMaxParallel(cwd, req.MaxParallel); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"max_parallel": req.MaxParallel})
}

type startRequest struct {
	TaskID string `json:"task_id"`
}

// Start POST /api/v1/taskmanager/start?workspace_id=123 — 启动全部或单个任务。
func (h *TaskManagerHandler) Start(c *gin.Context) {
	cwd, wsID, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	uid, _ := currentUserID(c)
	var req startRequest
	_ = c.ShouldBindJSON(&req) // 可空 body
	if err := h.svc.Start(c.Request.Context(), cwd, wsID, uid, strings.TrimSpace(req.TaskID)); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"started": true})
}

type stopRequest struct {
	TaskID string `json:"task_id"`
}

// Stop POST /api/v1/taskmanager/stop?workspace_id=123 — 停止全部或单个任务。
func (h *TaskManagerHandler) Stop(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req stopRequest
	_ = c.ShouldBindJSON(&req)
	if err := h.svc.Stop(cwd, strings.TrimSpace(req.TaskID)); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"stopped": true})
}

// Status GET /api/v1/taskmanager/status?workspace_id=123 — 轮询各任务状态。
func (h *TaskManagerHandler) Status(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	def, err := h.svc.Load(cwd)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"max_parallel": def.MaxParallel, "tasks": def.Tasks})
}

// Events GET /api/v1/taskmanager/events?workspace_id=123 — SSE 推送 tasks.json 变更事件。
// 任意来源（REST/MCP 工具/编排器/调度器）的写入都会触发一条 "changed" 事件，
// 前端收到后重新拉取任务列表，实现两侧界面自动同步；定期发送心跳保活。
func (h *TaskManagerHandler) Events(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	ch, cancel := services.SubscribeTaskChanges(cwd)
	defer cancel()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	ctx := c.Request.Context()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-ch:
			_, _ = io.WriteString(w, "data: changed\n\n")
			return true
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			return true
		}
	})
}

// GitStatus GET /api/v1/taskmanager/git-status?workspace_id=123
// 报告编排 cwd 是否为 git 仓库（编排任务需基于 worktree 隔离）。
func (h *TaskManagerHandler) GitStatus(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	Success(c, http.StatusOK, gin.H{"cwd": cwd, "is_git_repo": h.svc.IsGitRepo(cwd)})
}

// GitInit POST /api/v1/taskmanager/git-init?workspace_id=123
// 在编排 cwd 初始化 git 仓库（含初始提交）并创建 .worktrees 目录。
func (h *TaskManagerHandler) GitInit(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	if err := h.svc.InitGitRepo(cwd); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"cwd": cwd, "is_git_repo": true})
}

type archiveRequest struct {
	TaskID string `json:"task_id"`
}

// Archive POST /api/v1/taskmanager/archive?workspace_id=123 — 归档任务到回收站。
// task_id 为空或 "*" 时归档全部任务；顺带清理已过保留期的归档条目。
func (h *TaskManagerHandler) Archive(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req archiveRequest
	_ = c.ShouldBindJSON(&req) // 可空 body（= 归档全部）
	taskID := strings.TrimSpace(req.TaskID)
	archivedCount := 0
	if taskID == "" || taskID == "*" {
		n, err := h.svc.ArchiveAllTasks(cwd)
		if err != nil {
			Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		archivedCount = n
	} else {
		if err := h.svc.ArchiveTask(cwd, taskID); err != nil {
			Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		archivedCount = 1
	}
	h.svc.PurgeExpiredArchived(cwd, h.archiveRetentionDays(c))
	Success(c, http.StatusOK, gin.H{"archived": archivedCount})
}

// ListArchived GET /api/v1/taskmanager/archived?workspace_id=123 — 回收站列表。
// 先清理已过保留期的条目（含 worktree/会话），再返回剩余内容。
func (h *TaskManagerHandler) ListArchived(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	retention := h.archiveRetentionDays(c)
	h.svc.PurgeExpiredArchived(cwd, retention)
	list, err := h.svc.ListArchivedTasks(cwd)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if list == nil {
		list = []models.ArchivedTask{}
	}
	Success(c, http.StatusOK, gin.H{"tasks": list, "retention_days": retention})
}

// RestoreArchived POST /api/v1/taskmanager/archived/:task_id/restore?workspace_id=123
func (h *TaskManagerHandler) RestoreArchived(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	taskID := c.Param("task_id")
	if taskID == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 task_id")
		return
	}
	if err := h.svc.RestoreArchivedTask(cwd, taskID); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"restored": taskID})
}

// DeleteArchived DELETE /api/v1/taskmanager/archived/:task_id?workspace_id=123 — 彻底删除。
func (h *TaskManagerHandler) DeleteArchived(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	taskID := c.Param("task_id")
	if taskID == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 task_id")
		return
	}
	if err := h.svc.DeleteArchivedTask(cwd, taskID); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"deleted": taskID})
}
