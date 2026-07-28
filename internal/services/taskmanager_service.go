package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opennexus/internal/acp"
	"opennexus/internal/models"
)

// TaskManagerExecutor 是编排器执行任务所需的 agent 能力子集（*agent.Router 实现该接口）。
type TaskManagerExecutor interface {
	RunSessionTask(ctx context.Context, cfg acp.SessionTaskConfig) (acp.SessionTaskResult, error)
	FindWorkspaceByID(id uint) (*models.Workspace, error)
	// GetSessionByDBID 按 DB 主键查会话，用于继承父编排会话的 agent 类型。
	GetSessionByDBID(id uint) (*models.Session, error)
	// DefaultAgentType 返回首个已注册 agent 类型，作为任务未指定 agent 时的最终回退。
	DefaultAgentType() string
	// Prompt 向已有会话发送 prompt（自动恢复断开的连接），用于任务的继续对话。
	Prompt(ctx context.Context, sessionID, prompt string) (<-chan models.Message, error)
	// RunPromptOnce 在临时 ACP 会话发送一次 prompt 并收集文本响应（不落库），用于 AI 生成分支名。
	RunPromptOnce(ctx context.Context, agentType, modelValue, prompt string) (string, error)
}

// TaskManagerService 管理任务管理：读写工作区管理数据目录中的 tasks.json、按并发上限调度任务、
// 基于 git worktree 隔离每个任务的工作目录，并复用 RunSessionTask 创建持久会话执行。
type TaskManagerService struct {
	exec TaskManagerExecutor

	mu       sync.Mutex                    // 保护运行态
	stores   map[string]*TaskStore         // cwd -> 文件 store
	storesMu sync.Mutex                    // 保护 stores
	runs     map[string]*orchRun           // cwd -> 运行态（含信号量、cancel）
	taskCtx  map[string]context.CancelFunc // cwd:taskID -> 取消函数
}

type orchRun struct {
	cwd         string
	maxParallel int
	sem         chan struct{}  // 并发槽位
	wg          sync.WaitGroup // 等待所有任务结束
}

// NewTaskManagerService 创建编排服务。
func NewTaskManagerService(exec TaskManagerExecutor) *TaskManagerService {
	return &TaskManagerService{
		exec:    exec,
		stores:  make(map[string]*TaskStore),
		runs:    make(map[string]*orchRun),
		taskCtx: make(map[string]context.CancelFunc),
	}
}

// storeFor 返回指定 cwd 的 TaskStore，按 cwd 缓存避免同一目录多个锁。
func (s *TaskManagerService) storeFor(cwd string) *TaskStore {
	s.storesMu.Lock()
	defer s.storesMu.Unlock()
	if s.stores[cwd] == nil {
		s.stores[cwd] = NewTaskStore(cwd)
	}
	return s.stores[cwd]
}

// ErrNotGitRepo 表示编排 cwd 不是 git 仓库，需先初始化。
var ErrNotGitRepo = errors.New("当前工作目录不是 git 仓库，请先初始化")

// IsGitRepo 报告 cwd 是否为 git 仓库（编排任务需在 git 仓库内运行，以便隔离 worktree）。
func (s *TaskManagerService) IsGitRepo(cwd string) bool {
	if cwd == "" {
		return false
	}
	if _, err := acp.GitRoot(cwd); err == nil {
		return true
	}
	return acp.IsGitRepo(cwd)
}

// InitGitRepo 在 cwd 初始化 git 仓库（含初始提交），并确保 .worktrees 目录存在。
func (s *TaskManagerService) InitGitRepo(cwd string) error {
	if cwd == "" {
		return fmt.Errorf("cwd 不能为空")
	}
	if err := acp.GitInit(cwd); err != nil {
		return fmt.Errorf("初始化 git 仓库: %w", err)
	}
	repoRoot := cwd
	if root, err := acp.GitRoot(cwd); err == nil {
		repoRoot = root
	}
	if err := acp.EnsureWorktreesDir(repoRoot); err != nil {
		return fmt.Errorf("创建 worktrees 目录: %w", err)
	}
	return nil
}

