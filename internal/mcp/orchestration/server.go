// Package orchestrationmcp 提供 opennexus-orchestration MCP server，让主 agent 通过 MCP 工具
// 管理工作区下的任务编排（tasks.json）：新增/更新/删除任务、启停任务、调整并发上限、列出现状。
//
// 编排任务持久化于工作区 cwd 下的 tasks.json，由调度器读取并基于 git worktree 隔离执行每个任务。
// 本 server 从原 opennexus-subagent 抽离而来，作为独立 MCP server 对外暴露。
//
// 暴露 7 个工具：
//   - create_orchestration_task：新增编排任务
//   - update_orchestration_task：更新编排任务可编辑字段
//   - delete_orchestration_task：删除编排任务
//   - start_orchestration_task： 启动编排任务
//   - stop_orchestration_task：  停止编排任务
//   - set_orchestration_max_parallel：设置并发上限
//   - list_orchestration_tasks： 列出编排任务现状
//
// 鉴权复用 opennexus-notes 的 Bearer token 体系（用户级共享一个 token）。
package orchestrationmcp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// WorkspaceResolver 解析工作区并校验归属（取 cwd 用于编排任务）。
// 由 *agent.Router 实现（复用 FindWorkspaceByID）。
type WorkspaceResolver interface {
	FindWorkspaceByID(id uint) (*models.Workspace, error)
}

// OrchestratorTaskCreator 由 *services.OrchestratorService 实现，
// 用于通过 MCP 工具管理编排任务（创建/更新/删除/启停/调整并发）。
type OrchestratorTaskCreator interface {
	UpsertTask(cwd string, task models.OrchestrationTask) error
	DeleteTask(cwd, taskID string) error
	SetMaxParallel(cwd string, maxParallel int) error
	Stop(cwd, taskID string) error
	Start(ctx context.Context, cwd string, workspaceID uint, userID uint, taskID string) error
	Load(cwd string) (*models.OrchestrationDef, error)
}

// Handler 返回带 Bearer 鉴权的 orchestration MCP Streamable HTTP Handler。
//
// prefsRepo 用于解析"继承父 agent"：任务不指定 agent 后端时取用户最近使用的 agent 类型。
// wsResolver / orchCreator 用于编排工具（可传 nil 禁用）。
func Handler(settings *repository.NoteSettingsRepository, prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator) http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return newServer(prefsRepo, wsResolver, orchCreator)
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := BearerUserID(r, settings)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r.WithContext(withUserID(r.Context(), uid)))
	})
}

