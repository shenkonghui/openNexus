package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/gin-gonic/gin"

	acplocal "opennexus/internal/acp"
	"opennexus/internal/agent"
	"opennexus/internal/middleware"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// SessionStore 暴露会话相关业务能力（*agent.Router 实现该接口）。
type SessionStore interface {
	CreateSession(ctx context.Context, agentType string, workspaceID uint, userID uint, modelValue string) (*models.Session, error)
	// CreateSessionWithSource 创建会话并指定来源（manual/orchestration 等）。
	CreateSessionWithSource(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue string) (*models.Session, error)
	// CreateSessionWithCwd 创建会话并将 cwd 固定为用户指定目录（如已存在的 git worktree）。
	CreateSessionWithCwd(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue, cwd string) (*models.Session, error)
	ListSessions(userID uint) ([]models.Session, error)
	ListSessionsBySource(userID uint, source string) ([]models.Session, error)
	// FindSessionsByWorkspaceID 返回指定 workspace 下的会话（按 created_at DESC）。
	// 任务助手用它实现“一个工作区只复用最近一条管理会话”。
	FindSessionsByWorkspaceID(workspaceID uint) ([]models.Session, error)
	GetSessionByDBID(id uint) (*models.Session, error)
	DeleteSession(ctx context.Context, sessionID string) error
	CancelSession(ctx context.Context, sessionID string) error
	ResumeSession(ctx context.Context, sessionID string) (*models.Session, error)
	ClearContext(ctx context.Context, sessionID string) (*models.Session, error)
	ListMessages(sessionID string) ([]models.Message, error)
	// ListMessagesPaged 分页查询消息；limit<=0 使用默认页大小。
	ListMessagesPaged(sessionID string, limit, offset int) ([]models.Message, error)
	// ListMessagesRecent 返回最近的若干条消息 + 是否还有更早的消息（供前端「加载更多」）。
	// beforeSeq>0 时仅返回 sequence<beforeSeq 的消息；<=0 时不限制。
	ListMessagesRecent(sessionID string, beforeSeq int, limit int) ([]models.Message, bool, error)
	// ListMessagesByKind 仅查询指定 kind 的消息（如 tool_call_update），避免加载无关历史。
	ListMessagesByKind(sessionID string, kind string) ([]models.Message, error)
	// FindMessageByID 按消息主键查询单条消息（用于撤销等按消息定位的场景）。
	FindMessageByID(messageID uint) (*models.Message, error)
	// DeleteMessagesFromSequence 删除指定会话中 sequence 大于等于 fromSeq 的消息（会话回滚，含目标）。
	DeleteMessagesFromSequence(sessionID string, fromSeq int) (int64, error)
	ListCommands(sessionID string) ([]acp.AvailableCommand, error)
	ListConfiguredCommandsForSession(sessionID string) ([]acplocal.SlashCommand, error)
	ListConfigOptions(sessionID string) ([]acp.SessionConfigOption, error)
	ListModes(sessionID string) ([]acp.SessionMode, error)
	ListSkills(sessionID string) ([]acplocal.Skill, error)
	SetConfigOption(ctx context.Context, sessionID, configID, value string) error
	SetSessionMode(ctx context.Context, sessionID, modeID string) error
	RespondPermission(sessionID, requestID, optionID string, cancelled bool) error
	UpdateTitle(dbSessionID uint, title string) error
	// SetSessionYolo 设置会话级 YOLO（名单仍全局生效）。
	SetSessionYolo(dbSessionID uint, yolo bool) (*models.Session, error)
	Prompt(ctx context.Context, sessionID, prompt string) (<-chan models.Message, error)
	GetWorkspaceCwd(workspaceID uint) (string, error)
	ListExecutions(sessionID string) ([]repository.ExecutionAggregate, error)
	// SubscribeSession 订阅会话当前进行中的 prompt 流（断点续传），返回遗漏消息与实时 channel。
	SubscribeSession(sessionID string, lastSeq int) ([]models.Message, <-chan models.Message, error)
	// HasActivePrompt 判断会话是否有进行中的 prompt。
	HasActivePrompt(sessionID string) bool
	// ConnectionStatusForSession 返回会话所属 ACP 连接的状态与重连倒计时（毫秒）。
	ConnectionStatusForSession(sessionID string) (state string, retryInMs int64, attempt int)
	// ListInterruptedTasks 返回指定会话下因服务重启而中断的任务。
	ListInterruptedTasks(dbSessionID uint) ([]models.RunningTask, error)
	// ResumeInterruptedTask 恢复中断的任务：ResumeSession + 重新发送原 prompt。
	ResumeInterruptedTask(ctx context.Context, taskID uint) (<-chan models.Message, error)
	// ListRunningDBSessionIDs 返回指定用户下所有正在运行的 db_session_id。
	ListRunningDBSessionIDs(userID uint) ([]uint, error)
	// RunPromptOnce 在临时 ACP 会话中发送 prompt 并收集文本响应（不落库），
	// 用于 AI 生成 worktree 名等一次性调用。
	RunPromptOnce(ctx context.Context, agentType, modelValue, prompt string) (string, error)
}