// Load 读取 cwd 对应管理数据目录下的 tasks.json。文件不存在时返回空定义（max_parallel 取默认值）。
func (s *TaskManagerService) Load(cwd string) (*models.TaskManagerDef, error) {
	if cwd == "" {
		return nil, fmt.Errorf("cwd 不能为空")
	}
	return s.storeFor(cwd).Load()
}

// Save 将编排定义写回 cwd 对应管理数据目录下的 tasks.json（原子写）。
func (s *TaskManagerService) Save(cwd string, def *models.TaskManagerDef) error {
	if cwd == "" {
		return fmt.Errorf("cwd 不能为空")
	}
	return s.storeFor(cwd).Save(def)
}

// UpsertTask 新增或更新（按 id 匹配）单个任务，并写回文件。
func (s *TaskManagerService) UpsertTask(cwd string, task models.TaskManagerTask) error {
	return s.storeFor(cwd).UpsertTask(task)
}

// DeleteTask 删除指定任务。若任务正在运行则先取消，并尝试清理其 worktree。
func (s *TaskManagerService) DeleteTask(cwd, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed, err := s.deleteTaskLocked(cwd, func(t *models.TaskManagerTask) bool { return t.ID == taskID })
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("任务 %s 不存在", taskID)
	}
	return nil
}

// UnregisterSessionTask 注销会话登记的任务：删除 tasks.json 中 db_session_id 匹配的条目。
// 与 RegisterSessionTask 对称，删除会话时调用，避免会话删除后登记的任务残留
// 在任务列表中。未登记过的会话（无匹配条目）视为成功，直接返回 nil。
func (s *TaskManagerService) UnregisterSessionTask(cwd string, dbSessionID uint) error {
	if cwd == "" || dbSessionID == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.deleteTaskLocked(cwd, func(t *models.TaskManagerTask) bool {
		return t.DBSessionID != nil && *t.DBSessionID == dbSessionID
	})
	return err
}

// deleteTaskLocked 删除首个匹配的任务（需持有 s.mu）：取消运行、清理 worktree 并写回。
// 返回是否删除了条目。
func (s *TaskManagerService) deleteTaskLocked(cwd string, match func(*models.TaskManagerTask) bool) (bool, error) {
	def, err := s.storeFor(cwd).Load()
	if err != nil {
		return false, err
	}
	idx := -1
	var taskID, wtPath, branch string
	for i := range def.Tasks {
		if match(&def.Tasks[i]) {
			idx = i
			taskID = def.Tasks[i].ID
			wtPath = def.Tasks[i].WorktreePath
			branch = def.Tasks[i].Branch
			break
		}
	}
	if idx < 0 {
		return false, nil
	}
	// 取消运行中的任务
	s.cancelLocked(cwd, taskID)
	// 清理 worktree
	if wtPath != "" {
		if rerr := acp.RemoveWorktree(cwd, wtPath, branch); rerr != nil {
			slog.Warn("删除任务时清理 worktree 失败", "task", taskID, "err", rerr)
		}
	}
	def.Tasks = append(def.Tasks[:idx], def.Tasks[idx+1:]...)
	if err := s.storeFor(cwd).Save(def); err != nil {
		return false, err
	}
	return true, nil
}

// SetMaxParallel 更新并发上限。若当前有运行态且新值更小，已启动的任务不受影响，
// 新任务按新上限排队。
func (s *TaskManagerService) SetMaxParallel(cwd string, maxParallel int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxParallel <= 0 {
		maxParallel = 1
	}
	if r, ok := s.runs[cwd]; ok {
		// 重建信号量（仅影响尚未获取槽位的等待者）
		newSem := make(chan struct{}, maxParallel)
		r.sem = newSem
		r.maxParallel = maxParallel
	}
	return s.storeFor(cwd).SetMaxParallel(maxParallel)
}

