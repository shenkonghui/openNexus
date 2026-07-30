package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opennexus/internal/models"
	"opennexus/internal/workspacemeta"
)

const (
	tasksFileName               = "tasks.json"
	archivedTasksFileName       = "archived-tasks.json"
	scheduledExecutionsFileName = "scheduled-executions.jsonl"
	scheduledExecutionsDir      = ".openNexus"
	maxInlinedExecutions        = 10
)

// TaskStore 是工作区 tasks.json 的文件型存储，供编排器与调度器共享。
// 数据落在工作区的管理数据目录（workspacemeta.DirFor(cwd)）下，与 agent 工作目录分离；
// 首次访问时自动把旧版落在 cwd 内的数据搬迁过去。
// 所有写操作通过 mutex 序列化，避免同一进程内并发改写；文件级并发由调用方保证。
type TaskStore struct {
	cwd         string // agent 工作目录（旧版数据位置，仅用于迁移）
	dir         string // 管理数据目录；root 未设置时等于 cwd（旧行为）
	mu          sync.Mutex
	migrateOnce sync.Once
}

// NewTaskStore 创建基于 cwd 的 TaskStore，数据实际落在对应的管理数据目录。
func NewTaskStore(cwd string) *TaskStore {
	return &TaskStore{cwd: cwd, dir: workspacemeta.DirFor(cwd)}
}

func (s *TaskStore) tasksPath() string {
	return filepath.Join(s.dir, tasksFileName)
}

func (s *TaskStore) archivedPath() string {
	return filepath.Join(s.dir, archivedTasksFileName)
}

func (s *TaskStore) executionsPath() string {
	if s.dir == s.cwd {
		// 未启用管理数据目录：保持旧路径 <cwd>/.openNexus/scheduled-executions.jsonl
		return filepath.Join(s.dir, scheduledExecutionsDir, scheduledExecutionsFileName)
	}
	return filepath.Join(s.dir, scheduledExecutionsFileName)
}

// ensureMigrated 确保管理数据目录存在，并把旧版落在 cwd 内的 tasks.json、
// 执行记录 JSONL 搬迁过去（仅首次，新位置已有文件时不覆盖）。
// 迁移失败仅告警，不阻塞后续读写（新位置按空定义处理）。
func (s *TaskStore) ensureMigrated() {
	s.migrateOnce.Do(func() {
		if s.dir == s.cwd || s.cwd == "" {
			return
		}
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			slog.Warn("创建工作区管理数据目录失败", "dir", s.dir, "err", err)
			return
		}
		migrateFile(filepath.Join(s.cwd, tasksFileName), s.tasksPath())
		migrateFile(
			filepath.Join(s.cwd, scheduledExecutionsDir, scheduledExecutionsFileName),
			s.executionsPath(),
		)
	})
}

// migrateFile 把旧位置文件搬到新位置：新位置已存在或旧位置不存在时无操作；
// 优先 rename，跨设备时降级为 copy+remove。
func migrateFile(src, dst string) {
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if _, err := os.Stat(src); err != nil {
		return
	}
	if err := os.Rename(src, dst); err == nil {
		slog.Info("已迁移工作区管理数据", "from", src, "to", dst)
		return
	}
	if err := copyFile(src, dst); err != nil {
		slog.Warn("迁移工作区管理数据失败", "from", src, "to", dst, "err", err)
		return
	}
	if err := os.Remove(src); err != nil {
		slog.Warn("迁移后删除旧文件失败", "path", src, "err", err)
	}
	slog.Info("已迁移工作区管理数据", "from", src, "to", dst)
}

