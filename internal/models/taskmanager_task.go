package models

import (
	"encoding/json"
	"strings"
	"time"
)

// 任务管理（TaskManager）状态机常量。
const (
	TaskStatusPending   = "pending"   // 已定义，尚未加入队列
	TaskStatusQueued    = "queued"    // 已入队，等待执行槽位
	TaskStatusRunning   = "running"   // 正在执行（agent 产出消息中）
	TaskStatusDone      = "done"      // 正常完成
	TaskStatusFailed    = "failed"    // 执行失败
	TaskStatusCanceled  = "canceled"  // 用户手动停止
	TaskStatusInterrupt = "interrupt" // 服务重启时标记的中断态
)

// 任务优先级：p0 最高，p2 最低；缺省 p1。
const (
	TaskPriorityP0 = "p0"
	TaskPriorityP1 = "p1"
	TaskPriorityP2 = "p2"
)

// NormalizeTaskPriority 将优先级归一为 p0/p1/p2。
// 兼容空值、大小写、数字字面量（0/1/2）与 P0/P1/P2；无法识别时回退 p1。
func NormalizeTaskPriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case TaskPriorityP0, "0":
		return TaskPriorityP0
	case TaskPriorityP2, "2":
		return TaskPriorityP2
	default:
		return TaskPriorityP1
	}
}

// TaskPriorityRank 返回调度排序权重（越小越优先）。
func TaskPriorityRank(priority string) int {
	switch NormalizeTaskPriority(priority) {
	case TaskPriorityP0:
		return 0
	case TaskPriorityP2:
		return 2
	default:
		return 1
	}
}

// IsTaskRunning 报告该状态是否属于"占用执行资源"的活跃态。
func IsTaskRunning(status string) bool {
	switch status {
	case TaskStatusQueued, TaskStatusRunning:
		return true
	default:
		return false
	}
}

// NormalizeTaskStatus 将任务状态归一化为合法枚举值。
// 兼容 AI 手写 tasks.json 时常见的别名（如 completed/succeeded→done，cancelled→canceled）；
// 空值视为 pending；无法识别的值原样返回（前端会回退显示原值）。
func NormalizeTaskStatus(status string) string {
	switch status {
	case "":
		return TaskStatusPending
	case "completed", "succeeded", "success":
		return TaskStatusDone
	case "cancelled":
		return TaskStatusCanceled
	default:
		return status
	}
}

// goal 在任务上的展示状态（写入 tasks.json 的 goal 字段，供任务列表展示）。
const (
	TaskGoalStatusActive     = "active"     // goal 生效中（agent 工作/自动续轮）
	TaskGoalStatusEvaluating = "evaluating" // 正在评估是否达成
	TaskGoalStatusAchieved   = "achieved"   // 已达成
	TaskGoalStatusStopped    = "stopped"    // 已终止（超限/评估失败/续轮失败）
)

// TaskGoalState 是会话 goal 循环在任务条目上的状态快照。
// goal 本身是会话内存态（服务重启即清空）；每次状态变化经 GoalStateNotifier
// 同步到 tasks.json，使任务列表能展示 goal 进展；nil 表示该任务无 goal。
type TaskGoalState struct {
	Condition  string    `json:"condition"`
	Status     string    `json:"status"`               // 见 TaskGoalStatus* 常量
	Turns      int       `json:"turns"`                // 已自动续轮次数
	EvalCount  int       `json:"eval_count,omitempty"` // 已执行的达成审计（会签评估）次数
	LastReason string    `json:"last_reason,omitempty"`
	Roles      []string  `json:"roles,omitempty"` // 自动选取的评估角色名
	UpdatedAt  time.Time `json:"updated_at"`
}

// TaskSchedule 是定时任务调度配置；nil 表示非定时任务。
// 统一进 tasks.json 后，ScheduledTask 与 TaskManagerTask 共享同一套结构。
type TaskSchedule struct {
	CronExpr       string     `json:"cron_expr"`
	Enabled        bool       `json:"enabled"`
	TimeoutMinutes int        `json:"timeout_minutes"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastStatus     string     `json:"last_status,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
}