// Start 启动任务。taskID 为空时启动全部 pending/failed/canceled/interrupt 任务，
// 否则仅启动指定任务。已在运行的不会重复启动。
func (s *TaskManagerService) Start(ctx context.Context, cwd string, workspaceID uint, userID uint, taskID string) error {
	// 编排任务基于 git worktree 隔离，要求 cwd 为 git 仓库；非仓库时自动初始化（含初始提交），
	// 免去用户手动 git init 的步骤。
	if !s.IsGitRepo(cwd) {
		if err := s.InitGitRepo(cwd); err != nil {
			return err
		}
	}
	store := s.storeFor(cwd)
	def, err := store.Load()
	if err != nil {
		return err
	}
	maxParallel := def.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 1
	}

	s.mu.Lock()
	run := s.runs[cwd]
	if run == nil {
		run = &orchRun{cwd: cwd, maxParallel: maxParallel, sem: make(chan struct{}, maxParallel)}
		s.runs[cwd] = run
	}
	s.mu.Unlock()

	// 收集待启动任务
	var targets []models.TaskManagerTask
	for i := range def.Tasks {
		t := &def.Tasks[i]
		if taskID != "" && t.ID != taskID {
			continue
		}
		if models.IsTaskRunning(t.Status) {
			// 仅跳过内存中确实在跑的任务；服务重启后 tasks.json 残留的
			// running/queued 无 taskCtx，必须允许重新排队，否则「全部启动」会空转。
			taskKey := cwd + ":" + t.ID
			s.mu.Lock()
			_, live := s.taskCtx[taskKey]
			s.mu.Unlock()
			if live {
				continue
			}
		}
		if t.Status == models.TaskStatusDone && taskID == "" {
			continue // 全部启动时跳过已完成；显式单任务启动可重跑
		}
		// 重置为 queued
		t.Status = models.TaskStatusQueued
		t.Error = ""
		targets = append(targets, *t)
	}
	if len(targets) == 0 {
		return nil
	}
	// 高优先级先抢并发槽位（p0 > p1 > p2）
	sort.SliceStable(targets, func(i, j int) bool {
		return models.TaskPriorityRank(targets[i].Priority) < models.TaskPriorityRank(targets[j].Priority)
	})
	// 持久化 queued 状态
	if err := store.Save(def); err != nil {
		return fmt.Errorf("写入排队状态: %w", err)
	}

	for _, t := range targets {
		t := t
		run.wg.Add(1)
		go s.runTask(run, &t, workspaceID, userID)
	}
	return nil
}

// runTask 执行单个任务：获取槽位 → 创建 worktree → 运行会话 → 更新状态。
func (s *TaskManagerService) runTask(run *orchRun, t *models.TaskManagerTask, workspaceID, userID uint) {
	defer run.wg.Done()

	// 创建任务级 ctx，便于 Stop 取消
	taskKey := run.cwd + ":" + t.ID
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.taskCtx[taskKey] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.taskCtx, taskKey)
		s.mu.Unlock()
	}()

	// 获取并发槽位（可被取消打断）。获取槽位前保持 queued 状态，避免排队等待的任务
	// 也显示为“执行中”却并未真正发送——此前在获取槽位前就置为 running，导致 max_parallel
	// 之外的任务全部误显示为运行中。
	select {
	case run.sem <- struct{}{}:
		defer func() { <-run.sem }()
	case <-ctx.Done():
		s.markCanceled(run.cwd, t.ID, "用户停止（排队中）")
		return
	}

	// 已获得槽位，真正进入运行态
	now := time.Now()
	s.updateTask(run.cwd, t.ID, func(task *models.TaskManagerTask) {
		task.Status = models.TaskStatusRunning
		task.StartedAt = &now
		task.Error = ""
	})

	result, runErr := s.executeTask(ctx, run.cwd, t, workspaceID, userID)

	fin := time.Now()
	s.updateTask(run.cwd, t.ID, func(task *models.TaskManagerTask) {
		task.FinishedAt = &fin
		task.SessionID = result.SessionID
		if result.DBSessionID > 0 {
			dbID := result.DBSessionID
			task.DBSessionID = &dbID
		}
		if runErr != nil {
			task.Status = models.TaskStatusFailed
			task.Error = runErr.Error()
			return
		}
		if !result.Success {
			task.Status = models.TaskStatusFailed
			task.Error = result.Error
			return
		}
		task.Status = models.TaskStatusDone
	})
}

// taskBranchNamePrompt 是 AI 生成任务分支名的提示词模板。
const taskBranchNamePrompt = `请为以下开发任务生成一个简短的英文 git 分支名，要求以 feat/ 或 fix/ 开头（新功能用 feat/，缺陷修复用 fix/），斜杠后为 kebab-case 的 2-4 个英文单词，只用小写字母、数字和连字符，概括任务核心内容。
任务标题：{{title}}
任务详情：{{detail}}
仅输出分支名，不要输出其他任何内容。`