// copyFile 复制文件内容（rename 跨设备失败时的降级路径）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Load 读取 tasks.json；不存在时返回空定义。
func (s *TaskStore) Load() (*models.TaskManagerDef, error) {
	s.ensureMigrated()
	data, err := os.ReadFile(s.tasksPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &models.TaskManagerDef{MaxParallel: models.DefaultMaxParallel, Tasks: []models.TaskManagerTask{}}, nil
		}
		return nil, fmt.Errorf("读取 tasks.json: %w", err)
	}
	var def models.TaskManagerDef
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("解析 tasks.json: %w", err)
	}
	if def.MaxParallel <= 0 {
		def.MaxParallel = 1
	}
	if def.Tasks == nil {
		def.Tasks = []models.TaskManagerTask{}
	}
	for i := range def.Tasks {
		t := &def.Tasks[i]
		t.Status = models.NormalizeTaskStatus(t.Status)
		t.Priority = models.NormalizeTaskPriority(t.Priority)
	}
	return &def, nil
}

// Save 原子写回 tasks.json，成功后广播变更事件（驱动前端自动刷新）。
func (s *TaskStore) Save(def *models.TaskManagerDef) error {
	s.ensureMigrated()
	if def == nil {
		def = &models.TaskManagerDef{}
	}
	if def.MaxParallel <= 0 {
		def.MaxParallel = 1
	}
	if def.Tasks == nil {
		def.Tasks = []models.TaskManagerTask{}
	}
	for i := range def.Tasks {
		def.Tasks[i].Priority = models.NormalizeTaskPriority(def.Tasks[i].Priority)
	}
	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 tasks.json: %w", err)
	}
	tmp := s.tasksPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入 tasks.json: %w", err)
	}
	if err := os.Rename(tmp, s.tasksPath()); err != nil {
		return err
	}
	// 所有写路径（REST/MCP/编排器/调度器）都汇聚到此处落盘，统一在这里通知订阅者。
	notifyTaskChanged(s.cwd)
	return nil
}

// UpsertTask 新增或按 id 更新任务，保留运行时字段。
func (s *TaskStore) UpsertTask(task models.TaskManagerTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	def, err := s.Load()
	if err != nil {
		return err
	}
	if task.Status == "" {
		task.Status = models.TaskStatusPending
	}
	incomingPri := strings.TrimSpace(task.Priority)
	found := false
	for i := range def.Tasks {
		if def.Tasks[i].ID == task.ID {
			cur := def.Tasks[i]
			task.SessionID = cur.SessionID
			task.DBSessionID = cur.DBSessionID
			task.Status = cur.Status
			task.WorktreePath = cur.WorktreePath
			task.StartedAt = cur.StartedAt
			task.FinishedAt = cur.FinishedAt
			task.Error = cur.Error
			task.Executions = cur.Executions
			task.Branch = ifEmpty(task.Branch, cur.Branch)
			task.GoalCondition = ifEmpty(task.GoalCondition, cur.GoalCondition)
			task.Schedule = ifNilSchedule(task.Schedule, cur.Schedule)
			if incomingPri == "" {
				task.Priority = cur.Priority
			} else {
				task.Priority = models.NormalizeTaskPriority(incomingPri)
			}
			def.Tasks[i] = task
			found = true
			break
		}
	}
	if !found {
		task.Priority = models.NormalizeTaskPriority(incomingPri)
		def.Tasks = append(def.Tasks, task)
	}
	return s.Save(def)
}

// DeleteTask 删除指定任务。
func (s *TaskStore) DeleteTask(taskID string) error {
	s.mu.Lock()
	def, err := s.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	idx := -1
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("任务 %s 不存在", taskID)
	}
	def.Tasks = append(def.Tasks[:idx], def.Tasks[idx+1:]...)
	err = s.Save(def)
	s.mu.Unlock()
	return err
}

// ==================== 归档（回收站） ====================

// loadArchived 读取 archived-tasks.json；不存在时返回空列表。
func (s *TaskStore) loadArchived() ([]models.ArchivedTask, error) {
	s.ensureMigrated()
	data, err := os.ReadFile(s.archivedPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []models.ArchivedTask{}, nil
		}
		return nil, fmt.Errorf("读取 archived-tasks.json: %w", err)
	}
	var list []models.ArchivedTask
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("解析 archived-tasks.json: %w", err)
	}
	// 归一化历史脏数据：旧版归档会原样快照 running/queued 状态，导致回收站
	// 永远显示“执行中”；归档后任务必然已停止，读取时修正为 canceled。
	for i := range list {
		if models.IsTaskRunning(list[i].Status) {
			list[i].Status = models.TaskStatusCanceled
			if list[i].FinishedAt == nil {
				at := list[i].ArchivedAt
				list[i].FinishedAt = &at
			}
			if list[i].Error == "" {
				list[i].Error = "归档时停止"
			}
		}
	}
	return list, nil
}

