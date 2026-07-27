package handlers

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/models"
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
}

// NewTaskManagerHandler 创建 TaskManagerHandler。
func NewTaskManagerHandler(svc *services.TaskManagerService, wsStore TaskManagerWorkspaceStore) *TaskManagerHandler {
	return &TaskManagerHandler{svc: svc, wsStore: wsStore}
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
	MaxParallel int                        `json:"max_parallel"`
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
		ID:         strings.TrimSpace(req.ID),
		Title:      strings.TrimSpace(req.Title),
		Detail:     req.Detail,
		AgentType:  req.AgentType,
		ModelValue: strings.TrimSpace(req.ModelValue),
		Priority:   models.NormalizeTaskPriority(req.Priority),
		DependsOn:  req.DependsOn,
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