// generateTaskBranch 生成任务的 worktree 分支名：优先用任务的 agent 做一次性 AI 命名
//（规范化为 feat//fix/ 前缀），失败回退到标题/详情清洗，仍为空则兜底 task-<ID>。
func (s *TaskManagerService) generateTaskBranch(ctx context.Context, agentType string, t *models.TaskManagerTask) string {
	desc := strings.TrimSpace(t.Title)
	if desc == "" {
		desc = strings.TrimSpace(t.Detail)
	}
	if desc != "" && agentType != "" {
		aiCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		built := strings.ReplaceAll(taskBranchNamePrompt, "{{title}}", strings.TrimSpace(t.Title))
		built = strings.ReplaceAll(built, "{{detail}}", strings.TrimSpace(t.Detail))
		if resp, err := s.exec.RunPromptOnce(aiCtx, agentType, t.ModelValue, built); err == nil {
			if name := acp.NormalizeBranchName(resp); name != "" {
				return name
			}
		} else {
			slog.Warn("AI 生成任务分支名失败，回退规则提取", "task", t.ID, "agent", agentType, "err", err)
		}
		if name := acp.NormalizeBranchName(desc); name != "" {
			return name
		}
	}
	return "task-" + t.ID
}

// executeTask 创建 worktree 并调用 RunSessionTask 执行任务。
func (s *TaskManagerService) executeTask(ctx context.Context, cwd string, t *models.TaskManagerTask, workspaceID, userID uint) (acp.SessionTaskResult, error) {
	// 解析仓库根（worktree add 需在公共 git 仓库下执行）
	repoRoot := cwd
	if root, err := acp.GitRoot(cwd); err == nil {
		repoRoot = root
	}
	if err := acp.EnsureWorktreesDir(repoRoot); err != nil {
		return acp.SessionTaskResult{}, fmt.Errorf("创建 worktrees 目录: %w", err)
	}

	// 解析 agent 类型（提前到 worktree 创建前，AI 生成分支名也需要它）：
	// 任务未指定时回退到首个已注册 agent。直接把空 agent_type 传给 RunSessionTask
	// 会因 GetBackend 失败而报"agent 类型未注册"。
	agentType := t.AgentType
	if agentType == "" {
		agentType = s.exec.DefaultAgentType()
		// 回写解析结果，使 UI 显示实际使用的 agent，并让后续重跑保持一致。
		if agentType != "" {
			resolved := agentType
			s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
				task.AgentType = resolved
			})
		}
	}

	// 分支名：任务已指定则沿用（重跑场景保持不变）；否则 AI 生成 feat//fix/ 前缀分支名并去重。
	branch := t.Branch
	if branch == "" {
		branch = acp.UniqueWorktreeName(repoRoot, s.generateTaskBranch(ctx, agentType, t))
	}
	// worktree 目录跟随分支名（feat/xxx 形成嵌套目录）；重跑时复用已记录路径。
	wtPath := t.WorktreePath
	if wtPath == "" {
		wtPath = acp.WorktreePath(repoRoot, branch)
	}

	// 若 worktree 已存在（如上次中断），先清理重建
	if _, err := os.Stat(wtPath); err == nil {
		_ = acp.RemoveWorktree(repoRoot, wtPath, branch)
	}
	if err := acp.CreateWorktree(repoRoot, branch, wtPath, ""); err != nil {
		return acp.SessionTaskResult{}, fmt.Errorf("创建 worktree: %w", err)
	}

	// 记录 branch/worktreePath
	s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
		task.Branch = branch
		task.WorktreePath = wtPath
	})

	cfg := acp.SessionTaskConfig{
		AgentType:   agentType,
		ModelValue:  t.ModelValue,
		Prompt:      t.Detail,
		UserID:      userID,
		WorkspaceID: workspaceID,
		Source:      models.SessionSourceManual,
		// 任务在其专属 git worktree 内运行：用 worktree 路径覆盖工作区 cwd。
		Cwd: wtPath,
		// 会话落库后立即回写 db_session_id/session_id，使前端启动后能马上导航到该会话
		//（无需等 RunSessionTask 阻塞返回）。
		OnSessionCreated: func(dbID uint, sid string) {
			s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
				task.SessionID = sid
				if dbID > 0 {
					d := dbID
					task.DBSessionID = &d
				}
			})
		},
	}
	res, err := s.exec.RunSessionTask(ctx, cfg)

	// worktree 始终保留（无论成败）：便于用户查看改动、继续对话或提交。
	return res, err
}

