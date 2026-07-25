package models

import (
	"encoding/json"
	"strings"
	"time"
)

// 任务编排（Orchestration）状态机常量。
const (
	OrchTaskStatusPending   = "pending"   // 已定义，尚未加入队列
	OrchTaskStatusQueued    = "queued"    // 已入队，等待执行槽位
	OrchTaskStatusRunning   = "running"   // 正在执行（agent 产出消息中）
	OrchTaskStatusDone      = "done"      // 正常完成
	OrchTaskStatusFailed    = "failed"    // 执行失败
	OrchTaskStatusCanceled  = "canceled"  // 用户手动停止
	OrchTaskStatusInterrupt = "interrupt" // 服务重启时标记的中断态
)

// 编排任务优先级：p0 最高，p2 最低；缺省 p1。
const (
	OrchTaskPriorityP0 = "p0"
	OrchTaskPriorityP1 = "p1"
	OrchTaskPriorityP2 = "p2"
)

// NormalizeOrchTaskPriority 将优先级归一为 p0/p1/p2。
// 兼容空值、大小写、数字字面量（0/1/2）与 P0/P1/P2；无法识别时回退 p1。
func NormalizeOrchTaskPriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case OrchTaskPriorityP0, "0":
		return OrchTaskPriorityP0
	case OrchTaskPriorityP2, "2":
		return OrchTaskPriorityP2
	default:
		return OrchTaskPriorityP1
	}
}

// OrchTaskPriorityRank 返回调度排序权重（越小越优先）。
func OrchTaskPriorityRank(priority string) int {
	switch NormalizeOrchTaskPriority(priority) {
	case OrchTaskPriorityP0:
		return 0
	case OrchTaskPriorityP2:
		return 2
	default:
		return 1
	}
}

// IsOrchTaskRunning 报告该状态是否属于"占用执行资源"的活跃态。
func IsOrchTaskRunning(status string) bool {
	switch status {
	case OrchTaskStatusQueued, OrchTaskStatusRunning:
		return true
	default:
		return false
	}
}

// NormalizeOrchTaskStatus 将任务状态归一化为合法枚举值。
// 兼容 AI 手写 tasks.json 时常见的别名（如 completed/succeeded→done，cancelled→canceled）；
// 空值视为 pending；无法识别的值原样返回（前端会回退显示原值）。
func NormalizeOrchTaskStatus(status string) string {
	switch status {
	case "":
		return OrchTaskStatusPending
	case "completed", "succeeded", "success":
		return OrchTaskStatusDone
	case "cancelled":
		return OrchTaskStatusCanceled
	default:
		return status
	}
}

// OrchestrationTask 描述单个编排任务的定义与运行时状态。
// 持久化于工作区 cwd 下的 tasks.json（见 OrchestrationDef）。
type OrchestrationTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Detail 即任务详情，作为 prompt 发送给 agent。
	Detail     string `json:"detail"`
	AgentType  string `json:"agent_type"`
	ModelValue string `json:"model_value,omitempty"`
	// Priority 任务优先级：p0 / p1 / p2，缺省 p1。
	Priority string `json:"priority,omitempty"`

	// 运行时字段——执行后写回 tasks.json
	SessionID    string     `json:"session_id,omitempty"`    // 落库的稳定 session UUID
	DBSessionID  *uint      `json:"db_session_id,omitempty"` // 关联 DB Session 主键
	Status       string     `json:"status"`                  // 见 OrchTaskStatus* 常量
	Branch       string     `json:"branch,omitempty"`        // worktree 对应分支
	WorktreePath string     `json:"worktree_path,omitempty"` // worktree 绝对路径
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Error        string     `json:"error,omitempty"`

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
func (t *OrchestrationTask) UnmarshalJSON(data []byte) error {
	type alias OrchestrationTask
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
		t.Priority = NormalizeOrchTaskPriority(pri)
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

// OrchestrationDef 是 tasks.json 的顶层结构。
type OrchestrationDef struct {
	MaxParallel int                 `json:"max_parallel"` // 并发上限，<=0 视为串行(=1)
	Tasks       []OrchestrationTask `json:"tasks"`
	// ParentSessionID 是编排管理会话的 DB 主键。编排任务执行时创建的会话作为其子会话
	// （通过 Session.ParentSessionID 关联），形成上下级关系。由前端在创建编排管理会话后登记。
	ParentSessionID *uint `json:"parent_session_id,omitempty"`
}

// DefaultMaxParallel 是新建编排时的默认并发上限。
const DefaultMaxParallel = 3
