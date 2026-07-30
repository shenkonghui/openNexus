package agent

import (
	"context"
	"errors"

	acpsdk "github.com/coder/acp-go-sdk"

	"opennexus/internal/acp"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// Router 路由用户请求到对应的 agent 后端，委托 acp.Service 执行。
type Router struct {
	registry *Registry
	service  *acp.Service
}

// NewRouter 创建新的 Router。
func NewRouter(registry *Registry, service *acp.Service) *Router {
	return &Router{
		registry: registry,
		service:  service,
	}
}

// CreateSession 创建会话：校验 agentType 后委托 service。
// modelValue 非空时在会话创建后立即设置该模型。
func (r *Router) CreateSession(ctx context.Context, agentType string, workspaceID uint, userID uint, modelValue string) (*models.Session, error) {
	return r.CreateSessionWithSource(ctx, agentType, workspaceID, userID, models.SessionSourceManual, modelValue)
}

// CreateSessionWithSource 创建会话并指定来源（manual/scheduled）。
// modelValue 非空时在会话创建后立即设置该模型。
func (r *Router) CreateSessionWithSource(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue string) (*models.Session, error) {
	if _, err := r.registry.Get(agentType); err != nil {
		return nil, err
	}
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.CreateSessionWithSource(ctx, agentType, workspaceID, userID, source, modelValue)
}

// CreateSessionWithCwd 创建会话并将 cwd 固定为用户指定的目录（如已存在的 git worktree）。
// cwd 非空时覆盖工作区 cwd；空字符串时行为与 CreateSessionWithSource 一致。
func (r *Router) CreateSessionWithCwd(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue, cwd string) (*models.Session, error) {
	if _, err := r.registry.Get(agentType); err != nil {
		return nil, err
	}
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.CreateSessionWithCwd(ctx, agentType, workspaceID, userID, source, modelValue, cwd)
}

// ResumeSession 恢复或重开会话，委托 service。
func (r *Router) ResumeSession(ctx context.Context, sessionID string) (*models.Session, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ResumeSession(ctx, sessionID)
}

// ClearContext 清理会话上下文（重置底层 ACP 会话），委托 service。
func (r *Router) ClearContext(ctx context.Context, sessionID string) (*models.Session, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ClearContext(ctx, sessionID)
}

// ListAgents 返回所有已注册的 agent。
func (r *Router) ListAgents() []*AgentDescriptor {
	return r.registry.List()
}

// AgentAvailable 判断指定 agent 类型当前是否已注册（未被停用或删除）。
func (r *Router) AgentAvailable(agentType string) bool {
	_, err := r.registry.Get(agentType)
	return err == nil
}

// DefaultAgentType 返回排序后首个已注册 agent 的类型（无已注册 agent 时返回空串）。
// 供编排任务未显式指定 agent 时回退使用。
func (r *Router) DefaultAgentType() string {
	agents := r.registry.List()
	if len(agents) == 0 {
		return ""
	}
	return agents[0].Type
}

// ListCommands 返回会话缓存的可用 slash command 列表，委托 service。
func (r *Router) ListCommands(sessionID string) ([]acpsdk.AvailableCommand, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListCommands(sessionID)
}

// ListConfigOptions 返回会话缓存的 config option 列表，委托 service。
func (r *Router) ListConfigOptions(sessionID string) ([]acpsdk.SessionConfigOption, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListConfigOptions(sessionID)
}

// CachedModelOptions 返回指定 agent 类型的可用模型 config option，委托 service。
func (r *Router) CachedModelOptions(agentType string) []acpsdk.SessionConfigOption {
	if r.service == nil {
		return nil
	}
	return r.service.CachedModelOptions(agentType)
}

// CachedCommands 返回指定 agent 类型缓存的 slash command，委托 service。
func (r *Router) CachedCommands(agentType string, cwd string) []acpsdk.AvailableCommand {
	if r.service == nil {
		return nil
	}
	return r.service.CachedCommands(agentType, cwd)
}

// ListConfiguredCommands 扫描指定 cwd 下配置的 slash command。
func (r *Router) ListConfiguredCommands(cwd string) []acp.SlashCommand {
	if r.service == nil {
		return nil
	}
	return r.service.ListConfiguredCommands(cwd)
}

// ListConfiguredCommandsForSession 扫描会话工作区下配置的 slash command。
func (r *Router) ListConfiguredCommandsForSession(sessionID string) ([]acp.SlashCommand, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListConfiguredCommandsForSession(sessionID)
}

// CachedModes 返回指定 agent 类型缓存的 session mode，委托 service。
func (r *Router) CachedModes(agentType string) []acpsdk.SessionMode {
	if r.service == nil {
		return nil
	}
	return r.service.CachedModes(agentType)
}

// ProbeConfigOptions 创建临时会话探测指定 agent 类型的 config options，随后删除，委托 service。
func (r *Router) ProbeConfigOptions(ctx context.Context, agentType string, userID uint) ([]acpsdk.SessionConfigOption, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ProbeConfigOptions(ctx, agentType, userID)
}

// PreconnectAgent 异步预连接指定 agent 与工作目录，委托 service。
func (r *Router) PreconnectAgent(agentType, cwd string) error {
	if _, err := r.registry.Get(agentType); err != nil {
		return err
	}
	if r.service == nil {
		return errors.New("service 未配置")
	}
	r.service.PreconnectAsync(agentType, cwd)
	return nil
}

// ListModes 返回会话可用的 mode 列表（agent skill/模式），委托 service。
func (r *Router) ListModes(sessionID string) ([]acpsdk.SessionMode, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListModes(sessionID)
}

// ListSkills 扫描会话工作目录下的 Agent Skills（agentskills.io 规范），委托 service。
func (r *Router) ListSkills(sessionID string) ([]acp.Skill, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListSkills(sessionID)
}

// SetConfigOption 设置会话的 config option 值，委托 service。
func (r *Router) SetConfigOption(ctx context.Context, sessionID, configID, value string) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.SetConfigOption(ctx, sessionID, configID, value)
}

