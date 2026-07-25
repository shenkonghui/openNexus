package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"opennexus/internal/models"
	"opennexus/internal/services"
)

// OrchestrationWorkspaceStore 解析编排关联的工作区。
type OrchestrationWorkspaceStore interface {
	FindWorkspaceByID(id uint) (*models.Workspace, error)
}

// OrchestrationHandler 处理任务编排相关请求。
type OrchestrationHandler struct {
	svc     *services.OrchestratorService
	wsStore OrchestrationWorkspaceStore
}

// NewOrchestrationHandler 创建 OrchestrationHandler。
func NewOrchestrationHandler(svc *services.OrchestratorService, wsStore OrchestrationWorkspaceStore) *OrchestrationHandler {
	return &OrchestrationHandler{svc: svc, wsStore: wsStore}
}

// resolveCwd 通过 workspace_id 解析 cwd，并校验归属当前用户。
func (h *OrchestrationHandler) resolveCwd(c *gin.Context) (string, uint, bool) {
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

// Get GET /api/v1/orchestration?workspace_id=123 — 读取 tasks.json
func (h *OrchestrationHandler) Get(c *gin.Context) {
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
	MaxParallel     int                        `json:"max_parallel"`
	Tasks           []models.OrchestrationTask `json:"tasks"`
	ParentSessionID *uint                      `json:"parent_session_id"`
}

// Save PUT /api/v1/orchestration?workspace_id=123 — 整体覆盖保存编排定义。
func (h *OrchestrationHandler) Save(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req saveDefRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	parentSessionID := req.ParentSessionID
	// 整体覆盖时若未携带 parent_session_id，回填现有值，避免丢失父会话登记。
	if parentSessionID == nil {
		if cur, err := h.svc.Load(cwd); err == nil {
			parentSessionID = cur.ParentSessionID
		}
	}
	def := &models.OrchestrationDef{MaxParallel: req.MaxParallel, Tasks: req.Tasks, ParentSessionID: parentSessionID}
	if err := h.svc.Save(cwd, def); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, def)
}

type setParentSessionRequest struct {
	SessionID uint `json:"session_id" binding:"required"`
}

// SetParentSession PUT /api/v1/orchestration/parent-session?workspace_id=123
// 登记编排管理会话为 tasks.json 的父会话，供后续任务子会话关联。
func (h *OrchestrationHandler) SetParentSession(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req setParentSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if err := h.svc.SetParentSession(cwd, req.SessionID); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"parent_session_id": req.SessionID})
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

// UpsertTask POST /api/v1/orchestration/tasks?workspace_id=123 — 新增/更新单个任务。
func (h *OrchestrationHandler) UpsertTask(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	var req upsertTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	task := models.OrchestrationTask{
		ID:         strings.TrimSpace(req.ID),
		Title:      strings.TrimSpace(req.Title),
		Detail:     req.Detail,
		AgentType:  req.AgentType,
		ModelValue: strings.TrimSpace(req.ModelValue),
		Priority:   models.NormalizeOrchTaskPriority(req.Priority),
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
	Success(c, http.StatusOK, task)
}

// DeleteTask DELETE /api/v1/orchestration/tasks/:task_id?workspace_id=123
func (h *OrchestrationHandler) DeleteTask(c *gin.Context) {
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

// SetMaxParallel PUT /api/v1/orchestration/max-parallel?workspace_id=123
func (h *OrchestrationHandler) SetMaxParallel(c *gin.Context) {
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

// Start POST /api/v1/orchestration/start?workspace_id=123 — 启动全部或单个任务。
func (h *OrchestrationHandler) Start(c *gin.Context) {
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

// Stop POST /api/v1/orchestration/stop?workspace_id=123 — 停止全部或单个任务。
func (h *OrchestrationHandler) Stop(c *gin.Context) {
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

// Status GET /api/v1/orchestration/status?workspace_id=123 — 轮询各任务状态。
func (h *OrchestrationHandler) Status(c *gin.Context) {
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

// GitStatus GET /api/v1/orchestration/git-status?workspace_id=123
// 报告编排 cwd 是否为 git 仓库（编排任务需基于 worktree 隔离）。
func (h *OrchestrationHandler) GitStatus(c *gin.Context) {
	cwd, _, ok := h.resolveCwd(c)
	if !ok {
		return
	}
	Success(c, http.StatusOK, gin.H{"cwd": cwd, "is_git_repo": h.svc.IsGitRepo(cwd)})
}

// GitInit POST /api/v1/orchestration/git-init?workspace_id=123
// 在编排 cwd 初始化 git 仓库（含初始提交）并创建 .worktrees 目录。
func (h *OrchestrationHandler) GitInit(c *gin.Context) {
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
