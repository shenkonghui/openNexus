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
	// CancelSession 取消会话正在进行的 prompt（连带清除 goal）。任务会话的 prompt 用
	// detached context 运行，停止/归档时仅取消 taskCtx 无法终止 agent，需显式调用。
	CancelSession(ctx context.Context, sessionID string) error
	// DeleteSession 删除会话（含消息），删除任务时用于同步移除其关联会话。
	DeleteSession(ctx context.Context, sessionID string) error
	// LastRunStatus 返回会话最近一次 prompt 的真实终态（done/interrupted），
	// 消息流关闭后回查用，避免中断收尾被误判为完成；空串表示无记录。
	LastRunStatus(dbSessionID uint) string
	// HasActivePrompt 判断会话是否有进行中的 prompt（goal 清除时判断能否直接结算终态）。
	HasActivePrompt(sessionID string) bool
	// EnableGoal 为会话自动开启 goal 模式（已有生效 goal 时不重置），
	// 用于任务重发/继续对话时恢复服务重启丢失的 goal 内存态。
	EnableGoal(sessionID, condition string) error
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

// DeleteTask 删除指定任务。若任务正在运行则先取消，并尝试清理其 worktree；
// 任务关联的会话（db_session_id/session_id）一并删除，使左侧任务列表同步移除，
// 与「删除会话 → 注销任务」（UnregisterSessionTask）保持对称。
func (s *TaskManagerService) DeleteTask(cwd, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed, err := s.deleteTaskLocked(cwd, func(t *models.TaskManagerTask) bool { return t.ID == taskID }, true)
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
	// 会话本身正在被删除，此处只需移除登记条目，不再回头删会话。
	_, err := s.deleteTaskLocked(cwd, func(t *models.TaskManagerTask) bool {
		return t.DBSessionID != nil && *t.DBSessionID == dbSessionID
	}, false)
	return err
}

// ==================== 归档（回收站） ====================

// ArchiveTask 归档指定任务：若正在运行先真正停止（取消编排 watcher 与底层会话 prompt，
// 快照状态置 canceled），然后从 tasks.json 移入归档文件。直接快照 running 会让回收站
// 永远显示“执行中”，且 agent/goal 循环仍在后台继续执行。
// worktree 与关联会话保留，供恢复时原样放回；过期清理/彻底删除时才一并清理。
func (s *TaskManagerService) ArchiveTask(cwd, taskID string) error {
	s.mu.Lock()
	s.cancelLocked(cwd, taskID)
	s.mu.Unlock()
	store := s.storeFor(cwd)
	// 归档前收尾运行态：先写回 canceled，再取消会话，保证 PromptFinished 竞态时
	// IsTaskRunning 已为 false 不会覆写状态。
	var sessID string
	wasRunning := false
	_ = store.UpdateTaskStatus(taskID, func(t *models.TaskManagerTask) {
		if models.IsTaskRunning(t.Status) {
			wasRunning = true
			sessID = t.SessionID
			now := time.Now()
			t.Status = models.TaskStatusCanceled
			t.FinishedAt = &now
			t.Error = "归档时停止"
		}
	})
	// 任务会话的 prompt 用 detached context 运行，取消 taskCtx 并不会终止 agent；
	// 显式取消会话（连带清除 goal，写回的 goal 末态一并进入快照），失败不阻断归档。
	if wasRunning && sessID != "" && s.exec != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if cerr := s.exec.CancelSession(ctx, sessID); cerr != nil {
			slog.Warn("归档任务时取消会话失败", "task", taskID, "session", sessID, "err", cerr)
		}
		cancel()
	}
	_, err := store.ArchiveTask(taskID)
	return err
}

// ArchiveAllTasks 归档该 cwd 下全部任务，返回归档数量。
func (s *TaskManagerService) ArchiveAllTasks(cwd string) (int, error) {
	def, err := s.storeFor(cwd).Load()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range def.Tasks {
		if aerr := s.ArchiveTask(cwd, t.ID); aerr != nil {
			slog.Warn("归档任务失败", "task", t.ID, "err", aerr)
			continue
		}
		n++
	}
	return n, nil
}

// ListArchivedTasks 返回回收站内容（按归档时间倒序）。
func (s *TaskManagerService) ListArchivedTasks(cwd string) ([]models.ArchivedTask, error) {
	return s.storeFor(cwd).ListArchived()
}

// RestoreArchivedTask 把归档任务恢复回 tasks.json。
func (s *TaskManagerService) RestoreArchivedTask(cwd, taskID string) error {
	_, err := s.storeFor(cwd).RestoreArchived(taskID)
	return err
}

