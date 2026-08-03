package database

import (
	"testing"

	"opennexus/internal/models"
)

func TestConnect_MigratesTables(t *testing.T) {
	db, err := Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("Connect 返回错误: %v", err)
	}

	if !db.Migrator().HasTable(&models.User{}) {
		t.Error("期望 users 表已迁移，实际不存在")
	}
	if !db.Migrator().HasTable(&models.RefreshToken{}) {
		t.Error("期望 refresh_tokens 表已迁移，实际不存在")
	}
}

func TestConnect_MigratesSessionsTable(t *testing.T) {
	db, err := Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("Connect 返回错误: %v", err)
	}
	if !db.Migrator().HasTable(&models.Session{}) {
		t.Error("期望 sessions 表已迁移，实际不存在")
	}
	if !db.Migrator().HasColumn(&models.Session{}, "agent_session_id") {
		t.Error("期望 sessions.agent_session_id 列已迁移")
	}
}

func TestConnect_MigrateAgentSessionID(t *testing.T) {
	db, err := Connect("file:migrate-agent-sid?mode=memory&cache=shared", "")
	if err != nil {
		t.Fatalf("Connect 返回错误: %v", err)
	}
	// 模拟旧数据：session_id 存的是 ACP id，agent_session_id 为空
	old := &models.Session{
		SessionID: "acp-old-1", AgentType: "cursor", Cwd: "/tmp",
		Status: models.SessionStatusActive, WorkspaceMode: "",
	}
	if err := db.Create(old).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	pending := &models.Session{
		SessionID: "uuid-pending", AgentType: "cursor", Cwd: "/tmp",
		Status: models.SessionStatusPending, WorkspaceMode: "",
	}
	if err := db.Create(pending).Error; err != nil {
		t.Fatalf("Create pending: %v", err)
	}

	if err := migrateAgentSessionID(db); err != nil {
		t.Fatalf("migrateAgentSessionID: %v", err)
	}

	var got models.Session
	if err := db.First(&got, old.ID).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if got.SessionID != "acp-old-1" {
		t.Errorf("SessionID 不应被改: %q", got.SessionID)
	}
	if got.AgentSessionID != "acp-old-1" {
		t.Errorf("AgentSessionID = %q, 期望回填 acp-old-1", got.AgentSessionID)
	}

	var pend models.Session
	if err := db.First(&pend, pending.ID).Error; err != nil {
		t.Fatalf("First pending: %v", err)
	}
	if pend.AgentSessionID != "" {
		t.Errorf("pending 不应回填 AgentSessionID, got %q", pend.AgentSessionID)
	}
}

