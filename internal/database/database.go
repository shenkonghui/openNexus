package database

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"opennexus/internal/models"
)

// tuneDSN 为文件型 SQLite DSN 追加连接级 PRAGMA，显著降低写延迟与读写锁竞争：
//   - _journal_mode=WAL：写不再阻塞读、读不再阻塞写，避免流式持久化被 5s 轮询读卡住
//   - _busy_timeout=5000：锁竞争时最多等待 5s 而非立即返回 SQLITE_BUSY
//   - _synchronous=NORMAL：WAL 下安全且比 FULL 少一次 fsync，写更快
//   - _txlock=immediate：写事务开始即取写锁，避免升级死锁
//   - _foreign_keys=on：保持外键约束启用
//
// 内存库（测试用 :memory: / mode=memory）不支持 WAL，原样返回。
func tuneDSN(dsn string) string {
	if strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory") {
		return dsn
	}
	pragmas := "_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_txlock=immediate&_foreign_keys=on"
	if strings.Contains(dsn, "?") {
		return dsn + "&" + pragmas
	}
	return dsn + "?" + pragmas
}

func Connect(dsn string, defaultCwd string) (*gorm.DB, error) {
	// IgnoreRecordNotFoundError=true：terminalBridge 按 agent_session_id 反查会话路由
	// terminal 事件，查不到（临时探测会话 / 旧 session 已被新 ID 替换）是正常分支，
	// 不应刷 GORM 的 record not found 红色日志。First 仍返回 error，调用方按需处理。
	// LogLevel=Warn：仅打印慢查询与真实错误，屏蔽 INFO 级 SQL 回显。
	gormLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)
	db, err := gorm.Open(sqlite.Open(tuneDSN(dsn)), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.RefreshToken{}, &models.Session{}, &models.AgentConfig{}, &models.Workspace{}, &models.Note{}, &models.NoteSettings{}, &models.RunningTask{}, &models.TaskSettings{}, &models.GoalSettings{}, &models.UserAgentPrefs{}, &models.ACPConnection{}, &models.ToolCallRecord{}, &models.SecurityTestCase{}); err != nil {
		return nil, fmt.Errorf("迁移数据库: %w", err)
	}
	// 数据迁移：为旧 Session 创建对应 Workspace，填充 workspace_id
	if err := migrateOldSessionsToWorkspaces(db, defaultCwd); err != nil {
		return nil, fmt.Errorf("迁移旧会话数据: %w", err)
	}
	// 数据迁移：旧 session_id 曾是 ACP id，回填到 agent_session_id
	if err := migrateAgentSessionID(db); err != nil {
		return nil, fmt.Errorf("迁移 agent_session_id: %w", err)
	}
	// 数据迁移：项目改名（.nextAgent → .openNexus）后，修正 workspaces/sessions 中残留的旧路径。
	// 文件系统已由 config.MigrateLegacyDataDir 整体迁走，但 DB 里的 cwd/temp_dir 仍是旧路径，
	// 导致 persistent 工作区报"工作目录不存在"。此步幂等：无匹配行时无操作。
	if err := migrateLegacyWorkspacePaths(db); err != nil {
		return nil, fmt.Errorf("迁移历史工作区路径: %w", err)
	}
	// 数据迁移：把旧的"默认工作区"（temporary + 随机临时目录）统一改为 persistent + 固定 defaultCwd。
	// 幂等：已是 persistent 的记录不会被 mode='temporary' 命中。
	if err := migrateDefaultWorkspacesToPersistent(db, defaultCwd); err != nil {
		return nil, fmt.Errorf("迁移默认工作区: %w", err)
	}
	return db, nil
}

// migrateLegacyWorkspacePaths 把 workspaces/sessions 表里残留的 .nextAgent / .nexusagent
// 路径前缀改写为 .openNexus。配合 config.MigrateLegacyDataDir 的文件系统迁移，
// 保证改名前的旧会话/工作区在新路径下仍可用。多次执行结果一致。
func migrateLegacyWorkspacePaths(db *gorm.DB) error {
	type colRewrite struct {
		model interface{}
		col   string
	}
	rewrites := []colRewrite{
		{&models.Workspace{}, "cwd"},
		{&models.Workspace{}, "temp_dir"},
		{&models.Session{}, "cwd"},
		{&models.Session{}, "temp_dir"},
	}
	for _, r := range rewrites {
		// SQL: UPDATE <table> SET <col> = REPLACE(REPLACE(<col>, '/.nextAgent/', '/.openNexus/'), '/.nexusagent/', '/.openNexus/')
		//      WHERE <col> LIKE '%/.nextAgent/%' OR <col> LIKE '%/.nexusagent/%'
		if err := db.Model(r.model).
			Where(r.col+" LIKE ? OR "+r.col+" LIKE ?", "%/.nextAgent/%", "%/.nexusagent/%").
			Update(r.col, gorm.Expr(
				"REPLACE(REPLACE("+r.col+", '/.nextAgent/', '/.openNexus/'), '/.nexusagent/', '/.openNexus/')",
			)).Error; err != nil {
			return fmt.Errorf("改写 %s.%s 旧路径: %w", tableNameOf(r.model), r.col, err)
		}
	}
	return nil
}