// DeleteArchivedTask 从回收站彻底删除归档任务，并清理其 worktree 与关联会话。
func (s *TaskManagerService) DeleteArchivedTask(cwd, taskID string) error {
	entry, err := s.storeFor(cwd).RemoveArchived(taskID)
	if err != nil {
		return err
	}
	s.cleanupArchived(cwd, entry)
	return nil
}

// PurgeExpiredArchived 清理归档超过保留期的条目（含 worktree/会话），返回清理数量。
// retentionDays <= 0 时取默认保留天数。失败仅告警，不阻断调用方。
func (s *TaskManagerService) PurgeExpiredArchived(cwd string, retentionDays int) int {
	if retentionDays <= 0 {
		retentionDays = models.DefaultArchiveRetentionDays
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	expired, err := s.storeFor(cwd).PurgeArchivedBefore(cutoff)
	if err != nil {
		slog.Warn("清理过期归档任务失败", "cwd", cwd, "err", err)
		return 0
	}
	for i := range expired {
		s.cleanupArchived(cwd, &expired[i])
	}
	return len(expired)
}

// cleanupArchived 彻底删除归档条目时清理其 worktree 与关联会话（best-effort，失败仅记日志），
// 与 deleteTaskLocked 的清理逻辑保持一致。
func (s *TaskManagerService) cleanupArchived(cwd string, entry *models.ArchivedTask) {
	if entry == nil {
		return
	}
	if entry.WorktreePath != "" {
		if rerr := acp.RemoveWorktree(cwd, entry.WorktreePath, entry.Branch); rerr != nil {
			slog.Warn("删除归档任务时清理 worktree 失败", "task", entry.ID, "err", rerr)
		}
	}
	if s.exec == nil {
		return
	}
	sid := entry.SessionID
	if sid == "" && entry.DBSessionID != nil {
		if sess, gerr := s.exec.GetSessionByDBID(*entry.DBSessionID); gerr == nil && sess != nil {
			sid = sess.SessionID
		}
	}
	if sid != "" {
		if derr := s.exec.DeleteSession(context.Background(), sid); derr != nil {
			slog.Warn("删除归档任务时清理关联会话失败", "task", entry.ID, "session", sid, "err", derr)
		}
	}
}

// deleteTaskLocked 删除首个匹配的任务（需持有 s.mu）：取消运行、清理 worktree 并写回。
// deleteSession 为 true 时连带删除任务关联的会话（best-effort，失败仅记日志），
// 会话发起的注销（UnregisterSessionTask）传 false 避免重复删除。返回是否删除了条目。
func (s *TaskManagerService) deleteTaskLocked(cwd string, match func(*models.TaskManagerTask) bool, deleteSession bool) (bool, error) {
	def, err := s.storeFor(cwd).Load()
	if err != nil {
		return false, err
	}
	idx := -1
	var taskID, wtPath, branch, sessID string
	var dbSessID *uint
	for i := range def.Tasks {
		if match(&def.Tasks[i]) {
			idx = i
			taskID = def.Tasks[i].ID
			wtPath = def.Tasks[i].WorktreePath
			branch = def.Tasks[i].Branch
			sessID = def.Tasks[i].SessionID
			dbSessID = def.Tasks[i].DBSessionID
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
	// 连带删除关联会话：否则左侧任务列表（读 DB 会话）仍展示已删任务的对话条目，
	// 与右侧任务列表（读 tasks.json）不一致。
	if deleteSession && s.exec != nil {
		sid := sessID
		if sid == "" && dbSessID != nil {
			if sess, gerr := s.exec.GetSessionByDBID(*dbSessID); gerr == nil && sess != nil {
				sid = sess.SessionID
			}
		}
		if sid != "" {
			if derr := s.exec.DeleteSession(context.Background(), sid); derr != nil {
				slog.Warn("删除任务时清理关联会话失败", "task", taskID, "session", sid, "err", derr)
			}
		}
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
		task.SessionID = result.SessionID
		if result.DBSessionID > 0 {
			dbID := result.DBSessionID
			task.DBSessionID = &dbID
		}
		if runErr != nil {
			task.Status = models.TaskStatusFailed
			task.Error = runErr.Error()
			task.FinishedAt = &fin
			return
		}
		if result.Background {
			// 收集超时/调用取消但会话仍在后台运行：保持 running，
			// 由 PromptFinished 在 prompt 真正结束时按真实终态收尾。
			return
		}
		if result.Interrupted {
			// 中断收尾（超时/断开）：置 interrupt 可重发，不算完成也不算失败。
			task.Status = models.TaskStatusInterrupt
			task.Error = result.Error
			task.FinishedAt = &fin
			return
		}
		if !result.Success {
			task.Status = models.TaskStatusFailed
			task.Error = result.Error
			task.FinishedAt = &fin
			return
		}
		if goalActive(task) {
			// goal 生效中：审核通过（achieved）才算完成，保持 running，
			// 由 GoalStateChanged 在 goal 达成/终止时收尾。
			return
		}
		task.Status = models.TaskStatusDone
		task.FinishedAt = &fin
	})
}

// taskBranchNamePrompt 是 AI 生成任务分支名的提示词模板。
const taskBranchNamePrompt = `请为以下开发任务生成一个简短的英文 git 分支名，要求以 feat/ 或 fix/ 开头（新功能用 feat/，缺陷修复用 fix/），斜杠后为 kebab-case 的 2-4 个英文单词，只用小写字母、数字和连字符，概括任务核心内容。
任务标题：{{title}}
任务详情：{{detail}}
仅输出分支名，不要输出其他任何内容。`

// generateTaskBranch 生成任务的 worktree 分支名：优先用任务的 agent 做一次性 AI 命名
// （规范化为 feat//fix/ 前缀），失败回退到标题/详情清洗，仍为空则兜底 task-<ID>。
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

	// 运行目录：缺省在专属 git worktree 内隔离运行；NoWorktree 任务直接在工作区目录运行。
	runCwd := cwd
	if !t.NoWorktree {
		// 解析仓库根（worktree add 需在公共 git 仓库下执行）
		repoRoot := cwd
		if root, err := acp.GitRoot(cwd); err == nil {
			repoRoot = root
		}
		if err := acp.EnsureWorktreesDir(repoRoot); err != nil {
			return acp.SessionTaskResult{}, fmt.Errorf("创建 worktrees 目录: %w", err)
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
		runCwd = wtPath
	}

	cfg := acp.SessionTaskConfig{
		AgentType:   agentType,
		ModelValue:  t.ModelValue,
		Prompt:      t.Detail,
		UserID:      userID,
		WorkspaceID: workspaceID,
		Source:      models.SessionSourceManual,
		// 任务在其专属 git worktree（或 NoWorktree 时的工作区目录）内运行。
		Cwd: runCwd,
		// 任务定义了 goal 时自动开启 goal 模式：达成前任务保持 running，
		// 终态由 GoalStateChanged 回调收尾（achieved→done / stopped→failed）。
		Goal: strings.TrimSpace(t.GoalCondition),
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

	// 任务定义了 goal：重发/继续对话前恢复 goal 模式（服务重启会丢失 goal 内存态；
	// 已有生效 goal 时 EnableGoal 不重置，保留续轮/审计计数）。
	if cond := strings.TrimSpace(task.GoalCondition); cond != "" {
		if err := s.exec.EnableGoal(task.SessionID, cond); err != nil {
			slog.Warn("继续对话前恢复 goal 失败", "task", taskID, "session", task.SessionID, "err", err)
		}
	}

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
					// 消息流结束：回查本轮真实终态（prompt 消费方在关闭流前已落库），
					// 中断收尾置 interrupt 而非 done；goal 生效中保持 running，
					// 由 GoalStateChanged 收尾。仅在仍为运行态时写入（Stop 会先置 canceled）。
					runStatus := ""
					if task.DBSessionID != nil {
						runStatus = s.exec.LastRunStatus(*task.DBSessionID)
					}
					fin := time.Now()
					s.updateTask(cwd, taskID, func(t *models.TaskManagerTask) {
						if !models.IsTaskRunning(t.Status) {
							return
						}
						if runStatus != "" && runStatus != models.RunningTaskStatusDone {
							t.Status = models.TaskStatusInterrupt
							t.FinishedAt = &fin
							return
						}
						if goalActive(t) {
							return
						}
						t.Status = models.TaskStatusDone
						t.FinishedAt = &fin
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
	store := s.storeFor(cwd)
	def, err := store.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	var sessIDs []string
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
		if t.SessionID != "" {
			sessIDs = append(sessIDs, t.SessionID)
		}
	}
	saveErr := store.Save(def)
	s.mu.Unlock()
	// 取消底层会话 prompt（detached context，仅取消 taskCtx 无法终止 agent），
	// 连带清除 goal 避免自动续轮；best-effort，失败仅记日志。
	if s.exec != nil {
		for _, sid := range sessIDs {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if cerr := s.exec.CancelSession(ctx, sid); cerr != nil {
				slog.Warn("停止任务时取消会话失败", "session", sid, "err", cerr)
			}
			cancel()
		}
	}
	return saveErr
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
			// 已登记：不新增条目，但补充缺失字段并刷新运行态。
			// 创建时登记的条目可能 detail/title 为空（req.Prompt 缺省），
			// 首次发送 prompt 时回填。
			changed := false
			if prompt != "" && strings.TrimSpace(def.Tasks[i].Detail) == "" {
				def.Tasks[i].Detail = prompt
				changed = true
			}
			title := strings.TrimSpace(sess.Title)
			if title != "" && strings.TrimSpace(def.Tasks[i].Title) == "" {
				def.Tasks[i].Title = title
				changed = true
			}
			if !models.IsTaskRunning(def.Tasks[i].Status) {
				now := time.Now()
				def.Tasks[i].Status = models.TaskStatusRunning
				def.Tasks[i].StartedAt = &now
				def.Tasks[i].FinishedAt = nil
				def.Tasks[i].Error = ""
				changed = true
			}
			if changed {
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
		if !models.IsTaskRunning(t.Status) {
			return
		}
		if status == models.TaskStatusDone && goalActive(t) {
			// goal 生效中：本轮结束不算任务完成，保持 running 等 goal 审核，
			// 达成/终止时由 GoalStateChanged 收尾。
			return
		}
		t.Status = status
		t.FinishedAt = &now
	})
}

// goalActive 报告任务的 goal 是否仍在推进（生效中/评估中）。此期间任务保持
// running，审核通过（achieved）才置 done，失败/终止（stopped）置 failed。
func goalActive(t *models.TaskManagerTask) bool {
	return t.Goal != nil &&
		(t.Goal.Status == models.TaskGoalStatusActive || t.Goal.Status == models.TaskGoalStatusEvaluating)
}

// GoalStateChanged 实现 acp.GoalStateNotifier：goal 生命周期变化时把状态快照写回
// tasks.json 中对应会话的任务条目（state 为 nil 表示 goal 已清除，保留末态供展示），
// 写入触发 SSE 事件，任务列表自动刷新 goal 徽标。
// goal 终态同时联动任务终态：achieved → done，stopped → failed（goal 生效期间
// 任务一直保持 running，这里是唯一的收尾点）；仅改写运行态任务，避免覆盖
// canceled 等人工终态。
func (s *TaskManagerService) GoalStateChanged(dbSessionID uint, state *models.TaskGoalState) {
	cwd, taskID := s.taskForSession(dbSessionID)
	if taskID == "" {
		return
	}
	// 手动清除时需判断会话是否还有进行中的 prompt（有则由 turn 结束时自然结算）。
	promptActive := false
	if state == nil {
		if sess, err := s.exec.GetSessionByDBID(dbSessionID); err == nil && sess != nil {
			promptActive = s.exec.HasActivePrompt(sess.SessionID)
		}
	}
	now := time.Now()
	s.updateTask(cwd, taskID, func(t *models.TaskManagerTask) {
		if state == nil {
			// 手动清除：未达终态时标记为已终止，已达成/已终止的末态保留
			if t.Goal != nil && t.Goal.Status != models.TaskGoalStatusAchieved && t.Goal.Status != models.TaskGoalStatusStopped {
				t.Goal.Status = models.TaskGoalStatusStopped
				t.Goal.LastReason = "已手动清除"
				t.Goal.UpdatedAt = time.Now()
			}
			// goal 门控解除后，若任务仍在 running 且无进行中 prompt，
			// 说明本轮已因 goal 生效而保持 running，这里直接结算为完成。
			if t.Status == models.TaskStatusRunning && !promptActive {
				t.Status = models.TaskStatusDone
				t.Error = ""
				t.FinishedAt = &now
			}
			return
		}
		t.Goal = state
		switch state.Status {
		case models.TaskGoalStatusAchieved:
			if models.IsTaskRunning(t.Status) || t.Status == models.TaskStatusInterrupt {
				t.Status = models.TaskStatusDone
				t.Error = ""
				t.FinishedAt = &now
			}
		case models.TaskGoalStatusStopped:
			if models.IsTaskRunning(t.Status) || t.Status == models.TaskStatusInterrupt {
				t.Status = models.TaskStatusFailed
				reason := strings.TrimSpace(state.LastReason)
				if reason == "" {
					reason = "goal 已终止"
				}
				if state.EvalCount > 0 {
					reason = fmt.Sprintf("%s（已审计 %d 次）", reason, state.EvalCount)
				}
				t.Error = reason
				t.FinishedAt = &now
			}
		}
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

// firstLine 取 prompt 首行并按 rune 截断到 maxLen 个字符，用于任务标题兜底。
// 与 acp.extractTitle 行为对齐：剥离行首的 slash 命令前缀（如 /goal），
// 避免命令名出现在标题里；按 rune 截断避免切断多字节字符产生乱码。
func firstLine(prompt string, maxLen int) string {
	s := strings.TrimSpace(prompt)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "/") {
		if idx := strings.IndexByte(s, ' '); idx >= 0 {
			s = strings.TrimSpace(s[idx+1:])
		}
	}
	if runes := []rune(s); maxLen > 0 && len(runes) > maxLen {
		s = string(runes[:maxLen])
	}
	return s
}

// RecoverAll 在服务启动时调用，将所有 cwd 的 running/queued 状态重置为 interrupt。
// 遍历由外部提供的 cwd 列表（通常来自各 workspace 的 cwd）。
// goal 是会话内存态不跨重启存活，残留的 active/evaluating 快照一并置
// stopped，避免重启后僵尸 goal 徽标持续显示“生效中”。
// 返回本次被标记中断且定义了 goal 的任务，供启动后自动续跑（仅带 goal 的自动恢复）。
func (s *TaskManagerService) RecoverAll(cwds []string) []InterruptedGoalTask {
	var resumable []InterruptedGoalTask
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
				if strings.TrimSpace(def.Tasks[i].GoalCondition) != "" {
					resumable = append(resumable, InterruptedGoalTask{
						Cwd:        cwd,
						TaskID:     def.Tasks[i].ID,
						HasSession: def.Tasks[i].SessionID != "",
					})
				}
			}
			if g := def.Tasks[i].Goal; g != nil &&
				(g.Status == models.TaskGoalStatusActive || g.Status == models.TaskGoalStatusEvaluating) {
				g.Status = models.TaskGoalStatusStopped
				g.LastReason = "服务重启中断"
				g.UpdatedAt = time.Now()
				changed = true
			}
		}
		if changed {
			if err := s.storeFor(cwd).Save(def); err != nil {
				slog.Warn("RecoverAll 写回失败", "cwd", cwd, "err", err)
			}
		}
	}
	return resumable
}

// InterruptedGoalTask 记录 RecoverAll 时因重启被标记中断、且定义了 goal 的任务。
type InterruptedGoalTask struct {
	Cwd        string
	TaskID     string
	HasSession bool // 已有会话可直接继续对话；否则需重新启动
}

// AutoResumeGoalTasks 服务重启后自动续跑带 goal 的中断任务：
// 已有会话的走继续对话（SendPrompt 会自动恢复 goal 模式），
// 重启前仍在排队、尚无会话的重新启动。普通任务（无 goal）不自动续跑，
// 由用户手动决定，避免重启意外拉起大批 agent 会话。
// lookupWorkspace 用于无会话任务重新启动时取 workspaceID/userID。
func (s *TaskManagerService) AutoResumeGoalTasks(tasks []InterruptedGoalTask, lookupWorkspace func(cwd string) (*models.Workspace, error)) {
	const resumePrompt = "服务重启导致上一轮执行中断。请检查当前进度，继续完成任务目标，无需向用户确认。"
	for _, it := range tasks {
		if it.HasSession {
			if err := s.SendPrompt(context.Background(), it.Cwd, it.TaskID, resumePrompt); err != nil {
				slog.Warn("重启自动续跑失败（继续对话）", "cwd", it.Cwd, "task", it.TaskID, "err", err)
			} else {
				slog.Info("重启自动续跑（继续对话）", "cwd", it.Cwd, "task", it.TaskID)
			}
			continue
		}
		ws, err := lookupWorkspace(it.Cwd)
		if err != nil || ws == nil {
			slog.Warn("重启自动续跑失败：找不到工作区", "cwd", it.Cwd, "task", it.TaskID, "err", err)
			continue
		}
		if err := s.Start(context.Background(), it.Cwd, ws.ID, ws.UserID, it.TaskID); err != nil {
			slog.Warn("重启自动续跑失败（重新启动）", "cwd", it.Cwd, "task", it.TaskID, "err", err)
		} else {
			slog.Info("重启自动续跑（重新启动）", "cwd", it.Cwd, "task", it.TaskID)
		}
	}
}