// SessionTaskRegistrar 把新建会话登记到工作区 cwd 下的 tasks.json，
// 使任务编排视图统一展示所有任务/对话。由 *services.TaskManagerService 实现。
type SessionTaskRegistrar interface {
	RegisterSessionTask(cwd string, sess *models.Session, prompt string) error
	// UnregisterSessionTask 删除 tasks.json 中 db_session_id 匹配的任务条目，
	// 与登记对称：删除会话时调用，避免任务列表残留已删除会话的条目。
	UnregisterSessionTask(cwd string, dbSessionID uint) error
}

// SessionHandler 处理会话相关请求。
type SessionHandler struct {
	store     SessionStore
	registrar SessionTaskRegistrar
}

// NewSessionHandler 创建 SessionHandler。
func NewSessionHandler(store SessionStore) *SessionHandler {
	return &SessionHandler{store: store}
}

// SetTaskRegistrar 注入任务登记器，使新建会话首次发送 prompt 时同步写入 tasks.json。
// 不调用此方法时，SessionHandler 行为与原先一致（不写 tasks.json）。
func (h *SessionHandler) SetTaskRegistrar(r SessionTaskRegistrar) {
	h.registrar = r
}

// currentUserID 从 context 读取中间件注入的 userID。
func currentUserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get(middleware.UserIDKey())
	if !exists {
		return 0, false
	}
	uid, ok := v.(uint)
	return uid, ok
}

// resolveSessionWorkingDir 解析会话的实际工作目录，供终端 / 文件浏览等复用。
// 普通会话：优先取工作区当前 cwd（工作区目录可被用户在设置中修改）。
// 编排任务会话：git worktree 绝对路径经 cwdOverride 写入 session.Cwd，与工作区 cwd 不同，
// 此时以 worktree 作为工作目录，使终端 / 文件浏览与 agent 实际运行目录保持一致。
func resolveSessionWorkingDir(store SessionStore, sess *models.Session) string {
	cwd := sess.Cwd
	if sess.WorkspaceID != nil {
		if wsCwd, err := store.GetWorkspaceCwd(*sess.WorkspaceID); err == nil {
			// session.Cwd 为空（历史数据）或与工作区一致（普通会话）时，回退到工作区当前 cwd；
			// 仅当 session.Cwd 为 worktree 覆盖（非空且不同）时保留 session.Cwd。
			if sess.Cwd == "" || sess.Cwd == wsCwd {
				cwd = wsCwd
			}
		}
	}
	return cwd
}

// parseSessionID 解析 :id（uint，>0）。
func parseSessionID(c *gin.Context) (uint, bool) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "无效的会话 ID")
		return 0, false
	}
	return uint(id), true
}

// loadOwnedSession 加载 :id 对应会话并校验归属；失败时已写入错误响应。
func (h *SessionHandler) loadOwnedSession(c *gin.Context) (*models.Session, bool) {
	id, ok := parseSessionID(c)
	if !ok {
		return nil, false
	}
	sess, err := h.store.GetSessionByDBID(id)
	if err != nil || sess == nil {
		Fail(c, http.StatusNotFound, "SESSION_NOT_FOUND", "会话不存在")
		return nil, false
	}
	uid, ok := currentUserID(c)
	if !ok || sess.UserID != uid {
		Fail(c, http.StatusNotFound, "SESSION_NOT_FOUND", "会话不存在")
		return nil, false
	}
	return sess, true
}

// writeSessionError 将 service 层错误映射为统一 HTTP 响应。
func writeSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, agent.ErrAgentNotFound):
		Fail(c, http.StatusBadRequest, "AGENT_NOT_FOUND", "未知的 agent 类型")
	default:
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}

type createSessionRequest struct {
	AgentType   string `json:"agent_type" binding:"required"`
	WorkspaceID uint   `json:"workspace_id"`
	ModelValue  string `json:"model_value"`
	// Source 会话来源；允许 manual / orchestration（任务助手管理会话），空=manual。
	// orchestration 会话不登记 tasks.json（RegisterSessionTask 仅登记 manual），
	// 侧边栏也不作为普通任务展示，而是作为「编排对话」记录入口。
	Source string `json:"source"`
	// Cwd 可选的自定义工作目录（如用户选择的已存在 worktree 目录）；空=跟随工作区 cwd。
	Cwd string `json:"cwd"`
	// Yolo 创建时即开启本任务 YOLO。
	Yolo bool `json:"yolo"`
	// AutoWorktree 自动创建 worktree：由 AI 根据首条 prompt 生成分支名，
	// 在工作区仓库的 .worktrees 下创建并作为会话 cwd。与 Cwd 互斥（Cwd 优先）。
	AutoWorktree bool `json:"auto_worktree"`
	// Prompt 首条对话内容，仅用于 AutoWorktree 时的 AI 命名，不会在此接口发送给 agent。
	Prompt string `json:"prompt"`
}

