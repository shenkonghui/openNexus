package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"gorm.io/gorm"

	"opennexus/internal/config"
	"opennexus/internal/database"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

func setupACPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	db.Exec("DELETE FROM users")
	db.Exec("DELETE FROM refresh_tokens")
	db.Exec("DELETE FROM sessions")
	db.Exec("DELETE FROM workspaces")
	db.Exec("DELETE FROM running_tasks")
	return db
}

func testDiscoveryConfig(t *testing.T) (config.SkillsConfig, config.CommandsConfig, config.RulesConfig, config.SubAgentsConfig) {
	t.Helper()
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "this-is-a-very-long-jwt-secret-key-32+bytes!"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 默认配置失败: %v", err)
	}
	return cfg.Agents.Skills, cfg.Agents.Commands, cfg.Agents.Rules, cfg.Agents.SubAgents
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	db := setupACPTestDB(t)
	wsCfg := config.WorkspaceConfig{
		DefaultMode:   "external",
		TempDirPrefix: "test-",
	}
	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	return NewService(db, t.TempDir(), wsCfg, skills, commands, rules, subAgents)
}

func newTestServiceWithDB(t *testing.T, db *gorm.DB) (*Service, string) {
	t.Helper()
	msgDir := t.TempDir()
	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, msgDir, config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	return svc, msgDir
}

func TestService_RegisterBackend(t *testing.T) {
	svc := newTestService(t)
	svc.RegisterBackend(NewClaudeCodeBackend(config.ClaudeCodeConfig{}))

	b, err := svc.GetBackend("claude-code")
	if err != nil {
		t.Fatalf("GetBackend 错误: %v", err)
	}
	if b.Name() != "claude-code" {
		t.Errorf("Name = %q", b.Name())
	}
}

func TestService_GetBackend_NotFound(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.GetBackend("unknown"); err == nil {
		t.Error("期望未注册后端返回错误")
	}
}

