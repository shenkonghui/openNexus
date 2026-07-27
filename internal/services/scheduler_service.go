package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/robfig/cron/v3"

	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// SchedulerExecutor 是调度器执行定时任务所需的 agent 能力子集（*agent.Router 实现该接口）。
type SchedulerExecutor interface {
	CreateSessionWithSource(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue string) (*models.Session, error)
	PromptWithExecution(ctx context.Context, sessionID, prompt string, executionID *uint) (<-chan models.Message, error)
	ResumeSession(ctx context.Context, sessionID string) (*models.Session, error)
	GetSessionByDBID(id uint) (*models.Session, error)
	DeleteSession(ctx context.Context, sessionID string) error
	ListExecutions(sessionID string) ([]repository.ExecutionAggregate, error)
	ListConfigOptions(sessionID string) ([]acpsdk.SessionConfigOption, error)
	SetConfigOption(ctx context.Context, sessionID, configID, value string) error
	CreateWorkspace(ws *models.Workspace) error
}

// SchedulerService 是进程内 cron 调度器，管理定时任务的调度与执行。
// 定时任务配置统一存储在工作区 cwd 下的 tasks.json 中，执行历史写入
// {cwd}/.openNexus/scheduled-executions.jsonl。
type SchedulerService struct {
	wsRepo *repository.WorkspaceRepository
	exec   SchedulerExecutor
	cron   *cron.Cron

	mu       sync.Mutex
	entries  map[string]cron.EntryID // key = cwd + ":" + taskID
	taskLock sync.Map                // key = cwd + ":" + taskID -> *sync.Mutex
	stopOnce sync.Once
}

// NewSchedulerService 创建调度器。调用 Start() 后开始调度。
func NewSchedulerService(wsRepo *repository.WorkspaceRepository, exec SchedulerExecutor) *SchedulerService {
	return &SchedulerService{
		wsRepo:  wsRepo,
		exec:    exec,
		cron:    cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow))),
		entries: make(map[string]cron.EntryID),
	}
}

// taskKey 生成定时任务在调度器内的唯一键。
func taskKey(cwd, taskID string) string {
	return cwd + ":" + taskID
}

// Start 启动调度器并加载所有工作区 tasks.json 中 enabled 的定时任务。
func (s *SchedulerService) Start() error {
	cwds, err := s.wsRepo.ListCwds()
	if err != nil {
		return fmt.Errorf("加载工作区列表失败: %w", err)
	}
	total := 0
	for _, cwd := range cwds {
		tasks, err := NewTaskStore(cwd).ListScheduledTasks()
		if err != nil {
			log.Printf("加载工作区 %s 定时任务失败: %v", cwd, err)
			continue
		}
		for _, t := range tasks {
			if t.Schedule == nil || !t.Schedule.Enabled {
				continue
			}
			if err := s.schedule(cwd, t); err != nil {
				log.Printf("调度定时任务 %s/%s 失败: %v", cwd, t.ID, err)
				continue
			}
			total++
		}
	}
	s.cron.Start()
	log.Printf("定时任务调度器已启动，共加载 %d 个任务", total)
	return nil
}

// Stop 停止调度器。
func (s *SchedulerService) Stop() {
	s.stopOnce.Do(func() {
		ctx := s.cron.Stop()
		<-ctx.Done()
	})
}