// tableNameOf 返回 gorm model 指针对应的表名，用于日志。
func tableNameOf(model interface{}) string {
	if s, ok := model.(interface{ TableName() string }); ok {
		return s.TableName()
	}
	return fmt.Sprintf("%T", model)
}

// migrateAgentSessionID 对非 pending 且 agent_session_id 为空的行，用 session_id 回填。
func migrateAgentSessionID(db *gorm.DB) error {
	return db.Model(&models.Session{}).
		Where("status != ? AND (agent_session_id IS NULL OR agent_session_id = '')", models.SessionStatusPending).
		Update("agent_session_id", gorm.Expr("session_id")).Error
}

func migrateOldSessionsToWorkspaces(db *gorm.DB, defaultCwd string) error {
	var count int64
	if err := db.Model(&models.Session{}).
		Where("workspace_id IS NULL OR workspace_id = 0").
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	type userCwd struct {
		UserID uint   `gorm:"column:user_id"`
		Cwd    string `gorm:"column:cwd"`
	}
	var pairs []userCwd
	if err := db.Model(&models.Session{}).
		Select("DISTINCT user_id, cwd").
		Where("workspace_id IS NULL OR workspace_id = 0").
		Find(&pairs).Error; err != nil {
		return err
	}

	for _, p := range pairs {
		ws := models.Workspace{
			UserID: p.UserID,
			Name:   filepath.Base(p.Cwd),
			Cwd:    p.Cwd,
			Mode:   models.WorkspaceModePersistent,
		}
		if err := db.Create(&ws).Error; err != nil {
			return fmt.Errorf("创建 workspace (user=%d, cwd=%s): %w", p.UserID, p.Cwd, err)
		}
		if err := db.Model(&models.Session{}).
			Where("user_id = ? AND cwd = ? AND (workspace_id IS NULL OR workspace_id = 0)", p.UserID, p.Cwd).
			Update("workspace_id", ws.ID).Error; err != nil {
			return fmt.Errorf("更新 session workspace_id: %w", err)
		}
	}

	return createDefaultWorkspacesForEmptyUsers(db, defaultCwd)
}

// createDefaultWorkspacesForEmptyUsers 为尚无任何工作区的用户补建默认工作区。
// 默认工作区为 persistent 模式，cwd 固定为 defaultCwd（由 config 解析，
// 默认 ~/.openNexus/workspaces/default），跨会话持久保留，不再使用随机临时目录。
func createDefaultWorkspacesForEmptyUsers(db *gorm.DB, defaultCwd string) error {
	var userIDs []uint
	if err := db.Model(&models.User{}).Pluck("id", &userIDs).Error; err != nil {
		return err
	}
	for _, uid := range userIDs {
		var count int64
		if err := db.Model(&models.Workspace{}).Where("user_id = ?", uid).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := os.MkdirAll(defaultCwd, 0o755); err != nil {
				return fmt.Errorf("创建默认工作区目录: %w", err)
			}
			ws := &models.Workspace{
				UserID: uid,
				Name:   "默认工作区",
				Cwd:    defaultCwd,
				Mode:   models.WorkspaceModePersistent,
			}
			if err := db.Create(ws).Error; err != nil {
				return fmt.Errorf("保存默认 workspace: %w", err)
			}
		}
	}
	return nil
}

// migrateDefaultWorkspacesToPersistent 把旧的"默认工作区"（temporary + 随机临时目录）
// 统一改为 persistent + 固定 defaultCwd。与 config 解析后的固定路径保持一致，使旧用户
// 升级后默认工作区同样落在新固定目录下。幂等：已是 persistent 的记录不受影响。
// 旧随机临时目录不主动删除，避免误删用户文件。
func migrateDefaultWorkspacesToPersistent(db *gorm.DB, defaultCwd string) error {
	return db.Model(&models.Workspace{}).
		Where("name = ? AND mode = ?", "默认工作区", models.WorkspaceModeTemporary).
		Updates(map[string]interface{}{
			"cwd":      defaultCwd,
			"mode":     models.WorkspaceModePersistent,
			"temp_dir": "",
		}).Error
}