func TestService_ListSessions_Empty(t *testing.T) {
	svc := newTestService(t)
	sessions, err := svc.ListSessions(1)
	if err != nil {
		t.Fatalf("ListSessions 错误: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("期望 0 条会话，实际 %d", len(sessions))
	}
}

func TestService_GetSession_NotFound(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.GetSession("missing"); err == nil {
		t.Error("期望未找到时返回错误")
	}
}

func TestService_RecoverActiveSessions(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	// interrupted-session：有被中断任务，应被标记为 error
	interruptedSess := &models.Session{
		SessionID:     "recovery-interrupted",
		AgentType:     "claude-code",
		Cwd:           "/tmp",
		Status:        models.SessionStatusActive,
		WorkspaceMode: "",
	}
	_ = repo.Create(interruptedSess)
	// idle-session：无被中断任务（已完成），应保持 active
	idleSess := &models.Session{
		SessionID:     "recovery-idle",
		AgentType:     "claude-code",
		Cwd:           "/tmp",
		Status:        models.SessionStatusActive,
		WorkspaceMode: "",
	}
	_ = repo.Create(idleSess)

	// 为 interrupted-session 插入一个 running 状态的任务（恢复后变为 interrupted）
	taskRepo := repository.NewRunningTaskRepository(db)
	_ = taskRepo.Create(&models.RunningTask{
		DBSessionID: interruptedSess.ID,
		UserID:      1,
		Prompt:      "interrupted prompt",
		Status:      models.RunningTaskStatusRunning,
		StartedAt:   time.Now(),
	})

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	svc.RecoverActiveSessions()

	// 有中断任务的会话 → error
	s, _ := svc.GetSession("recovery-interrupted")
	if s.Status != models.SessionStatusError {
		t.Errorf("有中断任务的会话期望 status=error，实际 %q", s.Status)
	}
	// 无中断任务的空闲会话 → 保持 active（不再被误标为 error）
	s2, _ := svc.GetSession("recovery-idle")
	if s2.Status != models.SessionStatusActive {
		t.Errorf("空闲会话期望保持 status=active，实际 %q", s2.Status)
	}
}

func TestService_GetSessionByDBID(t *testing.T) {
	svc := newTestService(t)
	repo := repository.NewSessionRepository(setupACPTestDB(t))
	_ = repo.Create(&models.Session{
		SessionID: "db-id-test", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusActive, WorkspaceMode: "",
	})
	// 用 svc 自身的 sessions 仓库重新查（因为 newTestService 用的是同一个 db）
	sess, err := svc.GetSession("db-id-test")
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	got, err := svc.GetSessionByDBID(sess.ID)
	if err != nil {
		t.Fatalf("GetSessionByDBID 返回错误: %v", err)
	}
	if got.SessionID != "db-id-test" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
}

func TestService_GetSessionByDBID_NotFound(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.GetSessionByDBID(99999); err == nil {
		t.Error("期望不存在的 DB ID 返回错误")
	}
}

func TestService_ListMessages(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	sess := &models.Session{
		SessionID: "msg-list-1", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusActive, WorkspaceMode: "",
	}
	_ = repo.Create(sess)

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	msgDir := t.TempDir()
	svc := NewService(db, msgDir, config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	// 消息仓库带写缓冲与读缓存，必须复用 service 的实例（同目录双实例会读到未 flush 的半截文件）
	msgRepo := svc.MessageRepo()
	_ = msgRepo.Create(&models.Message{SessionID: "msg-list-1", DBSessionID: sess.ID, Role: models.MessageRoleUser, Kind: models.MessageKindUserMessageChunk, Content: "问题", RawJSON: "{}", Sequence: 1})
	_ = msgRepo.Create(&models.Message{SessionID: "msg-list-1", DBSessionID: sess.ID, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "回答", RawJSON: "{}", Sequence: 2})
	msgs, err := svc.ListMessages("msg-list-1")
	if err != nil {
		t.Fatalf("ListMessages 返回错误: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息，实际 %d", len(msgs))
	}
	if msgs[0].Content != "问题" {
		t.Errorf("第一条消息 Content = %q", msgs[0].Content)
	}
	if msgs[1].Content != "回答" {
		t.Errorf("第二条消息 Content = %q", msgs[1].Content)
	}
}

func TestService_ListMessages_ReturnsLastN(t *testing.T) {
	// 超过默认页大小时必须返回最近 N 条，而非最早 N 条（否则长会话 loadData 会冲掉近期对话）。
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	sess := &models.Session{
		SessionID: "msg-last-n", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusActive, WorkspaceMode: "",
	}
	_ = repo.Create(sess)
	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	msgDir := t.TempDir()
	svc := NewService(db, msgDir, config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	msgRepo := svc.MessageRepo()
	n := defaultMessagePageSize + 50
	for i := 1; i <= n; i++ {
		_ = msgRepo.Create(&models.Message{
			SessionID: "msg-last-n", DBSessionID: sess.ID,
			Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk,
			Content: fmt.Sprintf("m%d", i), RawJSON: "{}", Sequence: i,
		})
	}
	msgs, err := svc.ListMessages("msg-last-n")
	if err != nil {
		t.Fatalf("ListMessages 返回错误: %v", err)
	}
	if len(msgs) != defaultMessagePageSize {
		t.Fatalf("期望 %d 条，实际 %d", defaultMessagePageSize, len(msgs))
	}
	if msgs[0].Sequence != n-defaultMessagePageSize+1 {
		t.Errorf("首条 sequence = %d, 期望最近页起点 %d", msgs[0].Sequence, n-defaultMessagePageSize+1)
	}
	if msgs[len(msgs)-1].Sequence != n {
		t.Errorf("末条 sequence = %d, 期望 %d", msgs[len(msgs)-1].Sequence, n)
	}
}

func TestService_ListMessagesRecent(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	sess := &models.Session{
		SessionID: "msg-recent-1", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusActive, WorkspaceMode: "",
	}
	_ = repo.Create(sess)
	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	msgDir := t.TempDir()
	svc := NewService(db, msgDir, config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	msgRepo := svc.MessageRepo()
	// 写 10 条消息，sequence 1..10
	for i := 1; i <= 10; i++ {
		_ = msgRepo.Create(&models.Message{
			SessionID: "msg-recent-1", DBSessionID: sess.ID,
			Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk,
			Content: fmt.Sprintf("m%d", i), RawJSON: "{}", Sequence: i,
		})
	}

	// 默认（before=0, limit=0→默认页大小 500）：返回全部 10 条，hasMore=false
	msgs, hasMore, err := svc.ListMessagesRecent("msg-recent-1", 0, 0)
	if err != nil {
		t.Fatalf("ListMessagesRecent 返回错误: %v", err)
	}
	if len(msgs) != 10 || hasMore {
		t.Fatalf("期望 10 条 hasMore=false，实际 %d 条 hasMore=%v", len(msgs), hasMore)
	}

	// limit=3：返回最近 3 条 [8,9,10]，hasMore=true
	msgs, hasMore, err = svc.ListMessagesRecent("msg-recent-1", 0, 3)
	if err != nil {
		t.Fatalf("ListMessagesRecent limit=3 返回错误: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Sequence != 8 || msgs[2].Sequence != 10 || !hasMore {
		t.Fatalf("期望 [8,9,10] hasMore=true，实际 %v hasMore=%v", seqs(msgs), hasMore)
	}

	// before=8, limit=3：返回 sequence<8 的最近 3 条 [5,6,7]，hasMore=true
	msgs, hasMore, err = svc.ListMessagesRecent("msg-recent-1", 8, 3)
	if err != nil {
		t.Fatalf("ListMessagesRecent before=8 返回错误: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Sequence != 5 || msgs[2].Sequence != 7 || !hasMore {
		t.Fatalf("期望 [5,6,7] hasMore=true，实际 %v hasMore=%v", seqs(msgs), hasMore)
	}

	// before=3, limit=5：sequence<3 只有 [1,2]，hasMore=false
	msgs, hasMore, err = svc.ListMessagesRecent("msg-recent-1", 3, 5)
	if err != nil {
		t.Fatalf("ListMessagesRecent before=3 返回错误: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Sequence != 1 || msgs[1].Sequence != 2 || hasMore {
		t.Fatalf("期望 [1,2] hasMore=false，实际 %v hasMore=%v", seqs(msgs), hasMore)
	}

	// before=1：没有更早消息，返回空，hasMore=false
	msgs, hasMore, err = svc.ListMessagesRecent("msg-recent-1", 1, 10)
	if err != nil {
		t.Fatalf("ListMessagesRecent before=1 返回错误: %v", err)
	}
	if len(msgs) != 0 || hasMore {
		t.Fatalf("期望空 hasMore=false，实际 %d 条 hasMore=%v", len(msgs), hasMore)
	}
}

func seqs(msgs []models.Message) []int {
	out := make([]int, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Sequence)
	}
	return out
}

func TestService_ListMessages_SessionNotFound(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.ListMessages("nonexistent"); err == nil {
		t.Error("期望不存在的会话返回错误")
	}
}

func TestService_ListMessages_Empty(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	_ = repo.Create(&models.Session{
		SessionID: "empty-msg-1", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusActive, WorkspaceMode: "",
	})

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	msgs, err := svc.ListMessages("empty-msg-1")
	if err != nil {
		t.Fatalf("ListMessages 返回错误: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("期望 0 条消息，实际 %d", len(msgs))
	}
}

func TestService_ResumeSession_Closed_NoBackend(t *testing.T) {
	// 已关闭会话现在允许重开；此处后端未注册，应在获取后端阶段失败
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	_ = repo.Create(&models.Session{
		SessionID: "closed-resume-1", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusClosed, WorkspaceMode: "",
	})

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	_, err := svc.ResumeSession(context.Background(), "closed-resume-1")
	if err == nil {
		t.Error("期望后端未注册时重开返回错误")
	}
}

func TestService_ResumeSession_PersistentCwdNotExists(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	wsRepo := repository.NewWorkspaceRepository(db)
	missingDir := filepath.Join(t.TempDir(), "missing-persistent")
	ws := &models.Workspace{
		UserID: 1, Name: "项目", Cwd: missingDir,
		Mode: models.WorkspaceModePersistent,
	}
	if err := wsRepo.Create(ws); err != nil {
		t.Fatalf("创建 workspace 失败: %v", err)
	}
	wid := ws.ID
	_ = repo.Create(&models.Session{
		SessionID: "closed-resume-3", AgentType: "claude-code", Cwd: missingDir,
		Status: models.SessionStatusError, WorkspaceID: &wid,
	})

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	_, err := svc.ResumeSession(context.Background(), "closed-resume-3")
	if err == nil {
		t.Error("persistent 工作目录不存在时期望返回错误")
	}
}

func TestService_ResumeSession_CwdNotExists(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	wsRepo := repository.NewWorkspaceRepository(db)
	tempDir := filepath.Join(t.TempDir(), "missing-resume")
	ws := &models.Workspace{
		UserID: 1, Name: "默认", Cwd: tempDir,
		Mode: models.WorkspaceModeTemporary, TempDir: tempDir,
	}
	if err := wsRepo.Create(ws); err != nil {
		t.Fatalf("创建 workspace 失败: %v", err)
	}
	wid := ws.ID
	_ = repo.Create(&models.Session{
		SessionID: "closed-resume-2", AgentType: "claude-code", Cwd: tempDir,
		Status: models.SessionStatusError, WorkspaceID: &wid,
	})

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	_, err := svc.ResumeSession(context.Background(), "closed-resume-2")
	if err == nil {
		t.Error("期望后端未注册时重开返回错误")
	}
	if _, statErr := os.Stat(tempDir); statErr != nil {
		t.Errorf("temporary 工作目录应已重建: %v", statErr)
	}
}

func TestService_ResumeSession_SessionNotFound(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.ResumeSession(context.Background(), "nonexistent"); err == nil {
		t.Error("期望不存在的会话恢复返回错误")
	}
}

func TestService_DeleteSession_RemovesSessionAndMessages(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	msgDir := t.TempDir()
	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, msgDir, config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	// 消息仓库带写缓冲与读缓存，必须复用 service 的实例（同目录双实例会读到未 flush 的半截文件）
	msgRepo := svc.MessageRepo()
	wsRepo := repository.NewWorkspaceRepository(db)
	tempDir := filepath.Join(t.TempDir(), "keep-after-delete")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	ws := &models.Workspace{
		UserID: 1, Name: "默认", Cwd: tempDir,
		Mode: models.WorkspaceModeTemporary, TempDir: tempDir,
	}
	if err := wsRepo.Create(ws); err != nil {
		t.Fatalf("创建 workspace 失败: %v", err)
	}
	wid := ws.ID
	sess := &models.Session{
		SessionID: "delete-1", AgentType: "claude-code", Cwd: tempDir,
		Status: models.SessionStatusClosed, WorkspaceID: &wid,
	}
	if err := repo.Create(sess); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	if err := msgRepo.Create(&models.Message{
		SessionID: "delete-1", DBSessionID: sess.ID, Role: "user",
		Kind: "user_message_chunk", Content: "hi", Sequence: 1, RawJSON: "{}",
	}); err != nil {
		t.Fatalf("创建消息失败: %v", err)
	}

	if err := svc.DeleteSession(context.Background(), "delete-1"); err != nil {
		t.Fatalf("DeleteSession 错误: %v", err)
	}
	if _, err := repo.FindByID(sess.ID); err == nil {
		t.Error("期望会话记录已被删除")
	}
	msgs, _ := msgRepo.FindBySessionID("delete-1")
	if len(msgs) != 0 {
		t.Errorf("期望消息已删除，实际 %d 条", len(msgs))
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Error("删除会话后孤儿 temporary 工作区目录应被清理")
	}
	if _, err := wsRepo.FindByID(ws.ID); err == nil {
		t.Error("期望孤儿 temporary 工作区记录已被删除")
	}
}

func TestService_DeleteSession_KeepsPersistentWorkspace(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	wsRepo := repository.NewWorkspaceRepository(db)
	dir := filepath.Join(t.TempDir(), "persistent-ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	ws := &models.Workspace{
		UserID: 1, Name: "项目", Cwd: dir,
		Mode: models.WorkspaceModePersistent,
	}
	if err := wsRepo.Create(ws); err != nil {
		t.Fatalf("创建 workspace 失败: %v", err)
	}
	wid := ws.ID
	sess := &models.Session{
		SessionID: "delete-persist", AgentType: "claude-code", Cwd: dir,
		Status: models.SessionStatusClosed, WorkspaceID: &wid,
	}
	if err := repo.Create(sess); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	if err := svc.DeleteSession(context.Background(), "delete-persist"); err != nil {
		t.Fatalf("DeleteSession 错误: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("persistent 工作区目录应保留: %v", err)
	}
	if _, err := wsRepo.FindByID(ws.ID); err != nil {
		t.Errorf("persistent 工作区记录应保留: %v", err)
	}
}

// TestService_DeleteSessionWithMessages_ReleasesRouteAndCaches 验证
// DeleteSessionWithMessages（工作区删除路径）会清理 sessionPoolKey/commands/configs/modes
// 等缓存条目，避免原 bug 中 map 无限增长。
func TestService_DeleteSessionWithMessages_ReleasesRouteAndCaches(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	wsRepo := repository.NewWorkspaceRepository(db)
	dir := filepath.Join(t.TempDir(), "dsm-ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	ws := &models.Workspace{UserID: 1, Name: "ws", Cwd: dir, Mode: models.WorkspaceModePersistent}
	if err := wsRepo.Create(ws); err != nil {
		t.Fatalf("创建 workspace: %v", err)
	}
	wid := ws.ID
	sess := &models.Session{
		SessionID: "dsm-1", AgentType: "claude-code", Cwd: dir,
		Status: models.SessionStatusClosed, WorkspaceID: &wid,
	}
	if err := repo.Create(sess); err != nil {
		t.Fatalf("创建会话: %v", err)
	}

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)

	// 模拟该会话已建立路由与缓存（与 detachSession 清理的字段一一对应）
	poolKey := connectionKey(sess.AgentType, dir)
	svc.mu.Lock()
	svc.sessionPoolKey[sess.SessionID] = poolKey
	svc.commands[sess.SessionID] = []acp.AvailableCommand{{Name: "cmd1"}}
	svc.configs[sess.SessionID] = []acp.SessionConfigOption{{}}
	svc.modes[sess.SessionID] = []acp.SessionMode{{}}
	svc.mu.Unlock()

	if err := svc.DeleteSessionWithMessages(sess); err != nil {
		t.Fatalf("DeleteSessionWithMessages: %v", err)
	}

	svc.mu.RLock()
	_, hasRoute := svc.sessionPoolKey[sess.SessionID]
	_, hasCmd := svc.commands[sess.SessionID]
	_, hasCfg := svc.configs[sess.SessionID]
	_, hasMode := svc.modes[sess.SessionID]
	svc.mu.RUnlock()
	if hasRoute || hasCmd || hasCfg || hasMode {
		t.Errorf("DeleteSessionWithMessages 未清理缓存: route=%v cmd=%v cfg=%v mode=%v",
			hasRoute, hasCmd, hasCfg, hasMode)
	}

	// 消息与会话记录也应被删除
	msgs, _ := svc.messages.FindBySessionID(sess.SessionID)
	if len(msgs) != 0 {
		t.Errorf("期望消息已删除，实际 %d 条", len(msgs))
	}
	if _, err := repo.FindByID(sess.ID); err == nil {
		t.Error("期望会话记录已删除")
	}
}

func TestService_DeleteSession_NotFound(t *testing.T) {
	svc := newTestService(t)
	if err := svc.DeleteSession(context.Background(), "nonexistent"); err == nil {
		t.Error("期望不存在的会话删除返回错误")
	}
}

// TestMsgBroadcaster_FanOut 验证多订阅者都能收到广播的消息。
func TestMsgBroadcaster_FanOut(t *testing.T) {
	bc := newMsgBroadcaster(0)
	ch1, _ := bc.subscribe(16)
	ch2, _ := bc.subscribe(16)

	msg := models.Message{Sequence: 1, Content: "hello"}
	bc.broadcast(msg)

	m1 := <-ch1
	m2 := <-ch2
	if m1.Content != "hello" || m2.Content != "hello" {
		t.Errorf("两个订阅者都应收到消息，得到 %q 和 %q", m1.Content, m2.Content)
	}
	if bc.subscriberCount() != 2 {
		t.Errorf("期望 2 个订阅者，实际 %d", bc.subscriberCount())
	}

	bc.close()
	if bc.subscriberCount() != 0 {
		t.Errorf("关闭后期望 0 个订阅者，实际 %d", bc.subscriberCount())
	}
}

// TestService_RecoverActiveSessions_InterruptsRunningTasks 验证启动恢复会将 running 状态的 running_task 标记为 interrupted。
func TestService_RecoverActiveSessions_InterruptsRunningTasks(t *testing.T) {
	db := setupACPTestDB(t)
	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)

	// 插入一个 running 状态的 running_task
	taskRepo := repository.NewRunningTaskRepository(db)
	_ = taskRepo.Create(&models.RunningTask{
		DBSessionID: 1,
		UserID:      1,
		Prompt:      "test prompt",
		Status:      models.RunningTaskStatusRunning,
		StartedAt:   time.Now(),
	})

	// 触发恢复
	svc.RecoverActiveSessions()

	tasks, _ := svc.ListInterruptedTasks(1)
	if len(tasks) != 1 {
		t.Fatalf("期望 1 个 interrupted 任务，实际 %d", len(tasks))
	}
	if tasks[0].Status != models.RunningTaskStatusInterrupted {
		t.Errorf("期望 status=interrupted，实际 %q", tasks[0].Status)
	}
}

// TestRunningTaskRepository_CRUD 验证 running_task 仓库的基本 CRUD。
func TestRunningTaskRepository_CRUD(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewRunningTaskRepository(db)

	task := &models.RunningTask{
		DBSessionID: 10,
		UserID:      5,
		Prompt:      "hello agent",
		Status:      models.RunningTaskStatusRunning,
		StartedAt:   time.Now(),
	}
	if err := repo.Create(task); err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if task.ID == 0 {
		t.Fatal("Create 后 ID 应非零")
	}

	// UpdateLastSeq
	if err := repo.UpdateLastSeq(task.ID, 42); err != nil {
		t.Fatalf("UpdateLastSeq 失败: %v", err)
	}
	got, _ := repo.FindByID(task.ID)
	if got.LastSeq != 42 {
		t.Errorf("期望 LastSeq=42，实际 %d", got.LastSeq)
	}

	// MarkRunningAsInterrupted
	if err := repo.MarkRunningAsInterrupted(); err != nil {
		t.Fatalf("MarkRunningAsInterrupted 失败: %v", err)
	}
	interrupted, _ := repo.FindInterruptedByDBSessionID(10)
	if len(interrupted) != 1 {
		t.Errorf("期望 1 个 interrupted 任务，实际 %d", len(interrupted))
	}

	// UpdateStatus done
	if err := repo.UpdateStatus(task.ID, models.RunningTaskStatusDone, nil); err != nil {
		t.Fatalf("UpdateStatus 失败: %v", err)
	}
	interrupted2, _ := repo.FindInterruptedByDBSessionID(10)
	if len(interrupted2) != 0 {
		t.Errorf("标记 done 后期望 0 个 interrupted，实际 %d", len(interrupted2))
	}
}

func TestService_SetFailedTaskAutoRetryOnce(t *testing.T) {
	svc := newTestService(t)
	if !svc.failedTaskAutoRetryOnce {
		t.Error("NewService 默认应开启失败自动重试")
	}
	svc.SetFailedTaskAutoRetryOnce(false)
	if svc.failedTaskAutoRetryOnce {
		t.Error("SetFailedTaskAutoRetryOnce(false) 后应为关闭")
	}
}

// TestService_PromptMaxDuration 验证 prompt 最大存活时间的配置解析逻辑：
// 未设置/0/负值取默认 30min；正值原样使用。
func TestService_PromptMaxDuration(t *testing.T) {
	svc := newTestService(t)

	// 默认：未 SetPromptMaxDuration 时取 defaultPromptMaxDuration (30m)
	if got := svc.effectivePromptMaxDuration(); got != defaultPromptMaxDuration {
		t.Errorf("默认期望 %v，实际 %v", defaultPromptMaxDuration, got)
	}

	// 显式设置正值生效
	svc.SetPromptMaxDuration(5 * time.Minute)
	if got := svc.effectivePromptMaxDuration(); got != 5*time.Minute {
		t.Errorf("设置 5m 后期望 5m，实际 %v", got)
	}

	// 0/负值回落默认
	svc.SetPromptMaxDuration(0)
	if got := svc.effectivePromptMaxDuration(); got != defaultPromptMaxDuration {
		t.Errorf("设置 0 后期望默认 %v，实际 %v", defaultPromptMaxDuration, got)
	}
	svc.SetPromptMaxDuration(-time.Second)
	if got := svc.effectivePromptMaxDuration(); got != defaultPromptMaxDuration {
		t.Errorf("设置负值后期望默认 %v，实际 %v", defaultPromptMaxDuration, got)
	}
}

// TestSessionCwd 验证 sessionCwd 在以下场景的优先级：
// 1) session.Cwd 与工作区不同（如 worktree 覆盖）优先用 session.Cwd
// 2) session.Cwd 为空时回退到工作区 cwd
// 3) session.Cwd 与工作区相同则返回工作区 cwd
// 4) 无工作区时直接返回 session.Cwd
func TestSessionCwd(t *testing.T) {
	db := setupACPTestDB(t)
	wsRepo := repository.NewWorkspaceRepository(db)

	tmp := t.TempDir()
	wsCwd := filepath.Join(tmp, "workspace")
	wtCwd := filepath.Join(tmp, "workspace", ".worktrees", "task-1")
	if err := os.MkdirAll(wsCwd, 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	if err := os.MkdirAll(wtCwd, 0o755); err != nil {
		t.Fatalf("创建 worktree 目录: %v", err)
	}

	ws := &models.Workspace{UserID: 1, Name: "项目", Cwd: wsCwd, Mode: models.WorkspaceModePersistent}
	if err := wsRepo.Create(ws); err != nil {
		t.Fatalf("创建 workspace: %v", err)
	}

	t.Run("worktree覆盖优先使用session.Cwd", func(t *testing.T) {
		wid := ws.ID
		sess := &models.Session{SessionID: "s1", Cwd: wtCwd, WorkspaceID: &wid}
		if got := sessionCwd(sess, wsRepo); got != wtCwd {
			t.Errorf("sessionCwd = %q, want %q", got, wtCwd)
		}
	})

	t.Run("session.Cwd为空则回退到workspace.cwd", func(t *testing.T) {
		wid := ws.ID
		sess := &models.Session{SessionID: "s2", Cwd: "", WorkspaceID: &wid}
		if got := sessionCwd(sess, wsRepo); got != wsCwd {
			t.Errorf("sessionCwd = %q, want %q", got, wsCwd)
		}
	})

	t.Run("session.Cwd与workspace相同则返回workspace.cwd", func(t *testing.T) {
		wid := ws.ID
		sess := &models.Session{SessionID: "s3", Cwd: wsCwd, WorkspaceID: &wid}
		if got := sessionCwd(sess, wsRepo); got != wsCwd {
			t.Errorf("sessionCwd = %q, want %q", got, wsCwd)
		}
	})

	t.Run("无workspace时返回session.Cwd", func(t *testing.T) {
		sess := &models.Session{SessionID: "s4", Cwd: wtCwd}
		if got := sessionCwd(sess, wsRepo); got != wtCwd {
			t.Errorf("sessionCwd = %q, want %q", got, wtCwd)
		}
	})
}

// TestEffectiveIdleTimeout 验证 SetIdleTimeout 三态语义：正数=配置值；0=默认 30m；负数=关闭(0)。
func TestEffectiveIdleTimeout(t *testing.T) {
	s := newTestService(t)

	// 默认（未设置）= 30m
	if got := s.effectiveIdleTimeout(); got != 30*time.Minute {
		t.Errorf("默认 effectiveIdleTimeout = %v, 期望 30m", got)
	}

	// 正数=配置值
	s.SetIdleTimeout(5 * time.Minute)
	if got := s.effectiveIdleTimeout(); got != 5*time.Minute {
		t.Errorf("配置 5m 时 effectiveIdleTimeout = %v, 期望 5m", got)
	}

	// 负数=关闭（返回 0）
	s.SetIdleTimeout(-1)
	if got := s.effectiveIdleTimeout(); got != 0 {
		t.Errorf("关闭(-1)时 effectiveIdleTimeout = %v, 期望 0", got)
	}

	// 0=恢复默认
	s.SetIdleTimeout(0)
	if got := s.effectiveIdleTimeout(); got != 30*time.Minute {
		t.Errorf("置 0 后 effectiveIdleTimeout = %v, 期望 30m", got)
	}
}

// TestHasActivePromptForPoolKey 验证 poolKey 下存在活跃 prompt 的判定。
func TestHasActivePromptForPoolKey(t *testing.T) {
	s := newTestService(t)
	const pk = "codebuddy\x00/tmp/proj"

	s.mu.Lock()
	s.sessionPoolKey["sess-active"] = pk
	s.sessionPoolKey["sess-idle"] = pk
	s.activePrompts["sess-active"] = newMsgBroadcaster(0)
	s.mu.Unlock()

	if !s.hasActivePromptForPoolKey(pk) {
		t.Errorf("存在活跃 prompt 时 hasActivePromptForPoolKey=false, 期望 true")
	}

	// 清掉活跃 prompt 后应返回 false
	s.mu.Lock()
	delete(s.activePrompts, "sess-active")
	s.mu.Unlock()
	if s.hasActivePromptForPoolKey(pk) {
		t.Errorf("无活跃 prompt 时 hasActivePromptForPoolKey=true, 期望 false")
	}

	// 不相关的 poolKey 应返回 false
	if s.hasActivePromptForPoolKey("other\x00/x") {
		t.Errorf("无关 poolKey 时 hasActivePromptForPoolKey=true, 期望 false")
	}
}

// TestReapIdleConnections_NoRepo 验证 acpConnRepo 为 nil 时回收扫描不 panic（短路返回）。
func TestReapIdleConnections_NoRepo(t *testing.T) {
	s := newTestService(t)
	// acpConnRepo 默认为 nil（newTestService 不注入）
	s.reapIdleConnections() // 不应 panic
}

// TestReapIdleConnections_Disabled 验证 idleTimeout<=0（关闭）时不执行回收。
func TestReapIdleConnections_Disabled(t *testing.T) {
	s := newTestService(t)
	s.SetACPConnectionRepo(repository.NewACPConnectionRepository(setupACPTestDB(t)))
	s.SetIdleTimeout(-1)    // 关闭回收
	s.reapIdleConnections() // 应在 effectiveIdleTimeout<=0 处短路，不查 DB
}

// TestReapIdleConnections_StaleRowCleaned 验证：DB 存在超时空闲行、但 pool 已无对应连接时，
// 回收扫描会清除该残留心跳表行（防止 PID 失效后行永久残留）。
func TestReapIdleConnections_StaleRowCleaned(t *testing.T) {
	db := setupACPTestDB(t)
	// setupACPTestDB 未清 acp_connections 表，这里手动清
	db.Exec("DELETE FROM acp_connections")
	repo := repository.NewACPConnectionRepository(db)
	s := newTestService(t)
	s.SetACPConnectionRepo(repo)
	s.SetIdleTimeout(30 * time.Minute)

	const poolKey = "codebuddy\x00/tmp/proj"
	// 直接用 gorm 插入一条 last_active_at 已过期的行（绕过 Upsert 的 now 语义）
	stale := models.ACPConnection{
		PoolKey: poolKey, AgentType: "codebuddy", Cwd: "/tmp/proj", Pid: 99999,
		LastActiveAt: time.Now().Add(-2 * time.Hour), // 远超 30m 阈值
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatalf("插入空闲行失败: %v", err)
	}

	// 此时 pool 为空（无对应连接），扫描应清掉该残留行
	s.reapIdleConnections()

	var count int64
	db.Model(&models.ACPConnection{}).Count(&count)
	if count != 0 {
		t.Errorf("回收后 acp_connections 仍有 %d 行残留, 期望 0", count)
	}
}

// TestReapIdleConnections_RecentRowKept 验证：活动时间未超阈值的行不会被回收。
func TestReapIdleConnections_RecentRowKept(t *testing.T) {
	db := setupACPTestDB(t)
	db.Exec("DELETE FROM acp_connections")
	repo := repository.NewACPConnectionRepository(db)
	s := newTestService(t)
	s.SetACPConnectionRepo(repo)
	s.SetIdleTimeout(30 * time.Minute)

	// 活动时间在阈值内（5 分钟前，阈值 30 分钟）
	recent := models.ACPConnection{
		PoolKey: "codebuddy\x00/tmp/recent", AgentType: "codebuddy", Cwd: "/tmp/recent", Pid: 12345,
		LastActiveAt: time.Now().Add(-5 * time.Minute),
	}
	if err := db.Create(&recent).Error; err != nil {
		t.Fatalf("插入近期行失败: %v", err)
	}

	s.reapIdleConnections()

	var count int64
	db.Model(&models.ACPConnection{}).Count(&count)
	if count != 1 {
		t.Errorf("近期活动行被误删, 剩余 %d 行, 期望 1", count)
	}
}

// mockGoalStateNotifier 记录 GoalStateChanged 调用，用于验证 goal 清除通知。
type mockGoalStateNotifier struct {
	mu       sync.Mutex
	calls    []mockGoalCall
	notified chan struct{}
}

type mockGoalCall struct {
	dbSessionID uint
	state       *models.TaskGoalState
}

func (m *mockGoalStateNotifier) GoalStateChanged(dbSessionID uint, state *models.TaskGoalState) {
	m.mu.Lock()
	m.calls = append(m.calls, mockGoalCall{dbSessionID: dbSessionID, state: state})
	m.mu.Unlock()
	if m.notified != nil {
		select {
		case m.notified <- struct{}{}:
		default:
		}
	}
}

func (m *mockGoalStateNotifier) lastCall() (mockGoalCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return mockGoalCall{}, false
	}
	return m.calls[len(m.calls)-1], true
}

// TestCancelSessionNotifiesGoalCleared 验证 CancelSession 清除 goal 时会通知
// GoalStateNotifier（state=nil），使任务管理服务能及时写回 goal 终态，
// 避免用户手动停止后任务列表仍显示"goal 生效中"。
func TestCancelSessionNotifiesGoalCleared(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	sess := &models.Session{
		SessionID: "cancel-goal-test", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusActive,
	}
	if err := repo.Create(sess); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	notifier := &mockGoalStateNotifier{}
	svc.SetGoalStateNotifier(notifier)

	// 设定 goal
	svc.setGoal(sess.SessionID, "完成所有测试")
	if _, ok := svc.getGoal(sess.SessionID); !ok {
		t.Fatal("setGoal 后应能读到 goal")
	}

	// CancelSession 会返回 ErrSessionNotFound（无真实连接），但 goal 清除通知应已发出
	_ = svc.CancelSession(context.Background(), sess.SessionID)

	// goal 内存态应已清除
	if _, ok := svc.getGoal(sess.SessionID); ok {
		t.Error("CancelSession 后 goal 应已从内存清除")
	}

	// GoalStateNotifier 应被调用，且 state 为 nil（表示 goal 已清除）
	call, ok := notifier.lastCall()
	if !ok {
		t.Fatal("CancelSession 清除 goal 后未通知 GoalStateNotifier")
	}
	if call.dbSessionID != sess.ID {
		t.Errorf("通知的 dbSessionID = %d, 期望 %d", call.dbSessionID, sess.ID)
	}
	if call.state != nil {
		t.Errorf("通知的 state 应为 nil（goal 已清除），实际 %+v", call.state)
	}
}

// TestCancelSessionNoGoalNoNotify 验证会话无 goal 时 CancelSession 不触发通知。
func TestCancelSessionNoGoalNoNotify(t *testing.T) {
	db := setupACPTestDB(t)
	repo := repository.NewSessionRepository(db)
	sess := &models.Session{
		SessionID: "cancel-no-goal", AgentType: "claude-code", Cwd: "/tmp",
		Status: models.SessionStatusActive,
	}
	if err := repo.Create(sess); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	skills, commands, rules, subAgents := testDiscoveryConfig(t)
	svc := NewService(db, t.TempDir(), config.WorkspaceConfig{DefaultMode: "external"}, skills, commands, rules, subAgents)
	notifier := &mockGoalStateNotifier{}
	svc.SetGoalStateNotifier(notifier)

	_ = svc.CancelSession(context.Background(), sess.SessionID)

	if _, ok := notifier.lastCall(); ok {
		t.Error("会话无 goal 时 CancelSession 不应通知 GoalStateNotifier")
	}
}
