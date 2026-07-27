// Package taskmanagermcp 提供 opennexus-task MCP server，让主 agent 通过 MCP 工具
// 管理工作区下的任务管理（tasks.json）：新增/更新/删除任务、启停任务、调整并发上限、列出现状。
//
// 编排任务持久化于工作区管理数据目录中的 tasks.json（与 agent 工作目录分离），
// 由调度器读取并基于 git worktree 隔离执行每个任务。
// 本 server 从原 opennexus-subagent 抽离而来，作为独立 MCP server 对外暴露。
//
// 暴露 8 个工具：
//   - create_task：新增编排任务
//   - update_task：更新编排任务可编辑字段
//   - delete_task：删除编排任务
//   - start_task： 启动编排任务
//   - stop_task：  停止编排任务
//   - send_prompt：向任务已有会话发送新 prompt 继续对话
//   - set_max_parallel：设置并发上限
//   - list_tasks： 列出编排任务现状
//
// 鉴权复用 opennexus-notes 的 Bearer token 体系（用户级共享一个 token）。
package taskmanagermcp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opennexus/internal/acp"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// WorkspaceResolver 解析工作区并校验归属（取 cwd 用于编排任务）。
// 由 *agent.Router 实现（复用 FindWorkspaceByID）。
type WorkspaceResolver interface {
	FindWorkspaceByID(id uint) (*models.Workspace, error)
}

// TaskManagerTaskCreator 由 *services.TaskManagerService 实现，
// 用于通过 MCP 工具管理编排任务（创建/更新/删除/启停/调整并发）。
type TaskManagerTaskCreator interface {
	UpsertTask(cwd string, task models.TaskManagerTask) error
	DeleteTask(cwd, taskID string) error
	SetMaxParallel(cwd string, maxParallel int) error
	Stop(cwd, taskID string) error
	Start(ctx context.Context, cwd string, workspaceID uint, userID uint, taskID string) error
	Load(cwd string) (*models.TaskManagerDef, error)
	// SendPrompt 向任务已有会话追加发送新 prompt，继续对话。
	SendPrompt(ctx context.Context, cwd, taskID, prompt string) error
}