// SendPrompt 向任务已有会话追加发送新 prompt，继续对话。
// 任务必须已执行过（存在 session_id）。发送成功后立即返回，任务状态转 running，
// 消息流在后台消费完毕后转 done；期间可被 Stop 取消（转 canceled）。
// 注意：发送与消费不依赖调用方 ctx（MCP 请求结束即取消），而是绑定任务级 runCtx。
func (s *TaskManagerService) SendPrompt(_ context.Context, cwd, taskID, prompt string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task_id 不能为空")
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("prompt 不能为空")
	}
	def, err := s.storeFor(cwd).Load()
	if err != nil {
		return err
	}
	var task *models.TaskManagerTask
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			task = &def.Tasks[i]
			break
		}
	}
	if task == nil {
		return fmt.Errorf("任务 %s 不存在", taskID)
	}
	if task.SessionID == "" {
		return fmt.Errorf("任务 %s 尚未执行过，没有可继续的会话，请先 start_task", taskID)
	}

	// 内存中确实在跑的任务不允许并发追加 prompt，避免同一会话交叉发送
	taskKey := cwd + ":" + taskID
	s.mu.Lock()
	if _, live := s.taskCtx[taskKey]; live {
		s.mu.Unlock()
		return fmt.Errorf("任务 %s 正在运行中，请先等待完成或 stop_task", taskID)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.taskCtx[taskKey] = cancel
	s.mu.Unlock()

	// 同步发送以便把会话不存在等错误立即反馈给调用方；消息流在后台消费。
	// 使用 runCtx 而非调用方 ctx：MCP 工具请求返回后其 ctx 即被取消，会误中断对话。
	ch, err := s.exec.Prompt(runCtx, task.SessionID, prompt)
	if err != nil {
		s.mu.Lock()
		delete(s.taskCtx, taskKey)
		s.mu.Unlock()
		cancel()
		return fmt.Errorf("发送 prompt 失败: %w", err)
	}

	now := time.Now()
	s.updateTask(cwd, taskID, func(t *models.TaskManagerTask) {
		t.Status = models.TaskStatusRunning
		t.StartedAt = &now
		t.FinishedAt = nil
		t.Error = ""
	})

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.taskCtx, taskKey)
			s.mu.Unlock()
		}()
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					// 消息流结束：仅在仍为运行态时置 done（Stop 会先置 canceled）
					fin := time.Now()
					s.updateTask(cwd, taskID, func(t *models.TaskManagerTask) {
						if models.IsTaskRunning(t.Status) {
							t.Status = models.TaskStatusDone
							t.FinishedAt = &fin
						}
					})
					return
				}
			case <-runCtx.Done():
				// 被 Stop 取消：状态已由 Stop 写为 canceled，这里只负责退出
				return
			}
		}
	}()
	return nil
}

// Stop 停止任务。taskID 为空时停止该 cwd 下全部运行中/排队中任务。
func (s *TaskManagerService) Stop(cwd, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.storeFor(cwd)
	def, err := store.Load()
	if err != nil {
		return err
	}
	for i := range def.Tasks {
		t := &def.Tasks[i]
		if taskID != "" && t.ID != taskID {
			continue
		}
		if !models.IsTaskRunning(t.Status) {
			continue
		}
		s.cancelLocked(cwd, t.ID)
		now := time.Now()
		t.Status = models.TaskStatusCanceled
		t.FinishedAt = &now
		t.Error = "用户手动停止"
	}
	return store.Save(def)
}

// cancelLocked 取消指定任务（必须在持有 s.mu 时调用）。
func (s *TaskManagerService) cancelLocked(cwd, taskID string) {
	key := cwd + ":" + taskID
	if cancel, ok := s.taskCtx[key]; ok {
		cancel()
		delete(s.taskCtx, key)
	}
}

