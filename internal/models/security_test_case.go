package models

import "time"

// SecurityTestCase 是用户自定义的沙箱效果测试用例。
// 向 agent 发送预设的命令 prompt，在全局沙箱开启的前提下让命令真正执行，
// 通过工具调用的退出码与后端文件系统探测来验证沙箱是否有效隔离了危险操作。
// 用例按 user 维度管理（列表），可增删改与启用/禁用。
type SecurityTestCase struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"index;not null" json:"user_id"`
	// Name 用例显示名（如"写系统目录"）。
	Name string `gorm:"size:128" json:"name"`
	// Category 沙箱边界类别：fs_write_outside / fs_write_inside / fs_delete / fs_system / fs_sensitive / fs_home。
	Category string `gorm:"size:32" json:"category"`
	// Prompt 发送给 agent 的命令 prompt 正文（包含要执行的具体命令）。
	Prompt string `gorm:"type:text" json:"prompt"`
	// Enabled 是否启用（禁用的用例不参与测试）。
	Enabled bool `gorm:"not null;default:true" json:"enabled"`
	// SortOrder 列表排序（越小越靠前）。
	SortOrder int `gorm:"not null;default:0" json:"sort_order"`
	// ExpectBlocked 期望沙箱行为：true=沙箱应阻止该操作（命令应失败）；
	// false=沙箱应放行该操作（命令应成功，如工作目录内写入）。默认全部 true。
	ExpectBlocked bool      `gorm:"not null;default:true" json:"expect_blocked"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// 沙箱效果测试用例的边界类别常量。
const (
	SecTestFSWriteOutside = "fs_write_outside" // 写工作目录外：沙箱应阻止
	SecTestFSWriteInside  = "fs_write_inside"  // 写工作目录内：沙箱应放行
	SecTestFSDelete       = "fs_delete"        // 删除工作目录外文件：沙箱应阻止
	SecTestFSSystem       = "fs_system"        // 写系统目录(/etc 等)：沙箱应阻止
	SecTestFSSensitive    = "fs_sensitive"     // 写敏感路径(~/.ssh 等)：沙箱应阻止
	SecTestFSHome         = "fs_home"          // 写主目录非工作区：沙箱应阻止
)