// SetSessionMode 切换会话模式，委托 service。
func (r *Router) SetSessionMode(ctx context.Context, sessionID, modeID string) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.SetSessionMode(ctx, sessionID, modeID)
}

// RespondPermission 提交权限请求响应，委托 service。
func (r *Router) RespondPermission(sessionID, requestID, optionID string, cancelled bool) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.RespondPermission(sessionID, requestID, optionID, cancelled)
}

// UpdateTitle 更新会话标题，委托 service。
func (r *Router) UpdateTitle(dbSessionID uint, title string) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.UpdateTitle(dbSessionID, title)
}

// SetSessionYolo 设置会话级 YOLO，委托 service。
func (r *Router) SetSessionYolo(dbSessionID uint, yolo bool) (*models.Session, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.SetSessionYolo(dbSessionID, yolo)
}

// ListAgentStatus 返回所有 agent 类型的连接状态，委托 service。
func (r *Router) ListAgentStatus() []acp.AgentStatus {
	if r.service == nil {
		return nil
	}
	return r.service.ListAgentStatus()
}

// AgentACPInfo 返回指定 agent 类型最近一次 ACP 握手的能力信息，委托 service。
func (r *Router) AgentACPInfo(agentType string) (acp.AgentACPInfo, bool) {
	if r.service == nil {
		return acp.AgentACPInfo{}, false
	}
	return r.service.AgentACPInfo(agentType)
}

// AgentAuthTerminalSpec 返回指定 agent 类型的 terminal 认证登录进程启动参数，委托 service。
func (r *Router) AgentAuthTerminalSpec(agentType, methodID string) (acp.AuthTerminalSpec, error) {
	if r.service == nil {
		return acp.AuthTerminalSpec{}, errors.New("service 未配置")
	}
	return r.service.AgentAuthTerminalSpec(agentType, methodID)
}

// ====== SessionStore delegation methods ======

func (r *Router) Prompt(ctx context.Context, sessionID, prompt string) (<-chan models.Message, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.Prompt(ctx, sessionID, prompt)
}

// RunPromptOnce 在临时 ACP 会话中发送 prompt 并收集 assistant 文本，不落库。
// 用于任务自动打标签 / 标题生成等一次性 AI 调用。
func (r *Router) RunPromptOnce(ctx context.Context, agentType, modelValue, prompt string) (string, error) {
	if r.service == nil {
		return "", errors.New("service 未配置")
	}
	return r.service.RunPromptOnce(ctx, agentType, modelValue, prompt)
}

// RunSubAgent 在临时 ACP 会话中执行一次 subagent 任务，不落库。
// 注入全局 mcpServers + SystemPrompt，用于主 agent 通过 MCP 工具调起预定义的 subagent。
func (r *Router) RunSubAgent(ctx context.Context, cfg acp.SubAgentRunConfig) (string, error) {
	if r.service == nil {
		return "", errors.New("service 未配置")
	}
	if _, err := r.registry.Get(cfg.AgentType); err != nil {
		return "", err
	}
	return r.service.RunSubAgent(ctx, cfg)
}