// markCanceled 在 ctx 提前结束（如排队时被停止）时更新状态。
func (s *TaskManagerService) markCanceled(cwd, taskID, reason string) {
	now := time.Now()
	s.updateTask(cwd, taskID, func(task *models.TaskManagerTask) {
		if models.IsTaskRunning(task.Status) {
			task.Status = models.TaskStatusCanceled
			task.FinishedAt = &now
			task.Error = reason
		}
	})
}

// updateTask 修改指定任务后写回；未找到时 mutate 不执行。
func (s *TaskManagerService) updateTask(cwd, taskID string, mutate func(*models.TaskManagerTask)) {
	if err := s.storeFor(cwd).UpdateTaskStatus(taskID, mutate); err != nil {
		slog.Warn("updateTask 更新 tasks.json 失败", "cwd", cwd, "err", err)
	}
}

// RegisterSessionTask 把一个已存在的会话作为任务登记到 cwd 对应的 tasks.json。
// 用于将"新建对话"与 tasks.json 强关联：用户手动新建会话首次发送 prompt 时调用，
// 使所有任务/对话统一在 tasks.json 中可见，便于任务视图集中管理。
//
// 入参约束：
//   - cwd 为空或 sess 为空时直接返回 nil（无操作）。
//   - 仅 manual 会话登记；scheduled/classify 由各自引擎管理，不在此重复登记。
//   - 子会话（ParentSessionID 非 nil，由 MCP 工具创建）不登记，避免重复。
//
// 去重：按 db_session_id 检查，若 tasks.json 已存在相同 db_session_id 的任务则跳过，
// 避免重复发送导致重复条目。task.id 采用会话 DB 主键的字符串形式，
// 与自定义字符串 id 命名空间基本不冲突。
func (s *TaskManagerService) RegisterSessionTask(cwd string, sess *models.Session, prompt string) error {
	if cwd == "" || sess == nil {
		return nil
	}
	if sess.Source != models.SessionSourceManual {
		return nil
	}
	if sess.ParentSessionID != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := s.storeFor(cwd)
	def, err := store.Load()
	if err != nil {
		return fmt.Errorf("加载 tasks.json: %w", err)
	}
	for i := range def.Tasks {
		if existing := def.Tasks[i].DBSessionID; existing != nil && *existing == sess.ID {
			// 已登记：不新增条目，但本次又发起了新 prompt，非运行态时刷新回 running
			//（编排器/SendPrompt 自管的运行中任务不动），结束后由 PromptFinished 收尾。
			if !models.IsTaskRunning(def.Tasks[i].Status) {
				now := time.Now()
				def.Tasks[i].Status = models.TaskStatusRunning
				def.Tasks[i].StartedAt = &now
				def.Tasks[i].FinishedAt = nil
				def.Tasks[i].Error = ""
				return store.Save(def)
			}
			return nil
		}
	}
	title := strings.TrimSpace(sess.Title)
	if title == "" {
		title = firstLine(prompt, 40)
	}
	now := time.Now()
	dbID := sess.ID
	task := models.TaskManagerTask{
		ID:          strconv.FormatUint(uint64(sess.ID), 10),
		Title:       title,
		Detail:      prompt,
		AgentType:   sess.AgentType,
		ModelValue:  sess.ModelValue,
		Priority:    models.TaskPriorityP1,
		Status:      models.TaskStatusRunning,
		SessionID:   sess.SessionID,
		DBSessionID: &dbID,
		StartedAt:   &now,
	}
	def.Tasks = append(def.Tasks, task)
	return store.Save(def)
}