// schedule 为单个定时任务添加 cron 调度项。
func (s *SchedulerService) schedule(cwd string, t models.TaskManagerTask) error {
	key := taskKey(cwd, t.ID)
	entryID, err := s.cron.AddFunc(t.Schedule.CronExpr, func() {
		s.run(cwd, t.ID)
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.entries[key] = entryID
	s.mu.Unlock()
	return nil
}

// unschedule 移除任务的 cron 调度项。
func (s *SchedulerService) unschedule(cwd, taskID string) {
	key := taskKey(cwd, taskID)
	s.mu.Lock()
	entryID, ok := s.entries[key]
	if ok {
		delete(s.entries, key)
	}
	s.mu.Unlock()
	if ok {
		s.cron.Remove(entryID)
	}
}

// AddTask 把定时任务写入 tasks.json 并注册调度。
func (s *SchedulerService) AddTask(cwd string, t *models.TaskManagerTask) error {
	if t.Schedule == nil {
		t.Schedule = &models.TaskSchedule{Enabled: true, TimeoutMinutes: 5}
	}
	if err := NewTaskStore(cwd).UpsertTask(*t); err != nil {
		return err
	}
	if t.Schedule.Enabled {
		if err := s.schedule(cwd, *t); err != nil {
			return fmt.Errorf("任务已创建但调度失败: %w", err)
		}
	}
	return nil
}

// UpdateTask 更新 tasks.json 中的定时任务并重新调度。
func (s *SchedulerService) UpdateTask(cwd string, t *models.TaskManagerTask) error {
	s.unschedule(cwd, t.ID)
	if err := NewTaskStore(cwd).UpsertTask(*t); err != nil {
		return err
	}
	if t.Schedule != nil && t.Schedule.Enabled {
		if err := s.schedule(cwd, *t); err != nil {
			return fmt.Errorf("任务已更新但调度失败: %w", err)
		}
	}
	return nil
}

// RemoveTask 删除 tasks.json 中的定时任务，并删除关联会话。
func (s *SchedulerService) RemoveTask(cwd, taskID string) error {
	store := NewTaskStore(cwd)
	t, err := store.FindTask(taskID)
	if err != nil {
		return err
	}
	s.unschedule(cwd, taskID)
	if t.SessionID != "" {
		_ = s.exec.DeleteSession(context.Background(), t.SessionID)
	}
	return store.DeleteTask(taskID)
}

// RunTask 手动触发一次定时任务执行。
func (s *SchedulerService) RunTask(cwd, taskID string) error {
	store := NewTaskStore(cwd)
	if _, err := store.FindTask(taskID); err != nil {
		return err
	}
	go s.run(cwd, taskID)
	return nil
}

// run 执行一次定时任务。包含 per-task 互斥（重叠跳过）、session 准备、prompt 执行。
func (s *SchedulerService) run(cwd, taskID string) {
	store := NewTaskStore(cwd)
	t, err := store.FindTask(taskID)
	if err != nil {
		log.Printf("定时任务 %s/%s 不存在: %v", cwd, taskID, err)
		return
	}
	if t.Schedule == nil {
		log.Printf("定时任务 %s/%s 无 schedule 配置", cwd, taskID)
		return
	}

	// per-task 互斥：非阻塞，重叠则跳过
	key := taskKey(cwd, taskID)
	mu, _ := s.taskLock.LoadOrStore(key, &sync.Mutex{})
	taskMu := mu.(*sync.Mutex)
	if !taskMu.TryLock() {
		s.updateTaskStatus(cwd, taskID, func(task *models.TaskManagerTask) {
			now := time.Now()
			task.Schedule.LastRunAt = &now
			task.Schedule.LastStatus = models.TaskStatusSkipped
			task.Schedule.LastError = "上一次执行尚未结束"
		})
		s.recordSkip(cwd, taskID)
		log.Printf("定时任务 %s/%s 跳过：上一次执行尚未结束", cwd, taskID)
		return
	}
	defer taskMu.Unlock()

	s.updateTaskStatus(cwd, taskID, func(task *models.TaskManagerTask) {
		now := time.Now()
		task.Schedule.LastRunAt = &now
		task.Schedule.LastStatus = models.TaskStatusRunning
		task.Schedule.LastError = ""
	})

	timeoutMin := t.Schedule.TimeoutMinutes
	if timeoutMin <= 0 {
		timeoutMin = 5
	}

	execRecord, err := s.executeWithTimeout(cwd, t, timeoutMin)
	if err != nil {
		s.updateTaskStatus(cwd, taskID, func(task *models.TaskManagerTask) {
			task.Schedule.LastStatus = models.TaskStatusFailed
			task.Schedule.LastError = err.Error()
		})
		log.Printf("定时任务 %s/%s 执行失败: %v", cwd, taskID, err)
		return
	}
	s.updateTaskStatus(cwd, taskID, func(task *models.TaskManagerTask) {
		task.Schedule.LastStatus = models.TaskStatusSuccess
		task.Schedule.LastError = ""
	})
	if execRecord != nil {
		_ = store.AppendExecution(taskID, *execRecord)
	}
}

// recordSkip 记录一次跳过的执行（不分配 execution_id，用 0 标记）。
func (s *SchedulerService) recordSkip(cwd, taskID string) {
	rec := models.TaskExecutionRecord{
		ExecutionID: 0,
		Status:      models.TaskStatusSkipped,
		StartedAt:   time.Now(),
		Error:       "上一次执行尚未结束",
	}
	_ = NewTaskStore(cwd).AppendExecution(taskID, rec)
}

// executeWithTimeout 在指定超时内执行任务，返回执行记录用于后续状态更新。
func (s *SchedulerService) executeWithTimeout(cwd string, t *models.TaskManagerTask, timeoutMin int) (*models.TaskExecutionRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMin)*time.Minute)
	defer cancel()

	ws, err := s.wsRepo.FindByCwd(cwd)
	if err != nil {
		return nil, fmt.Errorf("查询工作区: %w", err)
	}

	session, execID, err := s.ensureSession(ctx, ws, t)
	if err != nil {
		return nil, err
	}

	// 设置模型（若任务配置了 model_value 且该会话支持模型选择）
	if err := s.applyModel(ctx, session.SessionID, t.ModelValue); err != nil {
		log.Printf("定时任务 %s/%s 设置模型 %q 失败: %v", cwd, t.ID, t.ModelValue, err)
	}

	ch, err := s.exec.PromptWithExecution(ctx, session.SessionID, t.Detail, &execID)
	if err != nil {
		return nil, fmt.Errorf("发送 prompt: %w", err)
	}

	startedAt := time.Now()
	// 同步消费消息流至结束或超时
	for range ch {
	}
	if ctx.Err() == context.DeadlineExceeded {
		return &models.TaskExecutionRecord{
			TaskID:      t.ID,
			ExecutionID: execID,
			Status:      models.TaskStatusFailed,
			StartedAt:   startedAt,
			FinishedAt:  time.Now(),
			Error:       fmt.Sprintf("执行超时（%d 分钟）", timeoutMin),
		}, fmt.Errorf("执行超时（%d 分钟）", timeoutMin)
	}

	return &models.TaskExecutionRecord{
		TaskID:      t.ID,
		ExecutionID: execID,
		Status:      models.TaskStatusSuccess,
		StartedAt:   startedAt,
		FinishedAt:  time.Now(),
	}, nil
}