// autoWorktreeNamePrompt 是 AI 生成 worktree 分支名的提示词模板。
const autoWorktreeNamePrompt = `请为以下开发任务生成一个简短的英文 git 分支名（kebab-case，2-4 个单词，只用小写字母、数字和连字符，概括任务核心内容）。
任务描述：{{prompt}}
仅输出分支名，不要输出其他任何内容。`

// generateWorktreeName 生成 worktree 名：优先用会话选定的 agent 做一次性 AI 命名，
// 失败时回退到从 prompt 首行提取，仍为空则用时间戳兑底。
func (h *SessionHandler) generateWorktreeName(ctx context.Context, req *createSessionRequest) string {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt != "" {
		aiCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		built := strings.ReplaceAll(autoWorktreeNamePrompt, "{{prompt}}", prompt)
		if resp, err := h.store.RunPromptOnce(aiCtx, req.AgentType, req.ModelValue, built); err == nil {
			if name := acplocal.SanitizeWorktreeName(resp); name != "" {
				return name
			}
		} else {
			slog.Warn("AI 生成 worktree 名失败，回退规则提取", "agent", req.AgentType, "err", err)
		}
		if name := acplocal.SanitizeWorktreeName(prompt); name != "" {
			return name
		}
	}
	return "task-" + time.Now().Format("20060102-150405")
}

// createAutoWorktree 在工作区仓库下自动创建 worktree 并返回其绝对路径。
// 非 git 仓库时自动初始化（与编排任务一致）；名称冲突时自动加序号。
func (h *SessionHandler) createAutoWorktree(ctx context.Context, req *createSessionRequest) (string, error) {
	wsCwd, err := h.store.GetWorkspaceCwd(req.WorkspaceID)
	if err != nil || strings.TrimSpace(wsCwd) == "" {
		return "", fmt.Errorf("无法解析工作区目录")
	}
	repoRoot, rootErr := acplocal.GitRoot(wsCwd)
	if rootErr != nil {
		if initErr := acplocal.GitInit(wsCwd); initErr != nil {
			return "", fmt.Errorf("初始化 git 仓库失败: %w", initErr)
		}
		repoRoot = wsCwd
		if root, e := acplocal.GitRoot(wsCwd); e == nil {
			repoRoot = root
		}
	}
	if err := acplocal.EnsureWorktreesDir(repoRoot); err != nil {
		return "", fmt.Errorf("创建 .worktrees 目录失败: %w", err)
	}
	name := acplocal.UniqueWorktreeName(repoRoot, h.generateWorktreeName(ctx, req))
	destPath := acplocal.WorktreePath(repoRoot, name)
	if err := acplocal.CreateWorktree(repoRoot, name, destPath, ""); err != nil {
		return "", fmt.Errorf("创建 worktree 失败: %w", err)
	}
	return destPath, nil
}

// Create POST /api/v1/sessions
func (h *SessionHandler) Create(c *gin.Context) {
	var req createSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	cwd := strings.TrimSpace(req.Cwd)
	// 会话来源：仅识别 orchestration（任务助手管理会话），其余一律按 manual 处理
	source := models.SessionSourceManual
	if req.Source == models.SessionSourceOrchestration {
		source = models.SessionSourceOrchestration
	}
	var sess *models.Session
	var err error
	switch {
	case cwd != "":
		// 用户选择了自定义工作目录：校验目录存在后将 cwd 固定到会话。
		info, statErr := os.Stat(cwd)
		if statErr != nil || !info.IsDir() {
			Fail(c, http.StatusBadRequest, "CWD_NOT_FOUND", "目录不存在: "+cwd)
			return
		}
		sess, err = h.store.CreateSessionWithCwd(c.Request.Context(), req.AgentType, req.WorkspaceID, uid, source, req.ModelValue, cwd)
	case req.AutoWorktree:
		// AI 自动命名并创建 worktree，以 worktree 路径作为会话 cwd。
		wtPath, wtErr := h.createAutoWorktree(c.Request.Context(), &req)
		if wtErr != nil {
			Fail(c, http.StatusBadRequest, "AUTO_WORKTREE_FAILED", wtErr.Error())
			return
		}
		sess, err = h.store.CreateSessionWithCwd(c.Request.Context(), req.AgentType, req.WorkspaceID, uid, source, req.ModelValue, wtPath)
	default:
		sess, err = h.store.CreateSessionWithSource(c.Request.Context(), req.AgentType, req.WorkspaceID, uid, source, req.ModelValue)
	}
	if err != nil {
		writeSessionError(c, err)
		return
	}
	if req.Yolo {
		if updated, yErr := h.store.SetSessionYolo(sess.ID, true); yErr == nil {
			sess = updated
		}
	}
	Success(c, http.StatusCreated, sess)
}