// saveArchived 原子写回 archived-tasks.json，并广播变更事件（驱动回收站/任务页自动刷新）。
func (s *TaskStore) saveArchived(list []models.ArchivedTask) error {
	s.ensureMigrated()
	if list == nil {
		list = []models.ArchivedTask{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 archived-tasks.json: %w", err)
	}
	tmp := s.archivedPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入 archived-tasks.json: %w", err)
	}
	if err := os.Rename(tmp, s.archivedPath()); err != nil {
		return err
	}
	notifyTaskChanged(s.cwd)
	return nil
}

// ArchiveTask 把指定任务从 tasks.json 移入归档文件（记录归档时间），返回归档条目。
func (s *TaskStore) ArchiveTask(taskID string) (*models.ArchivedTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, err := s.Load()
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("任务 %s 不存在", taskID)
	}
	archived, err := s.loadArchived()
	if err != nil {
		return nil, err
	}
	entry := models.ArchivedTask{TaskManagerTask: def.Tasks[idx], ArchivedAt: time.Now()}
	// 同 id 旧归档条目直接覆盖（重复归档以最新为准）
	kept := archived[:0]
	for _, a := range archived {
		if a.ID != taskID {
			kept = append(kept, a)
		}
	}
	kept = append(kept, entry)
	if err := s.saveArchived(kept); err != nil {
		return nil, err
	}
	def.Tasks = append(def.Tasks[:idx], def.Tasks[idx+1:]...)
	if err := s.Save(def); err != nil {
		return nil, err
	}
	return &entry, nil
}

// ListArchived 返回全部归档条目（按归档时间倒序）。
func (s *TaskStore) ListArchived() ([]models.ArchivedTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadArchived()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].ArchivedAt.After(list[j].ArchivedAt)
	})
	return list, nil
}

// RestoreArchived 把归档条目移回 tasks.json（同 id 任务已存在时报错）。
func (s *TaskStore) RestoreArchived(taskID string) (*models.TaskManagerTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadArchived()
	if err != nil {
		return nil, err
	}
	idx := -1
	for i := range list {
		if list[i].ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("归档任务 %s 不存在", taskID)
	}
	def, err := s.Load()
	if err != nil {
		return nil, err
	}
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			return nil, fmt.Errorf("任务 %s 已存在，无法恢复", taskID)
		}
	}
	task := list[idx].TaskManagerTask
	def.Tasks = append(def.Tasks, task)
	if err := s.Save(def); err != nil {
		return nil, err
	}
	list = append(list[:idx], list[idx+1:]...)
	if err := s.saveArchived(list); err != nil {
		return nil, err
	}
	return &task, nil
}

// RemoveArchived 从归档文件中彻底移除指定条目，返回被移除的条目（供调用方清理 worktree/会话）。
func (s *TaskStore) RemoveArchived(taskID string) (*models.ArchivedTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadArchived()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == taskID {
			entry := list[i]
			list = append(list[:i], list[i+1:]...)
			if err := s.saveArchived(list); err != nil {
				return nil, err
			}
			return &entry, nil
		}
	}
	return nil, fmt.Errorf("归档任务 %s 不存在", taskID)
}

// PurgeArchivedBefore 移除并返回归档时间早于 cutoff 的条目（供调用方清理 worktree/会话）。
func (s *TaskStore) PurgeArchivedBefore(cutoff time.Time) ([]models.ArchivedTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.loadArchived()
	if err != nil {
		return nil, err
	}
	var expired []models.ArchivedTask
	kept := list[:0]
	for _, a := range list {
		if a.ArchivedAt.Before(cutoff) {
			expired = append(expired, a)
		} else {
			kept = append(kept, a)
		}
	}
	if len(expired) == 0 {
		return nil, nil
	}
	if err := s.saveArchived(kept); err != nil {
		return nil, err
	}
	return expired, nil
}