// applyModel 在会话上设置模型 config option。modelValue 为空时不做任何操作。
func (s *SchedulerService) applyModel(ctx context.Context, sessionID, modelValue string) error {
	if strings.TrimSpace(modelValue) == "" {
		return nil
	}
	opts, err := s.exec.ListConfigOptions(sessionID)
	if err != nil {
		return fmt.Errorf("查询 config options: %w", err)
	}
	for _, opt := range opts {
		if opt.Select == nil || opt.Select.Category == nil {
			continue
		}
		if string(*opt.Select.Category) != "model" {
			continue
		}
		if !optionValueExists(opt, modelValue) {
			return fmt.Errorf("模型 %q 不在可用列表中", modelValue)
		}
		return s.exec.SetConfigOption(ctx, sessionID, string(opt.Select.Id), modelValue)
	}
	return fmt.Errorf("会话不支持模型选择")
}

// optionValueExists 检查 modelValue 是否在 config option 的可选项中。
func optionValueExists(opt acpsdk.SessionConfigOption, modelValue string) bool {
	if opt.Select == nil {
		return false
	}
	if opt.Select.Options.Ungrouped != nil {
		for _, o := range *opt.Select.Options.Ungrouped {
			if string(o.Value) == modelValue {
				return true
			}
		}
	}
	if opt.Select.Options.Grouped != nil {
		for _, g := range *opt.Select.Options.Grouped {
			for _, o := range g.Options {
				if string(o.Value) == modelValue {
					return true
				}
			}
		}
	}
	return false
}

// ensureSession 确保任务关联的 session 处于活跃可用状态，返回 session 与本次 execution_id。
func (s *SchedulerService) ensureSession(ctx context.Context, ws *models.Workspace, t *models.TaskManagerTask) (*models.Session, uint, error) {
	var session *models.Session
	var err error

	store := NewTaskStore(ws.Cwd)
	if t.SessionID == "" {
		// 首次执行：创建 session
		session, err = s.exec.CreateSessionWithSource(ctx, t.AgentType, ws.ID, ws.UserID, models.SessionSourceScheduled, t.ModelValue)
		if err != nil {
			return nil, 0, fmt.Errorf("创建会话: %w", err)
		}
		if err := store.UpdateTaskStatus(t.ID, func(task *models.TaskManagerTask) {
			task.SessionID = session.SessionID
			dbID := session.ID
			task.DBSessionID = &dbID
		}); err != nil {
			return nil, 0, fmt.Errorf("回填会话引用: %w", err)
		}
	} else {
		session, err = s.exec.GetSessionByDBID(*t.DBSessionID)
		if err != nil {
			return nil, 0, fmt.Errorf("查询关联会话: %w", err)
		}
		if session.Status != models.SessionStatusActive {
			oldSessionID := session.SessionID
			session, err = s.exec.ResumeSession(ctx, session.SessionID)
			if err != nil {
				return nil, 0, fmt.Errorf("恢复会话: %w", err)
			}
			if session.SessionID != oldSessionID {
				if err := store.UpdateTaskStatus(t.ID, func(task *models.TaskManagerTask) {
					task.SessionID = session.SessionID
					dbID := session.ID
					task.DBSessionID = &dbID
				}); err != nil {
					return nil, 0, fmt.Errorf("同步会话引用: %w", err)
				}
			}
		}
	}

	execs, err := s.exec.ListExecutions(session.SessionID)
	if err != nil {
		return nil, 0, fmt.Errorf("查询执行历史: %w", err)
	}
	var execID uint = 1
	if len(execs) > 0 {
		execID = execs[0].ExecutionID + 1
	}
	return session, execID, nil
}

// updateTaskStatus 更新指定定时任务的运行时字段。
func (s *SchedulerService) updateTaskStatus(cwd, taskID string, mutate func(*models.TaskManagerTask)) {
	if err := NewTaskStore(cwd).UpdateTaskStatus(taskID, mutate); err != nil {
		log.Printf("更新定时任务 %s/%s 状态失败: %v", cwd, taskID, err)
	}
}