func newServer(prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "opennexus-orchestration", Version: "1.0.0"}, nil)

	// addToolSafe 注册单个工具，并 recover mcp.AddTool 的 panic。
	//
	// go-sdk 的 mcp.AddTool 在输入结构体 jsonschema tag 非法时会 panic（例如
	// tag 形如 "x=1" 命中 "tag must not begin with 'WORD='" 规则）。若不兜底，
	// 该 panic 会沿 StreamableHTTPHandler 冒泡成 HTTP 500，导致 MCP 客户端判定
	// 整个 opennexus-orchestration server 连接失败、全部工具不可见。
	// 这里按工具粒度 recover：仅跳过出问题的那一个工具，server 仍可正常列出其余工具。
	addTool := func(name string, register func()) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("orchestration MCP: 跳过工具 %s（注册失败）: %v", name, r)
			}
		}()
		register()
	}

	addTool("create_orchestration_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "create_orchestration_task",
			Description: "在当前工作区的任务编排（tasks.json）中新增一个编排任务。任务默认 status=pending、priority=p1，可由编排调度器启动（基于 git worktree 隔离执行）。这是管理编排任务的首选方式（结构化、自带校验），优先于手写 tasks.json。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in createOrchTaskIn) (*mcp.CallToolResult, createOrchTaskOut, error) {
			return handleCreateOrchTask(ctx, prefsRepo, wsResolver, orchCreator, in)
		})
	})

	addTool("update_orchestration_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "update_orchestration_task",
			Description: "更新编排任务的可编辑字段（title/detail/agent_type/model_value/priority/depends_on）。按 task_id 匹配；运行时字段（status/session_id/worktree 等）保持不变。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateOrchTaskIn) (*mcp.CallToolResult, updateOrchTaskOut, error) {
			return handleUpdateOrchTask(ctx, prefsRepo, wsResolver, orchCreator, in)
		})
	})

	addTool("delete_orchestration_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "delete_orchestration_task",
			Description: "按 task_id 删除编排任务。若任务正在运行会先停止并清理其 worktree。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteOrchTaskIn) (*mcp.CallToolResult, deleteOrchTaskOut, error) {
			return handleDeleteOrchTask(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("start_orchestration_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "start_orchestration_task",
			Description: "启动编排任务。task_id 留空则启动全部待执行（pending/failed/canceled/interrupt）任务，否则仅启动指定任务。任务在其专属 git worktree 内执行。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in startOrchTaskIn) (*mcp.CallToolResult, startOrchTaskOut, error) {
			return handleStartOrchTask(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("stop_orchestration_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "stop_orchestration_task",
			Description: "停止编排任务。task_id 留空则停止全部运行中/排队中任务，否则仅停止指定任务。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopOrchTaskIn) (*mcp.CallToolResult, stopOrchTaskOut, error) {
			return handleStopOrchTask(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("set_orchestration_max_parallel", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "set_orchestration_max_parallel",
			Description: "设置编排并发上限 max_parallel（范围为 1~16，值为 1 时串行执行）。影响后续任务的并发调度。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in setOrchMaxParallelIn) (*mcp.CallToolResult, setOrchMaxParallelOut, error) {
			return handleSetOrchMaxParallel(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("list_orchestration_tasks", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "list_orchestration_tasks",
			Description: "列出当前工作区编排的所有任务（含 id/title/status/priority/agent_type/branch/cwd 等运行时状态），用于了解现状后再决定增删改或调度。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in listOrchTasksIn) (*mcp.CallToolResult, listOrchTasksOut, error) {
			return handleListOrchTasks(ctx, wsResolver, orchCreator, in)
		})
	})

	return srv
}

// ====== agent 类型解析（继承父 agent） ======

// resolveAgentType 解析 agent 类型：显式指定优先，否则"继承父 agent"（用户最近使用的 agent）。
func resolveAgentType(prefsRepo *repository.UserAgentPrefsRepository, uid uint, explicit string) (string, error) {
	if at := strings.TrimSpace(explicit); at != "" {
		return at, nil
	}
	return resolveInheritedAgentType(prefsRepo, uid)
}

// resolveInheritedAgentType 返回用户最近使用的 agent 类型（"继承父 agent"语义）。
// 用户从未使用过任何 agent 时返回错误。
func resolveInheritedAgentType(prefsRepo *repository.UserAgentPrefsRepository, uid uint) (string, error) {
	if prefsRepo == nil {
		return "", fmt.Errorf("无法解析继承的 agent 类型：偏好仓库未配置")
	}
	prefs, err := prefsRepo.FindByUserID(uid)
	if err != nil {
		return "", fmt.Errorf("读取用户 agent 偏好失败: %w", err)
	}
	last := strings.TrimSpace(prefs.LastAgentType)
	if last == "" {
		return "", fmt.Errorf("任务配置为继承父 agent，但用户尚未使用过任何 agent")
	}
	return last, nil
}

// resolveOrchCwd 校验工作区归属并返回 (uid, workspaceID, cwd)。所有编排工具共用此解析逻辑。
func resolveOrchCwd(ctx context.Context, wsResolver WorkspaceResolver, workspaceID uint) (uint, uint, string, error) {
	uid, ok := userIDFrom(ctx)
	if !ok {
		return 0, 0, "", fmt.Errorf("未认证")
	}
	if wsResolver == nil {
		return 0, 0, "", fmt.Errorf("工作区解析未配置")
	}
	if workspaceID == 0 {
		return 0, 0, "", fmt.Errorf("workspace_id 必填")
	}
	ws, err := wsResolver.FindWorkspaceByID(workspaceID)
	if err != nil || ws == nil {
		return 0, 0, "", fmt.Errorf("工作区不存在: %d", workspaceID)
	}
	if ws.UserID != uid {
		return 0, 0, "", fmt.Errorf("工作区不存在: %d", workspaceID)
	}
	if strings.TrimSpace(ws.Cwd) == "" {
		return 0, 0, "", fmt.Errorf("工作区未配置 cwd")
	}
	return uid, ws.ID, ws.Cwd, nil
}

// ====== create_orchestration_task ======

type createOrchTaskIn struct {
	Title       string   `json:"title" jsonschema:"任务标题"`
	Detail      string   `json:"detail" jsonschema:"任务详情，即发给 agent 的 prompt"`
	AgentType   string   `json:"agent_type,omitempty" jsonschema:"执行任务的 agent 类型，留空则继承用户最近使用的 agent"`
	ModelValue  string   `json:"model_value,omitempty" jsonschema:"模型值，留空则用 agent 默认"`
	Priority    string   `json:"priority,omitempty" jsonschema:"优先级，取值 p0、p1、p2，缺省为 p1"`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema:"依赖的其他任务 id 数组"`
	WorkspaceID uint     `json:"workspace_id,omitempty" jsonschema:"工作区 ID，留空则使用默认工作区"`
}

type createOrchTaskOut struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
}

// handleCreateOrchTask 在指定工作区的 tasks.json 中新增一个编排任务（status=pending）。
func handleCreateOrchTask(ctx context.Context, prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator, in createOrchTaskIn) (*mcp.CallToolResult, createOrchTaskOut, error) {
	uid, _, cwd, err := resolveOrchCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, createOrchTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, createOrchTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}

	title := strings.TrimSpace(in.Title)
	detail := strings.TrimSpace(in.Detail)
	if title == "" {
		return nil, createOrchTaskOut{}, fmt.Errorf("title 必填")
	}
	if detail == "" {
		return nil, createOrchTaskOut{}, fmt.Errorf("detail 必填")
	}

	// agent_type：显式指定优先，否则继承父 agent
	agentType, err := resolveAgentType(prefsRepo, uid, in.AgentType)
	if err != nil {
		return nil, createOrchTaskOut{}, err
	}

	// 生成简短唯一 id（与前端 OrchestrationTaskDialog 一致：t + base36）
	taskID := "t" + strconv.FormatInt(time.Now().UnixNano(), 36)

	task := models.OrchestrationTask{
		ID:         taskID,
		Title:      title,
		Detail:     detail,
		AgentType:  agentType,
		ModelValue: strings.TrimSpace(in.ModelValue),
		Priority:   models.NormalizeOrchTaskPriority(in.Priority),
		Status:     models.OrchTaskStatusPending,
		DependsOn:  in.DependsOn,
	}
	if err := orchCreator.UpsertTask(cwd, task); err != nil {
		return nil, createOrchTaskOut{}, fmt.Errorf("创建编排任务失败: %w", err)
	}

	return nil, createOrchTaskOut{TaskID: taskID, Title: title}, nil
}

// ====== update_orchestration_task ======

type updateOrchTaskIn struct {
	TaskID      string   `json:"task_id" jsonschema:"要更新的任务 id"`
	Title       string   `json:"title,omitempty" jsonschema:"新标题，留空则不修改"`
	Detail      string   `json:"detail,omitempty" jsonschema:"新任务详情(prompt)，留空则不修改"`
	AgentType   string   `json:"agent_type,omitempty" jsonschema:"新 agent 类型，留空则不修改"`
	ModelValue  string   `json:"model_value,omitempty" jsonschema:"新模型值，留空则不修改"`
	Priority    string   `json:"priority,omitempty" jsonschema:"新优先级，取值 p0、p1、p2，留空则不修改"`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema:"新依赖任务 id 数组"`
	WorkspaceID uint     `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type updateOrchTaskOut struct {
	TaskID  string `json:"task_id"`
	Updated bool   `json:"updated"`
}

func handleUpdateOrchTask(ctx context.Context, prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator, in updateOrchTaskIn) (*mcp.CallToolResult, updateOrchTaskOut, error) {
	uid, _, cwd, err := resolveOrchCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, updateOrchTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, updateOrchTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return nil, updateOrchTaskOut{}, fmt.Errorf("task_id 必填")
	}

	// 先加载现有任务，保留运行时字段，仅覆盖传入的非空字段
	def, err := orchCreator.Load(cwd)
	if err != nil {
		return nil, updateOrchTaskOut{}, fmt.Errorf("读取 tasks.json: %w", err)
	}
	var found *models.OrchestrationTask
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			found = &def.Tasks[i]
			break
		}
	}
	if found == nil {
		return nil, updateOrchTaskOut{}, fmt.Errorf("任务不存在: %s", taskID)
	}
	t := *found
	if strings.TrimSpace(in.Title) != "" {
		t.Title = strings.TrimSpace(in.Title)
	}
	if strings.TrimSpace(in.Detail) != "" {
		t.Detail = in.Detail
	}
	if strings.TrimSpace(in.AgentType) != "" {
		// 校验 agent 类型存在性（继承解析会校验）
		if _, rerr := resolveAgentType(prefsRepo, uid, in.AgentType); rerr != nil {
			return nil, updateOrchTaskOut{}, rerr
		}
		t.AgentType = strings.TrimSpace(in.AgentType)
	}
	if in.ModelValue != "" {
		t.ModelValue = strings.TrimSpace(in.ModelValue)
	}
	if strings.TrimSpace(in.Priority) != "" {
		t.Priority = models.NormalizeOrchTaskPriority(in.Priority)
	}
	if in.DependsOn != nil {
		t.DependsOn = in.DependsOn
	}
	// UpsertTask 会保留运行时字段（session/状态/时间戳/worktree）
	if err := orchCreator.UpsertTask(cwd, t); err != nil {
		return nil, updateOrchTaskOut{}, fmt.Errorf("更新编排任务失败: %w", err)
	}
	return nil, updateOrchTaskOut{TaskID: taskID, Updated: true}, nil
}

// ====== delete_orchestration_task ======

type deleteOrchTaskIn struct {
	TaskID      string `json:"task_id" jsonschema:"要删除的任务 id"`
	WorkspaceID uint   `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type deleteOrchTaskOut struct {
	TaskID  string `json:"task_id"`
	Deleted bool   `json:"deleted"`
}

func handleDeleteOrchTask(ctx context.Context, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator, in deleteOrchTaskIn) (*mcp.CallToolResult, deleteOrchTaskOut, error) {
	_, _, cwd, err := resolveOrchCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, deleteOrchTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, deleteOrchTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return nil, deleteOrchTaskOut{}, fmt.Errorf("task_id 必填")
	}
	if err := orchCreator.DeleteTask(cwd, taskID); err != nil {
		return nil, deleteOrchTaskOut{}, fmt.Errorf("删除编排任务失败: %w", err)
	}
	return nil, deleteOrchTaskOut{TaskID: taskID, Deleted: true}, nil
}

// ====== start_orchestration_task ======

type startOrchTaskIn struct {
	TaskID      string `json:"task_id,omitempty" jsonschema:"要启动的任务 id，留空则启动全部待执行任务"`
	WorkspaceID uint   `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type startOrchTaskOut struct {
	Started bool   `json:"started"`
	TaskID  string `json:"task_id,omitempty"`
}

func handleStartOrchTask(ctx context.Context, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator, in startOrchTaskIn) (*mcp.CallToolResult, startOrchTaskOut, error) {
	uid, wsID, cwd, err := resolveOrchCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, startOrchTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, startOrchTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if err := orchCreator.Start(ctx, cwd, wsID, uid, taskID); err != nil {
		return nil, startOrchTaskOut{}, fmt.Errorf("启动编排任务失败: %w", err)
	}
	return nil, startOrchTaskOut{Started: true, TaskID: taskID}, nil
}

// ====== stop_orchestration_task ======

type stopOrchTaskIn struct {
	TaskID      string `json:"task_id,omitempty" jsonschema:"要停止的任务 id，留空则停止全部运行中任务"`
	WorkspaceID uint   `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type stopOrchTaskOut struct {
	Stopped bool   `json:"stopped"`
	TaskID  string `json:"task_id,omitempty"`
}

func handleStopOrchTask(ctx context.Context, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator, in stopOrchTaskIn) (*mcp.CallToolResult, stopOrchTaskOut, error) {
	_, _, cwd, err := resolveOrchCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, stopOrchTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, stopOrchTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if err := orchCreator.Stop(cwd, taskID); err != nil {
		return nil, stopOrchTaskOut{}, fmt.Errorf("停止编排任务失败: %w", err)
	}
	return nil, stopOrchTaskOut{Stopped: true, TaskID: taskID}, nil
}

// ====== set_orchestration_max_parallel ======

type setOrchMaxParallelIn struct {
	MaxParallel int  `json:"max_parallel" jsonschema:"并发上限，范围 1~16，值为 1 时串行执行"`
	WorkspaceID uint `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type setOrchMaxParallelOut struct {
	MaxParallel int `json:"max_parallel"`
}

func handleSetOrchMaxParallel(ctx context.Context, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator, in setOrchMaxParallelIn) (*mcp.CallToolResult, setOrchMaxParallelOut, error) {
	_, _, cwd, err := resolveOrchCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, setOrchMaxParallelOut{}, err
	}
	if orchCreator == nil {
		return nil, setOrchMaxParallelOut{}, fmt.Errorf("编排任务创建未配置")
	}
	if in.MaxParallel < 1 || in.MaxParallel > 16 {
		return nil, setOrchMaxParallelOut{}, fmt.Errorf("max_parallel 范围 1~16")
	}
	if err := orchCreator.SetMaxParallel(cwd, in.MaxParallel); err != nil {
		return nil, setOrchMaxParallelOut{}, fmt.Errorf("设置并发上限失败: %w", err)
	}
	return nil, setOrchMaxParallelOut{MaxParallel: in.MaxParallel}, nil
}

// ====== list_orchestration_tasks ======

type listOrchTasksIn struct {
	WorkspaceID uint `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

// orchTaskSummary 是返回给 agent 的任务摘要（精简字段，避免泄露内部细节）。
type orchTaskSummary struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Priority   string   `json:"priority,omitempty"`
	AgentType  string   `json:"agent_type"`
	ModelValue string   `json:"model_value,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type listOrchTasksOut struct {
	MaxParallel int               `json:"max_parallel"`
	Tasks       []orchTaskSummary `json:"tasks"`
}

func handleListOrchTasks(ctx context.Context, wsResolver WorkspaceResolver, orchCreator OrchestratorTaskCreator, in listOrchTasksIn) (*mcp.CallToolResult, listOrchTasksOut, error) {
	_, _, cwd, err := resolveOrchCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, listOrchTasksOut{}, err
	}
	if orchCreator == nil {
		return nil, listOrchTasksOut{}, fmt.Errorf("编排任务创建未配置")
	}
	def, err := orchCreator.Load(cwd)
	if err != nil {
		return nil, listOrchTasksOut{}, fmt.Errorf("读取 tasks.json: %w", err)
	}
	tasks := make([]orchTaskSummary, 0, len(def.Tasks))
	for _, t := range def.Tasks {
		tasks = append(tasks, orchTaskSummary{
			ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority, AgentType: t.AgentType,
			ModelValue: t.ModelValue, Branch: t.Branch, Cwd: t.WorktreePath,
			DependsOn: t.DependsOn, Error: t.Error,
		})
	}
	return nil, listOrchTasksOut{MaxParallel: def.MaxParallel, Tasks: tasks}, nil
}

