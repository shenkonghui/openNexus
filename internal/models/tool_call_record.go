package models

import "time"

// 工具调用状态（对齐 ACP ToolCallStatus）。
const (
	ToolCallStatusPending    = "pending"
	ToolCallStatusInProgress = "in_progress"
	ToolCallStatusCompleted  = "completed"
	ToolCallStatusFailed     = "failed"
)

// ToolCallRecord 记录一次 agent 工具调用的历史，供「工具调用记录」页面查询。
// 数据来源两路：
//   - ACP prompt 流的 tool_call / tool_call_update：调用创建、状态流转、rawOutput 中的退出码；
//   - TerminalBridge（终端能力委托给客户端的 agent）：命令实际退出码兜底回填。
//
// shell 类调用（kind=execute）额外记录 Command / Cwd / ExitCode。
type ToolCallRecord struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	UserID      uint   `gorm:"index;not null" json:"user_id"`
	DBSessionID uint   `gorm:"index;not null" json:"db_session_id"`
	// ToolCallID 是 ACP toolCallId；TerminalBridge 兜底创建的记录用 "term:<terminalId>"。
	ToolCallID string `gorm:"size:512;index" json:"tool_call_id"`
	// Kind 是 ACP ToolKind（execute/read/edit/search/fetch/...）。
	Kind  string `gorm:"size:32;index" json:"kind"`
	Title string `gorm:"type:text" json:"title"`
	// Command 是 shell 调用的完整命令（来自 rawInput.command 或终端桥接的命令行）。
	Command string `gorm:"type:text" json:"command"`
	// Cwd 是命令执行目录（rawInput 未提供时回退到会话工作目录）。
	Cwd string `gorm:"size:1024" json:"cwd"`
	// TerminalID 关联 TerminalBridge 终端（tool_call_update 内嵌 terminal content 时写入），
	// 终端退出时按此回填 ExitCode。
	TerminalID string `gorm:"size:64;index" json:"terminal_id,omitempty"`
	// ExitCode 是 shell 命令退出码（rawOutput.exitCode 或终端桥接退出事件）。
	ExitCode *int   `json:"exit_code"`
	Status   string `gorm:"size:32;not null;default:pending" json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
