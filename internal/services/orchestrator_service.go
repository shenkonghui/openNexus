package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opennexus/internal/acp"
	"opennexus/internal/models"
)

// OrchestratorExecutor 是编排器执行任务所需的 agent 能力子集（*agent.Router 实现该接口）。
type OrchestratorExecutor interface {
	RunSessionTask(ctx context.Context, cfg acp.SessionTaskConfig) (acp.SessionTaskResult, error)
	FindWorkspaceByID(id uint) (*models.Workspace, error)
	// GetSessionByDBID 按 DB 主键查会话，用于继承父编排会话的 agent 类型。
	GetSessionByDBID(id uint) (*models.Session, error)
	// DefaultAgentType 返回首个已注册 agent 类型，作为任务未指定 agent 时的最终回退。
	DefaultAgentType() string
}

// OrchestratorService 管理任务编排：读写 tasks.json、按并发上限调度任务、
// 基于 git worktree 隔离每个任务的工作目录，并复用 RunSessionTask 创建持久会话执行。
type OrchestratorService struct {
	exec OrchestratorExecutor

	mu      sync.Mutex                    // 保护 defs/运行态
	runs    map[string]*orchRun           // cwd -> 运行态（含信号量、cancel）
	taskCtx map[string]context.CancelFunc // cwd:taskID -> 取消函数
}

type orchRun struct {
	cwd         string
	maxParallel int
	sem         chan struct{}  // 并发槽位
	wg          sync.WaitGroup // 等待所有任务结束
}

// tasksFileName 是 cwd 下编排定义文件名。
const tasksFileName = "tasks.json"

// NewOrchestratorService 创建编排服务。
func NewOrchestratorService(exec OrchestratorExecutor) *OrchestratorService {
	return &OrchestratorService{
		exec:    exec,
		runs:    make(map[string]*orchRun),
		taskCtx: make(map[string]context.CancelFunc),
	}
}

// tasksPath 返回 cwd 下 tasks.json 的绝对路径。
func tasksPath(cwd string) string {
	return filepath.Join(cwd, tasksFileName)
}

// ErrNotGitRepo 表示编排 cwd 不是 git 仓库，需先初始化。
var ErrNotGitRepo = errors.New("当前工作目录不是 git 仓库，请先初始化")

// IsGitRepo 报告 cwd 是否为 git 仓库（编排任务需在 git 仓库内运行，以便隔离 worktree）。
func (s *OrchestratorService) IsGitRepo(cwd string) bool {
	if cwd == "" {
		return false
	}
	if _, err := acp.GitRoot(cwd); err == nil {
		return true
	}
	return acp.IsGitRepo(cwd)
}

// InitGitRepo 在 cwd 初始化 git 仓库（含初始提交），并确保 .worktrees 目录存在。
func (s *OrchestratorService) InitGitRepo(cwd string) error {
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

// Load 读取 cwd 下的 tasks.json。文件不存在时返回空定义（max_parallel 取默认值）。
func (s *OrchestratorService) Load(cwd string) (*models.OrchestrationDef, error) {
	if cwd == "" {
		return nil, fmt.Errorf("cwd 不能为空")
	}
	data, err := os.ReadFile(tasksPath(cwd))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &models.OrchestrationDef{MaxParallel: models.DefaultMaxParallel, Tasks: []models.OrchestrationTask{}}, nil
		}
		return nil, fmt.Errorf("读取 tasks.json: %w", err)
	}
	var def models.OrchestrationDef
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("解析 tasks.json: %w", err)
	}
	if def.MaxParallel <= 0 {
		def.MaxParallel = 1
	}
	if def.Tasks == nil {
		def.Tasks = []models.OrchestrationTask{}
	}
	// 兜底：任务缺省状态置为 pending；归一化常见别名（如 AI 手写 tasks.json 可能用 completed）；
	// 优先级缺省 p1。
	for i := range def.Tasks {
		t := &def.Tasks[i]
		t.Status = models.NormalizeOrchTaskStatus(t.Status)
		t.Priority = models.NormalizeOrchTaskPriority(t.Priority)
	}
	return &def, nil
}