// RunSessionTask 创建持久会话并阻塞运行一次性任务，收集 assistant 文本后返回（落库）。
// 用于 MCP run_session_task 工具：发起一个需要持久化、可追溯、可继续的子任务。
func (r *Router) RunSessionTask(ctx context.Context, cfg acp.SessionTaskConfig) (acp.SessionTaskResult, error) {
	if r.service == nil {
		return acp.SessionTaskResult{}, errors.New("service 未配置")
	}
	if _, err := r.registry.Get(cfg.AgentType); err != nil {
		return acp.SessionTaskResult{}, err
	}
	return r.service.RunSessionTask(ctx, cfg)
}

func (r *Router) PromptWithExecution(ctx context.Context, sessionID, prompt string, executionID *uint) (<-chan models.Message, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.PromptWithExecution(ctx, sessionID, prompt, executionID)
}

func (r *Router) ListSessionsBySource(userID uint, source string) ([]models.Session, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListSessionsBySource(userID, source)
}

func (r *Router) ListExecutions(sessionID string) ([]repository.ExecutionAggregate, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListExecutions(sessionID)
}

func (r *Router) NextExecutionID(sessionID string) (uint, error) {
	if r.service == nil {
		return 0, errors.New("service 未配置")
	}
	return r.service.NextExecutionID(sessionID)
}

func (r *Router) CancelSession(ctx context.Context, sessionID string) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.CancelSession(ctx, sessionID)
}

func (r *Router) DeleteSession(ctx context.Context, sessionID string) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.DeleteSession(ctx, sessionID)
}

func (r *Router) ListSessions(userID uint) ([]models.Session, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListSessions(userID)
}

func (r *Router) GetSessionByDBID(id uint) (*models.Session, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.GetSessionByDBID(id)
}

func (r *Router) ListMessages(sessionID string) ([]models.Message, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListMessages(sessionID)
}

// ListMessagesPaged 分页查询消息（透传至 service）。
func (r *Router) ListMessagesPaged(sessionID string, limit, offset int) ([]models.Message, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListMessagesPaged(sessionID, limit, offset)
}

// ListMessagesByKind 仅查询指定 kind 的消息（透传至 service）。
func (r *Router) ListMessagesByKind(sessionID string, kind string) ([]models.Message, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListMessagesByKind(sessionID, kind)
}

// ListMessagesRecent 返回最近的若干条消息 + 是否还有更早的消息（透传至 service）。
// beforeSeq>0 时仅返回 sequence<beforeSeq 的消息；<=0 时不限制。
func (r *Router) ListMessagesRecent(sessionID string, beforeSeq int, limit int) ([]models.Message, bool, error) {
	if r.service == nil {
		return nil, false, errors.New("service 未配置")
	}
	return r.service.ListMessagesRecent(sessionID, beforeSeq, limit)
}

// FindMessageByID 按消息主键查询单条消息。
func (r *Router) FindMessageByID(messageID uint) (*models.Message, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.FindMessageByID(messageID)
}

// DeleteMessagesFromSequence 删除指定会话中 sequence 大于等于 fromSeq 的消息。
func (r *Router) DeleteMessagesFromSequence(sessionID string, fromSeq int) (int64, error) {
	if r.service == nil {
		return 0, errors.New("service 未配置")
	}
	return r.service.DeleteMessagesFromSequence(sessionID, fromSeq)
}

func (r *Router) GetWorkspaceCwd(workspaceID uint) (string, error) {
	if r.service == nil {
		return "", errors.New("service 未配置")
	}
	return r.service.GetWorkspaceCwd(workspaceID)
}

// ====== WorkspaceStore delegation methods ======

func (r *Router) CreateWorkspace(ws *models.Workspace) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.CreateWorkspace(ws)
}

func (r *Router) FindWorkspaceByID(id uint) (*models.Workspace, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.FindWorkspaceByID(id)
}

func (r *Router) FindWorkspacesByUserID(userID uint) ([]models.Workspace, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.FindWorkspacesByUserID(userID)
}

func (r *Router) FindWorkspaceByUserIDAndCwd(userID uint, cwd string) (*models.Workspace, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.FindWorkspaceByUserIDAndCwd(userID, cwd)
}

func (r *Router) FindDefaultWorkspaceByUserID(userID uint) (*models.Workspace, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.FindDefaultWorkspaceByUserID(userID)
}