// TaskExecutionRecord 是一次任务执行的元数据，append-only 写入 executions JSONL。
// 嵌入 tasks.json 仅保留最近若干条快照；完整历史建议存 JSONL。
type TaskExecutionRecord struct {
	TaskID      string    `json:"task_id"`
	ExecutionID uint      `json:"execution_id"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// TaskManagerTask 描述单个任务的定义与运行时状态。
// 持久化于工作区 cwd 下的 tasks.json（见 TaskManagerDef）。
// 当 Schedule 非 nil 时，该任务为定时任务，由 SchedulerService 调度；
// 否则为任务管理/手动任务，由 TaskManagerService 调度或用户手动触发。
type TaskManagerTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Detail 即任务详情，作为 prompt 发送给 agent。
	Detail     string `json:"detail"`
	AgentType  string `json:"agent_type"`
	ModelValue string `json:"model_value,omitempty"`
	// Priority 任务优先级：p0 / p1 / p2，缺省 p1。
	Priority string `json:"priority,omitempty"`
	// GoalCondition 是任务定义的 goal 完成条件（输入侧）；非空时任务运行自动开启 goal 模式，
	// 每轮结束自动审计，goal 达成后任务才置为完成（运行时快照见 Goal 字段）。
	GoalCondition string `json:"goal_condition,omitempty"`
	// NoWorktree 为 true 时不创建独立 git worktree，直接在工作区目录运行（缺省 false=worktree 隔离）。
	NoWorktree bool `json:"no_worktree,omitempty"`

	// Schedule 非 nil 时表示定时任务；任务管理/手动任务为 nil。
	Schedule *TaskSchedule `json:"schedule,omitempty"`
	// Executions 保留最近若干次执行记录，用于 UI 快速展示。
	// 完整历史存于 {cwd}/.opennexus/scheduled-executions.jsonl。
	Executions []TaskExecutionRecord `json:"executions,omitempty"`

	// 运行时字段——执行后写回 tasks.json
	SessionID    string     `json:"session_id,omitempty"`    // 落库的稳定 session UUID
	DBSessionID  *uint      `json:"db_session_id,omitempty"` // 关联 DB Session 主键
	Status       string     `json:"status"`                  // 见 TaskStatus* 常量
	Branch       string     `json:"branch,omitempty"`        // worktree 对应分支
	WorktreePath string     `json:"worktree_path,omitempty"` // worktree 绝对路径
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Error        string     `json:"error,omitempty"`
	// Goal 是会话 goal 循环的状态快照（GoalStateNotifier 同步写入）；nil 表示无 goal。
	Goal *TaskGoalState `json:"goal,omitempty"`

	// 可扩展：任务间依赖（v1 仅做数据层，引擎按并发上限调度）。
	DependsOn []string `json:"depends_on,omitempty"`
}

// jsonScalarToString 将 JSON 标量原始字节转为字符串：带引号的按字符串反序列化，
// 数字/布尔等字面量原样返回。用于兼容 AI 手写 tasks.json 时把 id 写成数字的情况。
func jsonScalarToString(raw json.RawMessage) (string, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return "", err
		}
		return str, nil
	}
	return s, nil
}

// UnmarshalJSON 兼容 AI 手写 tasks.json 时把 id / depends_on / priority 写成数字（而非字符串）的情况，
// 将数字字面量原样转成字符串，避免「cannot unmarshal number into Go struct field ... of type string」。
func (t *TaskManagerTask) UnmarshalJSON(data []byte) error {
	type alias TaskManagerTask
	aux := &struct {
		ID        json.RawMessage   `json:"id"`
		Priority  json.RawMessage   `json:"priority"`
		DependsOn []json.RawMessage `json:"depends_on"`
		*alias
	}{alias: (*alias)(t)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	id, err := jsonScalarToString(aux.ID)
	if err != nil {
		return err
	}
	t.ID = id
	if len(aux.Priority) > 0 {
		pri, err := jsonScalarToString(aux.Priority)
		if err != nil {
			return err
		}
		t.Priority = NormalizeTaskPriority(pri)
	}
	if aux.DependsOn != nil {
		t.DependsOn = make([]string, 0, len(aux.DependsOn))
		for _, raw := range aux.DependsOn {
			dep, err := jsonScalarToString(raw)
			if err != nil {
				return err
			}
			t.DependsOn = append(t.DependsOn, dep)
		}
	}
	return nil
}

// TaskManagerDef 是 tasks.json 的顶层结构。
type TaskManagerDef struct {
	MaxParallel int               `json:"max_parallel"` // 并发上限，<=0 视为串行(=1)
	Tasks       []TaskManagerTask `json:"tasks"`
}

// DefaultMaxParallel 是新建任务管理时的默认并发上限。
const DefaultMaxParallel = 3

// ArchivedTask 是归档到回收站的任务条目：保留任务全部字段（含 worktree/会话引用，
// 供恢复时原样放回 tasks.json），并记录归档时间用于过期清理。
type ArchivedTask struct {
	TaskManagerTask
	ArchivedAt time.Time `json:"archived_at"`
}

// UnmarshalJSON 显式覆盖内嵌 TaskManagerTask 提升上来的同名方法：
// 否则反序列化时只走 TaskManagerTask.UnmarshalJSON，archived_at 被丢弃成零值，
// 归档条目会被过期清理误判为已过期而立即删除。
func (a *ArchivedTask) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &a.TaskManagerTask); err != nil {
		return err
	}
	aux := struct {
		ArchivedAt time.Time `json:"archived_at"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	a.ArchivedAt = aux.ArchivedAt
	return nil
}

// DefaultArchiveRetentionDays 是归档任务在回收站的默认保留天数，过期后彻底删除。
const DefaultArchiveRetentionDays = 3