// Save 将编排定义写回 cwd 下 tasks.json（原子写）。
func (s *OrchestratorService) Save(cwd string, def *models.OrchestrationDef) error {
	if cwd == "" {
		return fmt.Errorf("cwd 不能为空")
	}
	if def == nil {
		def = &models.OrchestrationDef{}
	}
	if def.MaxParallel <= 0 {
		def.MaxParallel = 1
	}
	if def.Tasks == nil {
		def.Tasks = []models.OrchestrationTask{}
	}
	for i := range def.Tasks {
		def.Tasks[i].Priority = models.NormalizeOrchTaskPriority(def.Tasks[i].Priority)
	}
	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 tasks.json: %w", err)
	}
	tmp := tasksPath(cwd) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入 tasks.json: %w", err)
	}
	return os.Rename(tmp, tasksPath(cwd))
}

// UpsertTask 新增或更新（按 id 匹配）单个任务，并写回文件。
func (s *OrchestratorService) UpsertTask(cwd string, task models.OrchestrationTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, err := s.Load(cwd)
	if err != nil {
		return err
	}
	if task.Status == "" {
		task.Status = models.OrchTaskStatusPending
	}
	incomingPri := strings.TrimSpace(task.Priority)
	found := false
	for i := range def.Tasks {
		if def.Tasks[i].ID == task.ID {
			// 保留运行时字段（session/状态/时间戳），仅更新可编辑字段
			cur := def.Tasks[i]
			task.SessionID = cur.SessionID
			task.DBSessionID = cur.DBSessionID
			task.Status = cur.Status
			task.WorktreePath = cur.WorktreePath
			task.StartedAt = cur.StartedAt
			task.FinishedAt = cur.FinishedAt
			task.Error = cur.Error
			if task.Branch == "" {
				task.Branch = cur.Branch
			}
			// 未传 priority 时保留原值，避免更新其它字段时被重置为 p1
			if incomingPri == "" {
				task.Priority = cur.Priority
			} else {
				task.Priority = models.NormalizeOrchTaskPriority(incomingPri)
			}
			def.Tasks[i] = task
			found = true
			break
		}
	}
	if !found {
		task.Priority = models.NormalizeOrchTaskPriority(incomingPri)
		def.Tasks = append(def.Tasks, task)
	}
	return s.Save(cwd, def)
}

// DeleteTask 删除指定任务。若任务正在运行则先取消，并尝试清理其 worktree。
func (s *OrchestratorService) DeleteTask(cwd, taskID string) error {
	s.mu.Lock()
	def, err := s.Load(cwd)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	var (
		idx    = -1
		wtPath string
		branch string
	)
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			idx = i
			wtPath = def.Tasks[i].WorktreePath
			branch = def.Tasks[i].Branch
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("任务 %s 不存在", taskID)
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
	err = s.Save(cwd, def)
	s.mu.Unlock()
	return err
}

// SetMaxParallel 更新并发上限。若当前有运行态且新值更小，已启动的任务不受影响，
// 新任务按新上限排队。
func (s *OrchestratorService) SetMaxParallel(cwd string, maxParallel int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, err := s.Load(cwd)
	if err != nil {
		return err
	}
	if maxParallel <= 0 {
		maxParallel = 1
	}
	def.MaxParallel = maxParallel
	if r, ok := s.runs[cwd]; ok {
		// 重建信号量（仅影响尚未获取槽位的等待者）
		newSem := make(chan struct{}, maxParallel)
		r.sem = newSem
		r.maxParallel = maxParallel
	}
	return s.Save(cwd, def)
}