// Handler 返回带 Bearer 鉴权的 taskmanager MCP Streamable HTTP Handler。
//
// prefsRepo 用于解析"继承父 agent"：任务不指定 agent 后端时取用户最近使用的 agent 类型。
// wsResolver / orchCreator 用于编排工具（可传 nil 禁用）。
func Handler(settings *repository.NoteSettingsRepository, prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator) http.Handler {
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

func newServer(prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "opennexus-task", Version: "1.0.0"}, nil)

	// addToolSafe 注册单个工具，并 recover mcp.AddTool 的 panic。
	//
	// go-sdk 的 mcp.AddTool 在输入结构体 jsonschema tag 非法时会 panic（例如
	// tag 形如 "x=1" 命中 "tag must not begin with 'WORD='" 规则）。若不兜底，
	// 该 panic 会沿 StreamableHTTPHandler 冒泡成 HTTP 500，导致 MCP 客户端判定
	// 整个 opennexus-task server 连接失败、全部工具不可见。
	// 这里按工具粒度 recover：仅跳过出问题的那一个工具，server 仍可正常列出其余工具。
	addTool := func(name string, register func()) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("taskmanager MCP: 跳过工具 %s（注册失败）: %v", name, r)
			}
		}()
		register()
	}

	addTool("create_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "create_task",
			Description: "在当前工作区的任务管理（tasks.json）中新增一个编排任务。任务默认 status=pending、priority=p1，可由编排调度器启动（基于 git worktree 隔离执行）。这是管理编排任务的首选方式（结构化、自带校验），优先于手写 tasks.json。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in createTaskIn) (*mcp.CallToolResult, createTaskOut, error) {
			return handleCreateTask(ctx, prefsRepo, wsResolver, orchCreator, in)
		})
	})

	addTool("update_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "update_task",
			Description: "更新编排任务的可编辑字段（title/detail/agent_type/model_value/priority/depends_on）。按 task_id 匹配；运行时字段（status/session_id/worktree 等）保持不变。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateTaskIn) (*mcp.CallToolResult, updateTaskOut, error) {
			return handleUpdateTask(ctx, prefsRepo, wsResolver, orchCreator, in)
		})
	})

	addTool("delete_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "delete_task",
			Description: "按 task_id 删除编排任务。若任务正在运行会先停止并清理其 worktree。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteTaskIn) (*mcp.CallToolResult, deleteTaskOut, error) {
			return handleDeleteTask(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("start_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "start_task",
			Description: "启动编排任务。task_id 留空则启动全部待执行（pending/failed/canceled/interrupt）任务，否则仅启动指定任务。任务在其专属 git worktree 内执行。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in startTaskIn) (*mcp.CallToolResult, startTaskOut, error) {
			return handleStartTask(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("stop_task", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "stop_task",
			Description: "停止编排任务。task_id 留空则停止全部运行中/排队中任务，否则仅停止指定任务。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopTaskIn) (*mcp.CallToolResult, stopTaskOut, error) {
			return handleStopTask(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("send_prompt", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "send_prompt",
			Description: "向指定编排任务的已有会话发送一条新 prompt，在原上下文中继续对话（如追加需求、补充修改意见）。任务必须已执行过且当前不在运行中；发送后任务转为 running，agent 在其专属 worktree 内继续处理，完成后转 done，可用 list_tasks 跟踪进度。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in sendPromptIn) (*mcp.CallToolResult, sendPromptOut, error) {
			return handleSendPrompt(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("set_max_parallel", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "set_max_parallel",
			Description: "设置编排并发上限 max_parallel（范围为 1~16，值为 1 时串行执行）。影响后续任务的并发调度。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in setMaxParallelIn) (*mcp.CallToolResult, setMaxParallelOut, error) {
			return handleSetMaxParallel(ctx, wsResolver, orchCreator, in)
		})
	})

	addTool("list_tasks", func() {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "list_tasks",
			Description: "列出当前工作区编排的所有任务（含 id/title/status/priority/agent_type/branch/cwd 等运行时状态），用于了解现状后再决定增删改或调度。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTasksIn) (*mcp.CallToolResult, listTasksOut, error) {
			return handleListTasks(ctx, wsResolver, orchCreator, in)
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

// resolveTaskCwd 校验工作区归属并返回 (uid, workspaceID, cwd)。所有编排工具共用此解析逻辑。
func resolveTaskCwd(ctx context.Context, wsResolver WorkspaceResolver, workspaceID uint) (uint, uint, string, error) {
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

// ====== create_task ======

type createTaskIn struct {
	Title       string   `json:"title" jsonschema:"任务标题"`
	Detail      string   `json:"detail" jsonschema:"任务详情，即发给 agent 的 prompt"`
	AgentType   string   `json:"agent_type,omitempty" jsonschema:"执行任务的 agent 类型，留空则继承用户最近使用的 agent"`
	ModelValue  string   `json:"model_value,omitempty" jsonschema:"模型值，留空则用 agent 默认"`
	Priority    string   `json:"priority,omitempty" jsonschema:"优先级，取值 p0、p1、p2，缺省为 p1"`
	Branch      string   `json:"branch,omitempty" jsonschema:"任务 worktree 分支名，建议 feat/ 或 fix/ 开头的英文 kebab-case；留空则启动时由 AI 自动生成"`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema:"依赖的其他任务 id 数组"`
	WorkspaceID uint     `json:"workspace_id,omitempty" jsonschema:"工作区 ID，留空则使用默认工作区"`
}

type createTaskOut struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
}

// handleCreateTask 在指定工作区的管理数据目录 tasks.json 中新增一个编排任务（status=pending）。
func handleCreateTask(ctx context.Context, prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in createTaskIn) (*mcp.CallToolResult, createTaskOut, error) {
	uid, _, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, createTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, createTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}

	title := strings.TrimSpace(in.Title)
	detail := strings.TrimSpace(in.Detail)
	if title == "" {
		return nil, createTaskOut{}, fmt.Errorf("title 必填")
	}
	if detail == "" {
		return nil, createTaskOut{}, fmt.Errorf("detail 必填")
	}

	// agent_type：显式指定优先，否则继承父 agent
	agentType, err := resolveAgentType(prefsRepo, uid, in.AgentType)
	if err != nil {
		return nil, createTaskOut{}, err
	}

	// 生成简短唯一 id（与前端 TaskManagerTaskDialog 一致：t + base36）
	taskID := "t" + strconv.FormatInt(time.Now().UnixNano(), 36)

	task := models.TaskManagerTask{
		ID:         taskID,
		Title:      title,
		Detail:     detail,
		AgentType:  agentType,
		ModelValue: strings.TrimSpace(in.ModelValue),
		Priority:   models.NormalizeTaskPriority(in.Priority),
		Status:     models.TaskStatusPending,
		DependsOn:  in.DependsOn,
	}
	// 显式指定分支名时规范化为 feat//fix/ 前缀；留空则启动时由 AI 自动生成。
	if b := strings.TrimSpace(in.Branch); b != "" {
		task.Branch = acp.NormalizeBranchName(b)
	}
	if err := orchCreator.UpsertTask(cwd, task); err != nil {
		return nil, createTaskOut{}, fmt.Errorf("创建编排任务失败: %w", err)
	}

	return nil, createTaskOut{TaskID: taskID, Title: title}, nil
}

// ====== update_task ======

type updateTaskIn struct {
	TaskID      string   `json:"task_id" jsonschema:"要更新的任务 id"`
	Title       string   `json:"title,omitempty" jsonschema:"新标题，留空则不修改"`
	Detail      string   `json:"detail,omitempty" jsonschema:"新任务详情(prompt)，留空则不修改"`
	AgentType   string   `json:"agent_type,omitempty" jsonschema:"新 agent 类型，留空则不修改"`
	ModelValue  string   `json:"model_value,omitempty" jsonschema:"新模型值，留空则不修改"`
	Priority    string   `json:"priority,omitempty" jsonschema:"新优先级，取值 p0、p1、p2，留空则不修改"`
	DependsOn   []string `json:"depends_on,omitempty" jsonschema:"新依赖任务 id 数组"`
	WorkspaceID uint     `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type updateTaskOut struct {
	TaskID  string `json:"task_id"`
	Updated bool   `json:"updated"`
}

func handleUpdateTask(ctx context.Context, prefsRepo *repository.UserAgentPrefsRepository, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in updateTaskIn) (*mcp.CallToolResult, updateTaskOut, error) {
	uid, _, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, updateTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, updateTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return nil, updateTaskOut{}, fmt.Errorf("task_id 必填")
	}

	// 先加载现有任务，保留运行时字段，仅覆盖传入的非空字段
	def, err := orchCreator.Load(cwd)
	if err != nil {
		return nil, updateTaskOut{}, fmt.Errorf("读取 tasks.json: %w", err)
	}
	var found *models.TaskManagerTask
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			found = &def.Tasks[i]
			break
		}
	}
	if found == nil {
		return nil, updateTaskOut{}, fmt.Errorf("任务不存在: %s", taskID)
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
			return nil, updateTaskOut{}, rerr
		}
		t.AgentType = strings.TrimSpace(in.AgentType)
	}
	if in.ModelValue != "" {
		t.ModelValue = strings.TrimSpace(in.ModelValue)
	}
	if strings.TrimSpace(in.Priority) != "" {
		t.Priority = models.NormalizeTaskPriority(in.Priority)
	}
	if in.DependsOn != nil {
		t.DependsOn = in.DependsOn
	}
	// UpsertTask 会保留运行时字段（session/状态/时间戳/worktree）
	if err := orchCreator.UpsertTask(cwd, t); err != nil {
		return nil, updateTaskOut{}, fmt.Errorf("更新编排任务失败: %w", err)
	}
	return nil, updateTaskOut{TaskID: taskID, Updated: true}, nil
}

// ====== delete_task ======

type deleteTaskIn struct {
	TaskID      string `json:"task_id" jsonschema:"要删除的任务 id"`
	WorkspaceID uint   `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type deleteTaskOut struct {
	TaskID  string `json:"task_id"`
	Deleted bool   `json:"deleted"`
}

func handleDeleteTask(ctx context.Context, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in deleteTaskIn) (*mcp.CallToolResult, deleteTaskOut, error) {
	_, _, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, deleteTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, deleteTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return nil, deleteTaskOut{}, fmt.Errorf("task_id 必填")
	}
	if err := orchCreator.DeleteTask(cwd, taskID); err != nil {
		return nil, deleteTaskOut{}, fmt.Errorf("删除编排任务失败: %w", err)
	}
	return nil, deleteTaskOut{TaskID: taskID, Deleted: true}, nil
}

// ====== start_task ======

type startTaskIn struct {
	TaskID      string `json:"task_id,omitempty" jsonschema:"要启动的任务 id，留空则启动全部待执行任务"`
	WorkspaceID uint   `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type startTaskOut struct {
	Started bool   `json:"started"`
	TaskID  string `json:"task_id,omitempty"`
}

func handleStartTask(ctx context.Context, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in startTaskIn) (*mcp.CallToolResult, startTaskOut, error) {
	uid, wsID, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, startTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, startTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if err := orchCreator.Start(ctx, cwd, wsID, uid, taskID); err != nil {
		return nil, startTaskOut{}, fmt.Errorf("启动编排任务失败: %w", err)
	}
	return nil, startTaskOut{Started: true, TaskID: taskID}, nil
}

// ====== stop_task ======

type stopTaskIn struct {
	TaskID      string `json:"task_id,omitempty" jsonschema:"要停止的任务 id，留空则停止全部运行中任务"`
	WorkspaceID uint   `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type stopTaskOut struct {
	Stopped bool   `json:"stopped"`
	TaskID  string `json:"task_id,omitempty"`
}

func handleStopTask(ctx context.Context, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in stopTaskIn) (*mcp.CallToolResult, stopTaskOut, error) {
	_, _, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, stopTaskOut{}, err
	}
	if orchCreator == nil {
		return nil, stopTaskOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if err := orchCreator.Stop(cwd, taskID); err != nil {
		return nil, stopTaskOut{}, fmt.Errorf("停止编排任务失败: %w", err)
	}
	return nil, stopTaskOut{Stopped: true, TaskID: taskID}, nil
}

// ====== send_prompt ======

type sendPromptIn struct {
	TaskID      string `json:"task_id" jsonschema:"要继续对话的任务 id"`
	Prompt      string `json:"prompt" jsonschema:"发送给任务会话的新 prompt"`
	WorkspaceID uint   `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type sendPromptOut struct {
	TaskID string `json:"task_id"`
	Sent   bool   `json:"sent"`
}

// handleSendPrompt 向任务已有会话追加发送 prompt，继续对话。
func handleSendPrompt(ctx context.Context, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in sendPromptIn) (*mcp.CallToolResult, sendPromptOut, error) {
	_, _, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, sendPromptOut{}, err
	}
	if orchCreator == nil {
		return nil, sendPromptOut{}, fmt.Errorf("编排任务创建未配置")
	}
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return nil, sendPromptOut{}, fmt.Errorf("task_id 必填")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return nil, sendPromptOut{}, fmt.Errorf("prompt 必填")
	}
	if err := orchCreator.SendPrompt(ctx, cwd, taskID, in.Prompt); err != nil {
		return nil, sendPromptOut{}, fmt.Errorf("继续对话失败: %w", err)
	}
	return nil, sendPromptOut{TaskID: taskID, Sent: true}, nil
}

// ====== set_max_parallel ======

type setMaxParallelIn struct {
	MaxParallel int  `json:"max_parallel" jsonschema:"并发上限，范围 1~16，值为 1 时串行执行"`
	WorkspaceID uint `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

type setMaxParallelOut struct {
	MaxParallel int `json:"max_parallel"`
}

func handleSetMaxParallel(ctx context.Context, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in setMaxParallelIn) (*mcp.CallToolResult, setMaxParallelOut, error) {
	_, _, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, setMaxParallelOut{}, err
	}
	if orchCreator == nil {
		return nil, setMaxParallelOut{}, fmt.Errorf("编排任务创建未配置")
	}
	if in.MaxParallel < 1 || in.MaxParallel > 16 {
		return nil, setMaxParallelOut{}, fmt.Errorf("max_parallel 范围 1~16")
	}
	if err := orchCreator.SetMaxParallel(cwd, in.MaxParallel); err != nil {
		return nil, setMaxParallelOut{}, fmt.Errorf("设置并发上限失败: %w", err)
	}
	return nil, setMaxParallelOut{MaxParallel: in.MaxParallel}, nil
}

// ====== list_tasks ======

type listTasksIn struct {
	WorkspaceID uint `json:"workspace_id,omitempty" jsonschema:"工作区 ID"`
}

// taskSummary 是返回给 agent 的任务摘要（精简字段，避免泄露内部细节）。
type taskSummary struct {
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

type listTasksOut struct {
	MaxParallel int               `json:"max_parallel"`
	Tasks       []taskSummary `json:"tasks"`
}

func handleListTasks(ctx context.Context, wsResolver WorkspaceResolver, orchCreator TaskManagerTaskCreator, in listTasksIn) (*mcp.CallToolResult, listTasksOut, error) {
	_, _, cwd, err := resolveTaskCwd(ctx, wsResolver, in.WorkspaceID)
	if err != nil {
		return nil, listTasksOut{}, err
	}
	if orchCreator == nil {
		return nil, listTasksOut{}, fmt.Errorf("编排任务创建未配置")
	}
	def, err := orchCreator.Load(cwd)
	if err != nil {
		return nil, listTasksOut{}, fmt.Errorf("读取 tasks.json: %w", err)
	}
	tasks := make([]taskSummary, 0, len(def.Tasks))
	for _, t := range def.Tasks {
		tasks = append(tasks, taskSummary{
			ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority, AgentType: t.AgentType,
			ModelValue: t.ModelValue, Branch: t.Branch, Cwd: t.WorktreePath,
			DependsOn: t.DependsOn, Error: t.Error,
		})
	}
	return nil, listTasksOut{MaxParallel: def.MaxParallel, Tasks: tasks}, nil
}