// List GET /api/v1/sessions?source=manual|scheduled
func (h *SessionHandler) List(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	source := strings.TrimSpace(c.Query("source"))
	if source == "" {
		sessions, err := h.store.ListSessions(uid)
		if err != nil {
			writeSessionError(c, err)
			return
		}
		Success(c, http.StatusOK, gin.H{"sessions": sessions})
		return
	}
	sessions, err := h.store.ListSessionsBySource(uid, source)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, gin.H{"sessions": sessions})
}

// RunningSessions GET /api/v1/sessions/running — 返回当前用户正在运行的会话 db_session_id 列表。
// 侧边栏据此展示哪些会话正在执行（旋转图标）。
func (h *SessionHandler) RunningSessions(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	ids, err := h.store.ListRunningDBSessionIDs(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"db_session_ids": ids})
}

// LatestByWorkspace GET /api/v1/sessions/latest?workspace_id=123&source=orchestration
// 返回指定 workspace 下最近一条会话（按 created_at DESC）。
// 任务助手（OrchestrationChatPanel）用它实现“一个工作区只复用一条管理会话”：
// 进入任务页或首次发送前调用本接口，命中则复用，未命中（404）再创建新会话。
// 仅校验 workspace 归属当前用户；source 非空时仅匹配该来源的会话
// （任务助手传 orchestration，避免复用普通对话会话）。
func (h *SessionHandler) LatestByWorkspace(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	wsIDStr := strings.TrimSpace(c.Query("workspace_id"))
	if wsIDStr == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 workspace_id")
		return
	}
	wsID, err := strconv.ParseUint(wsIDStr, 10, 64)
	if err != nil || wsID == 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "workspace_id 参数无效")
		return
	}
	source := strings.TrimSpace(c.Query("source"))
	sessions, err := h.store.FindSessionsByWorkspaceID(uint(wsID))
	if err != nil {
		writeSessionError(c, err)
		return
	}
	// 过滤归属当前用户的会话（workspace 已校验归属，但会话 user_id 防御性二次校验）
	for i := range sessions {
		if sessions[i].UserID != uid {
			continue
		}
		if source != "" && sessions[i].Source != source {
			continue
		}
		// 子会话不参与复用：本接口用于工作区级会话复用（如任务助手管理会话），
		// 而历史版本的编排任务执行会话是 source=orchestration 的子会话且 cwd 指向
		// 任务 worktree（可能已删除），误复用会导致"工作目录不存在"。
		// 与侧边栏一致：管理会话 = source=orchestration 且无父会话（顶级）。
		if sessions[i].ParentSessionID != nil {
			continue
		}
		Success(c, http.StatusOK, sessions[i])
		return
	}
	// 无归属当前用户的会话：返回 404，调用方据此新建
	Fail(c, http.StatusNotFound, "SESSION_NOT_FOUND", "该工作区暂无会话")
}

// Get GET /api/v1/sessions/:id
func (h *SessionHandler) Get(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	Success(c, http.StatusOK, sess)
}

// Connection GET /api/v1/sessions/:id/connection
// 返回会话所属 ACP 连接的状态与自动重连倒计时，供前端展示
// “连接已断开，N 秒后自动重连”。state 为空表示尚未建立连接。
func (h *SessionHandler) Connection(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	state, retryInMs, attempt := h.store.ConnectionStatusForSession(sess.SessionID)
	Success(c, http.StatusOK, gin.H{
		"state":            state,
		"next_retry_in_ms": retryInMs,
		"attempt":          attempt,
	})
}

// UpdateTitle PUT /api/v1/sessions/:id/title
func (h *SessionHandler) UpdateTitle(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		Fail(c, http.StatusBadRequest, "INVALID_TITLE", "标题不能为空")
		return
	}
	if err := h.store.UpdateTitle(sess.ID, title); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	sess.Title = title
	Success(c, http.StatusOK, sess)
}