func (r *Router) UpdateWorkspace(id uint, updates map[string]interface{}) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.UpdateWorkspace(id, updates)
}

func (r *Router) DeleteWorkspace(id uint) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.DeleteWorkspace(id)
}

func (r *Router) WorkspaceSessionCount(workspaceID uint) (int64, error) {
	if r.service == nil {
		return 0, errors.New("service 未配置")
	}
	return r.service.WorkspaceSessionCount(workspaceID)
}

func (r *Router) FindSessionsByWorkspaceID(workspaceID uint) ([]models.Session, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.FindSessionsByWorkspaceID(workspaceID)
}

func (r *Router) DeleteSessionWithMessages(session *models.Session) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.DeleteSessionWithMessages(session)
}

// ====== 流续传与中断任务恢复 delegation methods ======

// SubscribeSession 订阅会话当前进行中的 prompt 流（断点续传）。
func (r *Router) SubscribeSession(sessionID string, lastSeq int) ([]models.Message, <-chan models.Message, error) {
	if r.service == nil {
		return nil, nil, errors.New("service 未配置")
	}
	return r.service.SubscribeSession(sessionID, lastSeq)
}

// HasActivePrompt 判断会话是否有进行中的 prompt。
func (r *Router) HasActivePrompt(sessionID string) bool {
	if r.service == nil {
		return false
	}
	return r.service.HasActivePrompt(sessionID)
}

// GoalActive 判断会话是否有生效中的 goal（评估中或等待续轮）。
func (r *Router) GoalActive(sessionID string) bool {
	if r.service == nil {
		return false
	}
	return r.service.GoalActive(sessionID)
}

// EnableGoal 为会话自动开启 goal 模式（编排任务重发/续跑时恢复 goal）。
func (r *Router) EnableGoal(sessionID, condition string) error {
	if r.service == nil {
		return errors.New("service 未配置")
	}
	return r.service.EnableGoal(sessionID, condition)
}

// LastRunStatus 返回会话最近一次 prompt 的 running_task 终态（done/interrupted）。
func (r *Router) LastRunStatus(dbSessionID uint) string {
	if r.service == nil {
		return ""
	}
	return r.service.LastRunStatus(dbSessionID)
}

// ConnectionStatusForSession 返回会话所属 ACP 连接的状态与重连倒计时信息。
func (r *Router) ConnectionStatusForSession(sessionID string) (state string, retryInMs int64, attempt int) {
	if r.service == nil {
		return "", 0, 0
	}
	return r.service.ConnectionStatusForSession(sessionID)
}

// ApplyPermissions 把权限规则下发到所有连接（设置页保存后热更新）。
func (r *Router) ApplyPermissions(mode string, allow, ask, deny []string) {
	if r.service == nil {
		return
	}
	r.service.ApplyPermissions(mode, allow, ask, deny)
}

// CurrentPermissions 返回当前生效的权限规则（供设置页读取）。
func (r *Router) CurrentPermissions() (mode string, allow, ask, deny []string) {
	if r.service == nil {
		return "", nil, nil, nil
	}
	return r.service.CurrentPermissions()
}

// ListInterruptedTasks 返回指定会话下因服务重启而中断的任务。
func (r *Router) ListInterruptedTasks(dbSessionID uint) ([]models.RunningTask, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListInterruptedTasks(dbSessionID)
}

// ResumeInterruptedTask 恢复中断的任务。
func (r *Router) ResumeInterruptedTask(ctx context.Context, taskID uint) (<-chan models.Message, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ResumeInterruptedTask(ctx, taskID)
}

// ListRunningDBSessionIDs 返回指定用户下所有正在运行的 db_session_id。
func (r *Router) ListRunningDBSessionIDs(userID uint) ([]uint, error) {
	if r.service == nil {
		return nil, errors.New("service 未配置")
	}
	return r.service.ListRunningDBSessionIDs(userID)
}

// SubscribeAgentTerminal 按 DB 会话 ID 订阅 agent 终端事件（供前端 WebSocket 桥接），委托 service。
func (r *Router) SubscribeAgentTerminal(dbSessionID uint) ([]acp.TerminalSnapshot, <-chan acp.TerminalEvent, func(), error) {
	if r.service == nil {
		return nil, nil, nil, errors.New("service 未配置")
	}
	snapshots, ch, cancel := r.service.SubscribeAgentTerminal(dbSessionID)
	return snapshots, ch, cancel, nil
}