// FindTask 按 id 查询任务。
func (s *TaskStore) FindTask(taskID string) (*models.TaskManagerTask, error) {
	def, err := s.Load()
	if err != nil {
		return nil, err
	}
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			return &def.Tasks[i], nil
		}
	}
	return nil, fmt.Errorf("任务 %s 不存在", taskID)
}

// ListScheduledTasks 返回本工作区全部定时任务（Schedule 非 nil）。
func (s *TaskStore) ListScheduledTasks() ([]models.TaskManagerTask, error) {
	def, err := s.Load()
	if err != nil {
		return nil, err
	}
	var out []models.TaskManagerTask
	for _, t := range def.Tasks {
		if t.Schedule != nil {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// SetMaxParallel 更新并发上限。
func (s *TaskStore) SetMaxParallel(maxParallel int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, err := s.Load()
	if err != nil {
		return err
	}
	if maxParallel <= 0 {
		maxParallel = 1
	}
	def.MaxParallel = maxParallel
	return s.Save(def)
}

// UpdateTaskStatus 更新指定任务的运行时字段与最近执行记录。
func (s *TaskStore) UpdateTaskStatus(taskID string, mutate func(*models.TaskManagerTask)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, err := s.Load()
	if err != nil {
		return err
	}
	for i := range def.Tasks {
		if def.Tasks[i].ID == taskID {
			mutate(&def.Tasks[i])
			return s.Save(def)
		}
	}
	return fmt.Errorf("任务 %s 不存在", taskID)
}

// AppendExecution 追加一条执行记录到 JSONL，并更新 tasks.json 中的 executions 快照。
func (s *TaskStore) AppendExecution(taskID string, rec models.TaskExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec.TaskID = taskID
	def, err := s.Load()
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

	// 写 JSONL
	if err := s.appendExecutionJSONLLocked(rec); err != nil {
		return err
	}

	// 更新快照
	task.Executions = append(task.Executions, rec)
	if len(task.Executions) > maxInlinedExecutions {
		task.Executions = task.Executions[len(task.Executions)-maxInlinedExecutions:]
	}
	return s.Save(def)
}

func (s *TaskStore) appendExecutionJSONLLocked(rec models.TaskExecutionRecord) error {
	s.ensureMigrated()
	p := s.executionsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("创建执行记录目录: %w", err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// ListExecutions 读取本工作区全部执行记录（可指定 taskID 过滤）。
func (s *TaskStore) ListExecutions(taskID string) ([]models.TaskExecutionRecord, error) {
	s.ensureMigrated()
	p := s.executionsPath()
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []models.TaskExecutionRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec models.TaskExecutionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if taskID != "" && rec.TaskID != taskID {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// FindTaskInCwds 在所有指定 cwd 的 tasks.json 中按 id 查找任务。
func FindTaskInCwds(taskID string, cwds []string) (string, *models.TaskManagerTask, error) {
	for _, cwd := range cwds {
		def, err := NewTaskStore(cwd).Load()
		if err != nil {
			continue
		}
		for i := range def.Tasks {
			if def.Tasks[i].ID == taskID {
				return cwd, &def.Tasks[i], nil
			}
		}
	}
	return "", nil, fmt.Errorf("任务 %s 不存在", taskID)
}

// ListAllScheduledTasks 扫描所有指定 cwd 的 tasks.json，返回每个 cwd 下的定时任务。
func ListAllScheduledTasks(cwds []string) (map[string][]models.TaskManagerTask, error) {
	out := make(map[string][]models.TaskManagerTask)
	for _, cwd := range cwds {
		tasks, err := NewTaskStore(cwd).ListScheduledTasks()
		if err != nil {
			continue
		}
		if len(tasks) > 0 {
			out[cwd] = tasks
		}
	}
	return out, nil
}

func ifEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ifNilSchedule(a, b *models.TaskSchedule) *models.TaskSchedule {
	if a != nil {
		return a
	}
	return b
}