// PromptFinished 实现 acp.PromptFinishedNotifier：会话 prompt 流结束时同步 tasks.json
// 中登记条目的状态。会话登记的任务（RegisterSessionTask）不经过编排器 runTask，
// 其运行态只能由本回调收尾，否则完成后永远显示“执行中”。
// 编排器/SendPrompt 正在管理的任务（taskCtx 存活）由其自身收尾，这里跳过避免竞争。
func (s *TaskManagerService) PromptFinished(dbSessionID uint, runStatus string) {
	if dbSessionID == 0 || s.exec == nil {
		return
	}
	sess, err := s.exec.GetSessionByDBID(dbSessionID)
	if err != nil || sess == nil || sess.WorkspaceID == nil {
		return
	}
	ws, err := s.exec.FindWorkspaceByID(*sess.WorkspaceID)
	if err != nil || ws == nil || strings.TrimSpace(ws.Cwd) == "" {
		return
	}
	cwd := ws.Cwd
	def, err := s.storeFor(cwd).Load()
	if err != nil {
		return
	}
	taskID := ""
	for i := range def.Tasks {
		if id := def.Tasks[i].DBSessionID; id != nil && *id == dbSessionID {
			taskID = def.Tasks[i].ID
			break
		}
	}
	if taskID == "" {
		return
	}
	s.mu.Lock()
	_, live := s.taskCtx[cwd+":"+taskID]
	s.mu.Unlock()
	if live {
		return
	}
	status := models.TaskStatusDone
	if runStatus != models.RunningTaskStatusDone {
		status = models.TaskStatusInterrupt
	}
	now := time.Now()
	s.updateTask(cwd, taskID, func(t *models.TaskManagerTask) {
		if models.IsTaskRunning(t.Status) {
			t.Status = status
			t.FinishedAt = &now
		}
	})
}

// GoalStateChanged 实现 acp.GoalStateNotifier：goal 生命周期变化时把状态快照写回
// tasks.json 中对应会话的任务条目（state 为 nil 表示 goal 已清除，保留末态供展示），
// 写入触发 SSE 事件，任务列表自动刷新 goal 徽标。
func (s *TaskManagerService) GoalStateChanged(dbSessionID uint, state *models.TaskGoalState) {
	cwd, taskID := s.taskForSession(dbSessionID)
	if taskID == "" {
		return
	}
	s.updateTask(cwd, taskID, func(t *models.TaskManagerTask) {
		if state == nil {
			// 手动清除：未达终态时标记为已终止，已达成/已终止的末态保留
			if t.Goal != nil && t.Goal.Status != models.TaskGoalStatusAchieved && t.Goal.Status != models.TaskGoalStatusStopped {
				t.Goal.Status = models.TaskGoalStatusStopped
				t.Goal.LastReason = "已手动清除"
				t.Goal.UpdatedAt = time.Now()
			}
			return
		}
		t.Goal = state
	})
}

// taskForSession 按 db 会话 ID 定位其在 tasks.json 中登记的任务（会话→工作区 cwd→任务）。
func (s *TaskManagerService) taskForSession(dbSessionID uint) (cwd, taskID string) {
	if dbSessionID == 0 || s.exec == nil {
		return "", ""
	}
	sess, err := s.exec.GetSessionByDBID(dbSessionID)
	if err != nil || sess == nil || sess.WorkspaceID == nil {
		return "", ""
	}
	ws, err := s.exec.FindWorkspaceByID(*sess.WorkspaceID)
	if err != nil || ws == nil || strings.TrimSpace(ws.Cwd) == "" {
		return "", ""
	}
	def, err := s.storeFor(ws.Cwd).Load()
	if err != nil {
		return "", ""
	}
	for i := range def.Tasks {
		if id := def.Tasks[i].DBSessionID; id != nil && *id == dbSessionID {
			return ws.Cwd, def.Tasks[i].ID
		}
	}
	return "", ""
}

// firstLine 取 prompt 首行并截断到 maxLen 字符，用于任务标题兜底。
func firstLine(prompt string, maxLen int) string {
	s := strings.TrimSpace(prompt)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	if maxLen > 0 && len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

// RecoverAll 在服务启动时调用，将所有 cwd 的 running/queued 状态重置为 interrupt。
// 遍历由外部提供的 cwd 列表（通常来自各 workspace 的 cwd）。
func (s *TaskManagerService) RecoverAll(cwds []string) {
	for _, cwd := range cwds {
		def, err := s.storeFor(cwd).Load()
		if err != nil {
			continue
		}
		changed := false
		for i := range def.Tasks {
			if models.IsTaskRunning(def.Tasks[i].Status) {
				def.Tasks[i].Status = models.TaskStatusInterrupt
				changed = true
			}
		}
		if changed {
			if err := s.storeFor(cwd).Save(def); err != nil {
				slog.Warn("RecoverAll 写回失败", "cwd", cwd, "err", err)
			}
		}
	}
}