// UpdateYolo PUT /api/v1/sessions/:id/yolo — 按任务开关 YOLO。
func (h *SessionHandler) UpdateYolo(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	var req struct {
		Yolo bool `json:"yolo"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	updated, err := h.store.SetSessionYolo(sess.ID, req.Yolo)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusOK, updated)
}

// Delete DELETE /api/v1/sessions/:id — 彻底删除会话及其消息。
func (h *SessionHandler) Delete(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	if err := h.store.DeleteSession(c.Request.Context(), sess.SessionID); err != nil {
		writeSessionError(c, err)
		return
	}
	// 同步注销 tasks.json 中该会话登记的任务（与首次发送时的登记对称），
	// 否则会话删除后任务条目残留，侧边栏/编排页仍会展示。失败仅记日志。
	h.unregisterFromTasks(sess)
	Success(c, http.StatusOK, struct{}{})
}

// unregisterFromTasks 删除会话在其工作区 tasks.json 中登记的任务条目。
// registrar 未注入或工作区缺失时静默跳过；失败仅记录日志，不影响删除结果。
func (h *SessionHandler) unregisterFromTasks(sess *models.Session) {
	if h.registrar == nil || sess.WorkspaceID == nil {
		return
	}
	cwd, err := h.store.GetWorkspaceCwd(*sess.WorkspaceID)
	if err != nil || cwd == "" {
		return
	}
	if err := h.registrar.UnregisterSessionTask(cwd, sess.ID); err != nil {
		slog.Warn("注销 tasks.json 会话任务失败", "session", sess.ID, "cwd", cwd, "err", err)
	}
}

// Messages GET /api/v1/sessions/:id/messages
// 三种查询模式（按优先级）：
//  1. before=<seq>：返回 sequence<before 的最近 limit 条 + has_more（前端「加载更多」向前翻页）。
//  2. limit/offset：按 offset 升序分页（兼容旧接口）。
//  3. 不传参数：返回默认页大小（最近 N 条）+ has_more。
//
// 响应体始终包含 has_more 字段（旧 offset 模式除外，保持兼容）。
func (h *SessionHandler) Messages(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	if beforeStr := c.Query("before"); beforeStr != "" {
		beforeSeq, _ := strconv.Atoi(beforeStr)
		limit, _ := strconv.Atoi(c.Query("limit"))
		msgs, hasMore, err := h.store.ListMessagesRecent(sess.SessionID, beforeSeq, limit)
		if err != nil {
			writeSessionError(c, err)
			return
		}
		Success(c, http.StatusOK, gin.H{"messages": msgs, "has_more": hasMore})
		return
	}
	if c.Query("limit") != "" || c.Query("offset") != "" {
		limit, _ := strconv.Atoi(c.Query("limit"))
		offset, _ := strconv.Atoi(c.Query("offset"))
		msgs, err := h.store.ListMessagesPaged(sess.SessionID, limit, offset)
		if err != nil {
			writeSessionError(c, err)
			return
		}
		// 旧 offset 模式保持原响应结构（仅 messages），避免破坏潜在调用方。
		Success(c, http.StatusOK, gin.H{"messages": msgs})
		return
	}
	// 默认：最近 N 条 + has_more（前端据此决定是否显示「加载更多」）。
	msgs, hasMore, err := h.store.ListMessagesRecent(sess.SessionID, 0, 0)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, gin.H{"messages": msgs, "has_more": hasMore})
}

// Executions GET /api/v1/sessions/:id/executions — 会话内按 execution_id 聚合的执行块（定时任务/笔记分类）。
func (h *SessionHandler) Executions(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	execs, err := h.store.ListExecutions(sess.SessionID)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	if execs == nil {
		execs = []repository.ExecutionAggregate{}
	}
	Success(c, http.StatusOK, gin.H{"executions": execs})
}

// Cancel POST /api/v1/sessions/:id/cancel
func (h *SessionHandler) Cancel(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	if sess.Status != models.SessionStatusActive {
		Fail(c, http.StatusConflict, "SESSION_NOT_ACTIVE", "会话不在活跃状态")
		return
	}
	if err := h.store.CancelSession(c.Request.Context(), sess.SessionID); err != nil {
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

// Resume POST /api/v1/sessions/:id/resume
// 恢复 error 状态会话或重开 closed 状态会话。
func (h *SessionHandler) Resume(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	updated, err := h.store.ResumeSession(c.Request.Context(), sess.SessionID)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, updated)
}

// ClearContext POST /api/v1/sessions/:id/clear-context
// 清理会话上下文：重置底层 ACP 会话（token 占用归零），保留数据库会话与历史消息。
func (h *SessionHandler) ClearContext(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	updated, err := h.store.ClearContext(c.Request.Context(), sess.SessionID)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, updated)
}

// Commands GET /api/v1/sessions/:id/commands — 返回会话缓存的可用 slash command。
func (h *SessionHandler) Commands(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	cmds, err := h.store.ListCommands(sess.SessionID)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	configured, _ := h.store.ListConfiguredCommandsForSession(sess.SessionID)
	items := buildCommandItems(cmds, configured)
	Success(c, http.StatusOK, gin.H{"commands": items})
}

// modeItem 是对外暴露的 session mode 描述。
type modeItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Modes GET /api/v1/sessions/:id/modes — 返回会话可用的 mode 列表（agent skill/模式）。
func (h *SessionHandler) Modes(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	modes, err := h.store.ListModes(sess.SessionID)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	items := make([]modeItem, 0, len(modes))
	for _, m := range modes {
		desc := ""
		if m.Description != nil {
			desc = *m.Description
		}
		items = append(items, modeItem{
			ID:          string(m.Id),
			Name:        m.Name,
			Description: desc,
		})
	}
	Success(c, http.StatusOK, gin.H{"modes": items})
}

// skillItem 是对外暴露的 Agent Skill 描述。
type skillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Scope       string `json:"scope"`
	Path        string `json:"path"`
}

// Skills GET /api/v1/sessions/:id/skills — 返回会话工作目录下发现的 Agent Skills。
func (h *SessionHandler) Skills(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	skills, err := h.store.ListSkills(sess.SessionID)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	items := make([]skillItem, 0, len(skills))
	for _, s := range skills {
		items = append(items, skillItem{
			Name:        s.Name,
			Description: s.Description,
			Location:    s.Location,
			Scope:       s.Scope,
			Path:        s.Path,
		})
	}
	Success(c, http.StatusOK, gin.H{"skills": items})
}

// configOptionItem 是对外暴露的 config option 描述。
type configOptionItem struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Category     string              `json:"category"`
	Type         string              `json:"type"`
	CurrentValue string              `json:"current_value"`
	Options      []configOptionValue `json:"options"`
}

// configOptionValue 是可选项的值描述。
type configOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ConfigOptions GET /api/v1/sessions/:id/config-options — 返回会话的 config option（含模型选择）。
func (h *SessionHandler) ConfigOptions(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	opts, err := h.store.ListConfigOptions(sess.SessionID)
	if err != nil {
		writeSessionError(c, err)
		return
	}
	items := make([]configOptionItem, 0, len(opts))
	for _, opt := range opts {
		item := configOptionItem{
			Type: "boolean",
			// 初始化为空切片，避免 JSON 序列化为 null 导致前端 o.options.length 崩溃
			Options: []configOptionValue{},
		}
		if opt.Select != nil {
			item.ID = string(opt.Select.Id)
			item.Name = opt.Select.Name
			item.Type = "select"
			item.CurrentValue = string(opt.Select.CurrentValue)
			if opt.Select.Category != nil {
				item.Category = string(*opt.Select.Category)
			}
			if opt.Select.Description != nil {
				item.Name = opt.Select.Name
			}
			if opt.Select.Options.Ungrouped != nil {
				for _, o := range *opt.Select.Options.Ungrouped {
					desc := ""
					if o.Description != nil {
						desc = *o.Description
					}
					item.Options = append(item.Options, configOptionValue{
						Value:       string(o.Value),
						Name:        o.Name,
						Description: desc,
					})
				}
			}
			if opt.Select.Options.Grouped != nil {
				for _, g := range *opt.Select.Options.Grouped {
					for _, o := range g.Options {
						desc := ""
						if o.Description != nil {
							desc = *o.Description
						}
						item.Options = append(item.Options, configOptionValue{
							Value:       string(o.Value),
							Name:        o.Name,
							Description: desc,
						})
					}
				}
			}
		}
		// 会话记录中保存了用户发送时选择的模型（ModelValue），
		// 用它覆盖 model 选项的 CurrentValue，避免回显 agent 默认模型。
		if item.Category == "model" && sess.ModelValue != "" && optionValuePresent(item.Options, sess.ModelValue) {
			item.CurrentValue = sess.ModelValue
		}
		items = append(items, item)
	}
	Success(c, http.StatusOK, gin.H{"config_options": items})
}

// optionValuePresent 判断给定值是否存在于可选项列表中。
func optionValuePresent(options []configOptionValue, value string) bool {
	for _, o := range options {
		if o.Value == value {
			return true
		}
	}
	return false
}

type setConfigOptionRequest struct {
	ConfigID string `json:"config_id" binding:"required"`
	Value    string `json:"value" binding:"required"`
}

// SetConfigOption POST /api/v1/sessions/:id/config-options — 设置会话的 config option 值。
func (h *SessionHandler) SetConfigOption(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	var req setConfigOptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if err := h.store.SetConfigOption(c.Request.Context(), sess.SessionID, req.ConfigID, req.Value); err != nil {
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

type setModeRequest struct {
	ModeID string `json:"mode_id" binding:"required"`
}

// SetMode POST /api/v1/sessions/:id/mode — 切换会话模式（ask / agent / edit 等）。
func (h *SessionHandler) SetMode(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	var req setModeRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ModeID) == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "mode_id 不能为空")
		return
	}
	if sess.Status != models.SessionStatusActive {
		// error/closed 会话：切换模式前先自动恢复连接，与发送 prompt 行为一致。
		if _, err := h.store.ResumeSession(c.Request.Context(), sess.SessionID); err != nil {
			writeSessionError(c, err)
			return
		}
	}
	if err := h.store.SetSessionMode(c.Request.Context(), sess.SessionID, req.ModeID); err != nil {
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

type respondPermissionRequest struct {
	OptionID  string `json:"option_id"`
	Cancelled bool   `json:"cancelled"`
}

// RespondPermission POST /api/v1/sessions/:id/permissions/:requestId/respond
func (h *SessionHandler) RespondPermission(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	requestID := strings.TrimSpace(c.Param("requestId"))
	if requestID == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 requestId")
		return
	}
	var req respondPermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if !req.Cancelled && strings.TrimSpace(req.OptionID) == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "option_id 不能为空")
		return
	}
	if err := h.store.RespondPermission(sess.SessionID, requestID, req.OptionID, req.Cancelled); err != nil {
		if errors.Is(err, acplocal.ErrPermissionNotFound) {
			Fail(c, http.StatusNotFound, "PERMISSION_NOT_FOUND", "权限请求不存在或已过期")
			return
		}
		writeSessionError(c, err)
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

type promptRequest struct {
	Prompt string `json:"prompt" binding:"required"`
}

// sseKeepaliveInterval 是 agent 静默期向客户端发送 SSE 心跳的周期。
// 心跳为 SSE 注释行（": keepalive\n\n"），前端 reader 收到任意字节即重置空闲超时计时器，
// 防止长任务（跑测试、大目录搜索等）期间因无数据触发前端 120s 空闲超时误判 agent 卡死。
const sseKeepaliveInterval = 30 * time.Second

// streamSSEMessages 设置 SSE 响应头并以 SSE 格式写入消息流。
// 适用于发起新 prompt / 恢复任务的端点。返回后本次响应结束。
func streamSSEMessages(c *gin.Context, ch <-chan models.Message) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	streamSSELoop(c, ch)
}

// streamSSELoop 执行 SSE 消息流写入的核心循环（不设置响应头）。
// 供 streamSSEMessages 与已自行写入响应头/补发遗漏消息的 Stream 端点共用。
//
// 与裸 c.Stream 的差异（解决 prompt 生命周期解耦 HTTP 后的善后问题）：
//   - 周期发送心跳注释行保活，防前端空闲超时；
//   - 客户端断开（c.Request.Context 取消）时立即返回，但后台继续排空 ch，
//     直到服务端 prompt goroutine 关闭 ch——否则 prompt goroutine 写满 256 缓冲后阻塞，
//     导致 running_task 卡在 running、activePrompts 永不释放（等于换种方式卡死）。
//     排空丢弃的消息对正确性无影响：消息已落库 + 经广播器分发给断点续传订阅者。
func streamSSELoop(c *gin.Context, ch <-chan models.Message) {
	ctx := c.Request.Context()
	flusher, _ := c.Writer.(http.Flusher)
	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				return
			}
			b, _ := json.Marshal(msg)
			// 每条消息附带 id: <sequence>，供客户端断点续传（Last-Event-ID）
			_, _ = fmt.Fprintf(c.Writer, "id: %d\ndata: %s\n\n", msg.Sequence, b)
			if flusher != nil {
				flusher.Flush()
			}
		case <-keepalive.C:
			// 心跳注释行：无 data: 行，前端 parseSSEEvents 忽略；reader 收到字节即重置空闲计时器
			_, _ = fmt.Fprintf(c.Writer, ": keepalive\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case <-ctx.Done():
			// 客户端断开（页面关闭/网络中断/前端空闲超时 abort）。
			// prompt 已与 HTTP ctx 解耦、仍在后台运行，故不可关闭 ch；改为后台排空防 goroutine 阻塞。
			slog.Debug("SSE 客户端断开，后台排空消息流", "path", c.Request.URL.Path)
			drainInBackground(ch)
			return
		}
	}
}

// drainInBackground 在后台排空 ch 直至其关闭。用于 SSE 客户端断开后，
// 避免服务端 prompt goroutine 因向 ch 写入无人消费的消息而阻塞。
// 调用方应在确认客户端已断开、不再读取 ch 时调用。
func drainInBackground(ch <-chan models.Message) {
	go func() {
		for range ch {
		}
	}()
}

// Prompt POST /api/v1/sessions/:id/prompt （SSE 流）
func (h *SessionHandler) Prompt(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	var req promptRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "prompt 不能为空")
		return
	}
	if sess.Status != models.SessionStatusActive && sess.Status != models.SessionStatusPending {
		// 自动恢复 error 或 closed 状态的会话
		if _, err := h.store.ResumeSession(c.Request.Context(), sess.SessionID); err != nil {
			writeSessionError(c, err)
			return
		}
	}
	// 把会话登记到工作区 tasks.json，使"新建对话"与编排任务统一在 tasks.json 中可见。
	// 不能用 pending 状态判断"首次发送"：首发前的 setConfigOption/切模式等调用会经
	// 自动恢复把 pending 会话提前激活为 active，导致登记被跳过。registrar 按
	// db_session_id 幂等去重，每次 prompt 调用均安全；仅 manual 顶级会话登记
	// （registrar 内部过滤），子会话/定时/分类会话由各自引擎管理。
	// 失败仅记录日志，不阻断 prompt。
	h.registerToTasks(c, sess, req.Prompt)
	ch, err := h.store.Prompt(c.Request.Context(), sess.SessionID, req.Prompt)
	if err != nil {
		writeSessionError(c, err)
		return
	}

	streamSSEMessages(c, ch)
}

// registerToTasks 把会话登记到其工作区 cwd 下的 tasks.json。
// 通过 SessionStore.GetWorkspaceCwd 解析工作区目录；registrar 未注入或工作区缺失时静默跳过。
// 登记失败仅记录日志，不影响后续 prompt 流程。
func (h *SessionHandler) registerToTasks(c *gin.Context, sess *models.Session, prompt string) {
	if h.registrar == nil {
		return
	}
	if sess.WorkspaceID == nil {
		return
	}
	cwd, err := h.store.GetWorkspaceCwd(*sess.WorkspaceID)
	if err != nil || cwd == "" {
		return
	}
	if err := h.registrar.RegisterSessionTask(cwd, sess, prompt); err != nil {
		slog.Warn("登记会话到 tasks.json 失败", "session", sess.ID, "cwd", cwd, "err", err)
	}
}

// Stream GET /api/v1/sessions/:id/stream
// 订阅会话当前进行中的 prompt 流（断点续传），不发起新 prompt。
// 客户端通过 Last-Event-ID 头携带最后收到的 sequence，服务端先从 DB 补齐遗漏消息，再推送实时流。
func (h *SessionHandler) Stream(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}

	// 解析 Last-Event-ID 头（客户端最后收到的 sequence）
	lastSeq := 0
	if lei := c.GetHeader("Last-Event-ID"); lei != "" {
		if n, err := strconv.Atoi(lei); err == nil && n >= 0 {
			lastSeq = n
		}
	}

	missed, ch, err := h.store.SubscribeSession(sess.SessionID, lastSeq)
	if err != nil {
		writeSessionError(c, err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	// 先发送 DB 补齐的遗漏消息（每条带 id: sequence）
	for _, msg := range missed {
		b, _ := json.Marshal(msg)
		_, _ = fmt.Fprintf(c.Writer, "id: %d\ndata: %s\n\n", msg.Sequence, b)
	}
	c.Writer.Flush()

	// 若无实时 channel（无活跃 prompt），直接结束
	if ch == nil {
		_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		return
	}

	// 复用 keepalive + 断开排空逻辑（headers 与遗漏消息已写入）
	streamSSELoop(c, ch)
}

// InterruptedTasks GET /api/v1/sessions/:id/interrupted-tasks
// 返回指定会话下因服务重启而中断的任务列表。
func (h *SessionHandler) InterruptedTasks(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	tasks, err := h.store.ListInterruptedTasks(sess.ID)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "LIST_INTERRUPTED_FAILED", "查询中断任务失败")
		return
	}
	Success(c, http.StatusOK, gin.H{"tasks": tasks})
}

// ResumeInterruptedTask POST /api/v1/running-tasks/:taskId/resume
// 恢复中断的任务：自动 ResumeSession 并重新发送原 prompt。
func (h *SessionHandler) ResumeInterruptedTask(c *gin.Context) {
	taskIDStr := c.Param("taskId")
	taskID, err := strconv.ParseUint(taskIDStr, 10, 64)
	if err != nil || taskID == 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "无效的任务 ID")
		return
	}

	ch, err := h.store.ResumeInterruptedTask(c.Request.Context(), uint(taskID))
	if err != nil {
		writeSessionError(c, err)
		return
	}

	streamSSEMessages(c, ch)
}