func TestMigrateLegacyWorkspacePaths(t *testing.T) {
	db, err := Connect("file:migrate-legacy-paths?mode=memory&cache=shared", "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// 旧路径数据：workspaces + sessions 各一条含 .nextAgent / .nexusagent
	ws := &models.Workspace{
		UserID: 1, Name: "old",
		Cwd:     "/home/u/.nextAgent/session/nexus-123",
		TempDir: "/home/u/.nextAgent/session/nexus-123",
		Mode:    models.WorkspaceModePersistent,
	}
	if err := db.Create(ws).Error; err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	se := &models.Session{
		SessionID: "s1", AgentType: "claude-code",
		Cwd:          "/home/u/.nexusagent/session/abc",
		TempDir:      "/home/u/.nexusagent/session/abc",
		Status:       models.SessionStatusActive,
		WorkspaceMode: models.WorkspaceModeTemporary,
		WorkspaceID:  &ws.ID,
	}
	if err := db.Create(se).Error; err != nil {
		t.Fatalf("Create session: %v", err)
	}

	if err := migrateLegacyWorkspacePaths(db); err != nil {
		t.Fatalf("migrateLegacyWorkspacePaths: %v", err)
	}

	var gotWS models.Workspace
	if err := db.First(&gotWS, ws.ID).Error; err != nil {
		t.Fatalf("First ws: %v", err)
	}
	if gotWS.Cwd != "/home/u/.openNexus/session/nexus-123" {
		t.Errorf("workspace.cwd = %q, 期望 .openNexus", gotWS.Cwd)
	}
	if gotWS.TempDir != "/home/u/.openNexus/session/nexus-123" {
		t.Errorf("workspace.temp_dir = %q, 期望 .openNexus", gotWS.TempDir)
	}

	var gotSE models.Session
	if err := db.First(&gotSE, se.ID).Error; err != nil {
		t.Fatalf("First session: %v", err)
	}
	if gotSE.Cwd != "/home/u/.openNexus/session/abc" {
		t.Errorf("session.cwd = %q, 期望 .openNexus", gotSE.Cwd)
	}
	if gotSE.TempDir != "/home/u/.openNexus/session/abc" {
		t.Errorf("session.temp_dir = %q, 期望 .openNexus", gotSE.TempDir)
	}

	// 幂等：再跑一次不应改动
	if err := migrateLegacyWorkspacePaths(db); err != nil {
		t.Fatalf("二次 migrateLegacyWorkspacePaths: %v", err)
	}
	var gotWS2 models.Workspace
	_ = db.First(&gotWS2, ws.ID).Error
	if gotWS2.Cwd != "/home/u/.openNexus/session/nexus-123" {
		t.Errorf("二次执行后 workspace.cwd = %q, 期望不变", gotWS2.Cwd)
	}
}

// TestMigrateDefaultWorkspacesToPersistent 验证旧的 temporary 默认工作区被迁移为
// persistent + 固定 defaultCwd，且已是 persistent 的同名工作区不受影响（幂等）。
func TestMigrateDefaultWorkspacesToPersistent(t *testing.T) {
	db, err := Connect("file:migrate-default-ws?mode=memory&cache=shared", "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	const defaultCwd = "/home/u/.openNexus/workspaces/default"

	// 旧默认工作区：temporary + 随机临时目录
	old := &models.Workspace{
		UserID: 1, Name: "默认工作区",
		Cwd:     "/home/u/.openNexus/session/opennexus-abc",
		TempDir: "/home/u/.openNexus/session/opennexus-abc",
		Mode:    models.WorkspaceModeTemporary,
	}
	if err := db.Create(old).Error; err != nil {
		t.Fatalf("Create old default workspace: %v", err)
	}
	// 另一个用户的普通 persistent 工作区（同名但已是 persistent），不应被改动
	other := &models.Workspace{
		UserID: 2, Name: "默认工作区",
		Cwd:  "/home/u/.openNexus/workspaces/preset",
		Mode: models.WorkspaceModePersistent,
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("Create persistent workspace: %v", err)
	}

	if err := migrateDefaultWorkspacesToPersistent(db, defaultCwd); err != nil {
		t.Fatalf("migrateDefaultWorkspacesToPersistent: %v", err)
	}

	var got models.Workspace
	if err := db.First(&got, old.ID).Error; err != nil {
		t.Fatalf("First old: %v", err)
	}
	if got.Mode != models.WorkspaceModePersistent {
		t.Errorf("mode = %q, 期望 persistent", got.Mode)
	}
	if got.Cwd != defaultCwd {
		t.Errorf("cwd = %q, 期望 %q", got.Cwd, defaultCwd)
	}
	if got.TempDir != "" {
		t.Errorf("temp_dir = %q, 期望清空", got.TempDir)
	}

	var gotOther models.Workspace
	if err := db.First(&gotOther, other.ID).Error; err != nil {
		t.Fatalf("First other: %v", err)
	}
	if gotOther.Cwd != "/home/u/.openNexus/workspaces/preset" {
		t.Errorf("已是 persistent 的记录被误改: cwd = %q", gotOther.Cwd)
	}

	// 幂等：再跑一次，old 记录已为 persistent，不应再被命中
	if err := migrateDefaultWorkspacesToPersistent(db, defaultCwd); err != nil {
		t.Fatalf("二次 migrate: %v", err)
	}
	var got2 models.Workspace
	_ = db.First(&got2, old.ID).Error
	if got2.Cwd != defaultCwd {
		t.Errorf("二次执行后 cwd = %q, 期望不变", got2.Cwd)
	}
}