// Start 启动任务。taskID 为空时启动全部 pending/failed/canceled/interrupt 任务，
// 否则仅启动指定任务。已在运行的不会重复启动。
func (s *OrchestratorService) Start(ctx context.Context, cwd string, workspaceID uint, userID uint, taskID string) error {
	// 编排任务基于 git worktree 隔离，要求 cwd 为 git 仓库；非仓库时自动初始化（含初始提交），
	// 免去用户手动 git init 的步骤。
	if !s.IsGitRepo(cwd) {
		if err := s.InitGitRepo(cwd); err != nil {
			return err
		}
	}
	def, err := s.Load(cwd)
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
	var targets []models.OrchestrationTask
	for i := range def.Tasks {
		t := &def.Tasks[i]
		if taskID != "" && t.ID != taskID {
			continue
		}
		if models.IsOrchTaskRunning(t.Status) {
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
		if t.Status == models.OrchTaskStatusDone && taskID == "" {
			continue // 全部启动时跳过已完成；显式单任务启动可重跑
		}
		// 重置为 queued
		t.Status = models.OrchTaskStatusQueued
		t.Error = ""
		targets = append(targets, *t)
	}
	if len(targets) == 0 {
		return nil
	}
	// 高优先级先抢并发槽位（p0 > p1 > p2）
	sort.SliceStable(targets, func(i, j int) bool {
		return models.OrchTaskPriorityRank(targets[i].Priority) < models.OrchTaskPriorityRank(targets[j].Priority)
	})
	// 持久化 queued 状态
	if err := s.Save(cwd, def); err != nil {
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
func (s *OrchestratorService) runTask(run *orchRun, t *models.OrchestrationTask, workspaceID, userID uint) {
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
	s.updateTask(run.cwd, t.ID, func(task *models.OrchestrationTask) {
		task.Status = models.OrchTaskStatusRunning
		task.StartedAt = &now
		task.Error = ""
	})

	result, runErr := s.executeTask(ctx, run.cwd, t, workspaceID, userID)

	fin := time.Now()
	s.updateTask(run.cwd, t.ID, func(task *models.OrchestrationTask) {
		task.FinishedAt = &fin
		task.SessionID = result.SessionID
		if result.DBSessionID > 0 {
			dbID := result.DBSessionID
			task.DBSessionID = &dbID
		}
		if runErr != nil {
			task.Status = models.OrchTaskStatusFailed
			task.Error = runErr.Error()
			return
		}
		if !result.Success {
			task.Status = models.OrchTaskStatusFailed
			task.Error = result.Error
			return
		}
		task.Status = models.OrchTaskStatusDone
	})
}

// executeTask 创建 worktree 并调用 RunSessionTask 执行任务。
func (s *OrchestratorService) executeTask(ctx context.Context, cwd string, t *models.OrchestrationTask, workspaceID, userID uint) (acp.SessionTaskResult, error) {
	// 解析仓库根（worktree add 需在公共 git 仓库下执行）
	repoRoot := cwd
	if root, err := acp.GitRoot(cwd); err == nil {
		repoRoot = root
	}
	if err := acp.EnsureWorktreesDir(repoRoot); err != nil {
		return acp.SessionTaskResult{}, fmt.Errorf("创建 worktrees 目录: %w", err)
	}

	branch := t.Branch
	if branch == "" {
		branch = "task-" + t.ID
	}
	wtPath := acp.WorktreePath(repoRoot, t.ID)

	// 若 worktree 已存在（如上次中断），先清理重建
	if _, err := os.Stat(wtPath); err == nil {
		_ = acp.RemoveWorktree(repoRoot, wtPath, branch)
	}
	if err := acp.CreateWorktree(repoRoot, branch, wtPath, ""); err != nil {
		return acp.SessionTaskResult{}, fmt.Errorf("创建 worktree: %w", err)
	}

	// 记录 branch/worktreePath
	s.updateTask(cwd, t.ID, func(task *models.OrchestrationTask) {
		task.Branch = branch
		task.WorktreePath = wtPath
	})

	// 解析 agent 类型：任务未指定时回退到首个已注册 agent。
	// 直接把空 agent_type 传给 RunSessionTask 会因 GetBackend 失败而报"agent 类型未注册"。
	agentType := t.AgentType
	if agentType == "" {
		agentType = s.exec.DefaultAgentType()
		// 回写解析结果，使 UI 显示实际使用的 agent，并让后续重跑保持一致。
		if agentType != "" {
			resolved := agentType
			s.updateTask(cwd, t.ID, func(task *models.OrchestrationTask) {
				task.AgentType = resolved
			})
		}
	}

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
			s.updateTask(cwd, t.ID, func(task *models.OrchestrationTask) {
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

// Stop 停止任务。taskID 为空时停止该 cwd 下全部运行中/排队中任务。
func (s *OrchestratorService) Stop(cwd, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, err := s.Load(cwd)
	if err != nil {
		return err
	}
	for i := range def.Tasks {
		t := &def.Tasks[i]
		if taskID != "" && t.ID != taskID {
			continue
		}
		if !models.IsOrchTaskRunning(t.Status) {
			continue
		}
		s.cancelLocked(cwd, t.ID)
		now := time.Now()
		t.Status = models.OrchTaskStatusCanceled
		t.FinishedAt = &now
		t.Error = "用户手动停止"
	}
	return s.Save(cwd, def)
}

// cancelLocked 取消指定任务（必须在持有 s.mu 时调用）。
func (s *OrchestratorService) cancelLocked(cwd, taskID string) {
	key := cwd + ":" + taskID
	if cancel, ok := s.taskCtx[key]; ok {
		cancel()
		delete(s.taskCtx, key)
	}
}

// markCanceled 在 ctx 提前结束（如排队时被停止）时更新状态。
func (s *OrchestratorService) markCanceled(cwd, taskID, reason string) {
	now := time.Now()
	s.updateTask(cwd, taskID, func(task *models.OrchestrationTask) {
		if models.IsOrchTaskRunning(task.Status) {
			task.Status = models.OrchTaskStatusCanceled
			task.FinishedAt = &now
			task.Error = reason
		}
	})
}

// updateTask 加载 → 修改指定任务 → 写回。修改函数在 nil 任务（未找到）时不执行。
func (s *OrchestratorService) updateTask(cwd, taskID string, mutate func(*models.OrchestrationTask)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, err := s.Load(cwd)
	if err != nil {
		slog.Warn("updateTask 加载 tasks.json 失败", "cwd", cwd, "err", err)
		return
	}
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			mutate(&def.Tasks[i])
			break
		}
	}
	if err := s.Save(cwd, def); err != nil {
		slog.Warn("updateTask 写回 tasks.json 失败", "cwd", cwd, "err", err)
	}
}

// RegisterSessionTask 把一个已存在的会话作为任务登记到 cwd 下的 tasks.json。
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
func (s *OrchestratorService) RegisterSessionTask(cwd string, sess *models.Session, prompt string) error {
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
	def, err := s.Load(cwd)
	if err != nil {
		return fmt.Errorf("加载 tasks.json: %w", err)
	}
	for i := range def.Tasks {
		if existing := def.Tasks[i].DBSessionID; existing != nil && *existing == sess.ID {
			return nil // 已登记，跳过
		}
	}
	title := strings.TrimSpace(sess.Title)
	if title == "" {
		title = firstLine(prompt, 40)
	}
	now := time.Now()
	dbID := sess.ID
	task := models.OrchestrationTask{
		ID:          strconv.FormatUint(uint64(sess.ID), 10),
		Title:       title,
		Detail:      prompt,
		AgentType:   sess.AgentType,
		ModelValue:  sess.ModelValue,
		Priority:    models.OrchTaskPriorityP1,
		Status:      models.OrchTaskStatusRunning,
		SessionID:   sess.SessionID,
		DBSessionID: &dbID,
		StartedAt:   &now,
	}
	def.Tasks = append(def.Tasks, task)
	return s.Save(cwd, def)
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
func (s *OrchestratorService) RecoverAll(cwds []string) {
	for _, cwd := range cwds {
		def, err := s.Load(cwd)
		if err != nil {
			continue
		}
		changed := false
		for i := range def.Tasks {
			if models.IsOrchTaskRunning(def.Tasks[i].Status) {
				def.Tasks[i].Status = models.OrchTaskStatusInterrupt
				changed = true
			}
		}
		if changed {
			if err := s.Save(cwd, def); err != nil {
				slog.Warn("RecoverAll 写回失败", "cwd", cwd, "err", err)
			}
		}
	}
}
