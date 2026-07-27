package services

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"opennexus/internal/acp"
	"opennexus/internal/models"
)

// mockTMExecutor 捕获传入 RunSessionTask 的 cfg，用于断言父会话透传。
type mockTMExecutor struct {
	lastCfg acp.SessionTaskConfig
	result  acp.SessionTaskResult
}

func (m *mockTMExecutor) RunSessionTask(_ context.Context, cfg acp.SessionTaskConfig) (acp.SessionTaskResult, error) {
	m.lastCfg = cfg
	return m.result, nil
}

func (m *mockTMExecutor) FindWorkspaceByID(_ uint) (*models.Workspace, error) {
	return &models.Workspace{}, nil
}

func (m *mockTMExecutor) GetSessionByDBID(_ uint) (*models.Session, error) {
	return nil, nil
}

func (m *mockTMExecutor) DefaultAgentType() string {
	return "mock-agent"
}

// TestExecuteTaskUsesManualSource 验证 executeTask 创建的会话 source 为 manual，
// 且不再携带 ParentSessionID（编排管理会话概念已取消）。
func TestExecuteTaskUsesManualSource(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s-uuid", DBSessionID: 99}}
	svc := NewTaskManagerService(mock)

	// executeTask 需在 git 仓库内运行（创建 worktree 隔离）。
	if err := svc.InitGitRepo(cwd); err != nil {
		t.Fatalf("InitGitRepo: %v", err)
	}

	task := &models.TaskManagerTask{ID: "task1", Title: "T", Detail: "prompt", AgentType: "demo"}
	res, err := svc.executeTask(context.Background(), cwd, task, 5, 8)
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if !res.Success {
		t.Fatalf("executeTask result 应成功: %+v", res)
	}
	if mock.lastCfg.ParentSessionID != nil {
		t.Fatalf("ParentSessionID 应为 nil，实际 %v", *mock.lastCfg.ParentSessionID)
	}
	if mock.lastCfg.Source != models.SessionSourceManual {
		t.Fatalf("Source = %q, want manual", mock.lastCfg.Source)
	}
}

// TestExecuteTaskUsesWorktreeCwd 验证 executeTask 将 worktree 路径作为 Cwd 传给 RunSessionTask，
// 使任务 session 在独立 worktree 内运行。
func TestExecuteTaskUsesWorktreeCwd(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s-uuid", DBSessionID: 99}}
	svc := NewTaskManagerService(mock)

	if err := svc.InitGitRepo(cwd); err != nil {
		t.Fatalf("InitGitRepo: %v", err)
	}

	task := &models.TaskManagerTask{ID: "task1", Title: "T", Detail: "prompt", AgentType: "demo"}
	res, err := svc.executeTask(context.Background(), cwd, task, 5, 8)
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if !res.Success {
		t.Fatalf("executeTask result 应成功: %+v", res)
	}
	wantSuffix := filepath.Join(".worktrees", "task1")
	if mock.lastCfg.Cwd == "" || !strings.HasSuffix(mock.lastCfg.Cwd, wantSuffix) {
		t.Fatalf("Cwd = %q, 期望以 %q 结尾", mock.lastCfg.Cwd, wantSuffix)
	}
}

func TestRecoverAll_MarksRunningAsInterrupt(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	def := &models.TaskManagerDef{
		MaxParallel: 1,
		Tasks: []models.TaskManagerTask{
			{ID: "a", Title: "A", Detail: "d", Status: models.TaskStatusRunning},
			{ID: "b", Title: "B", Detail: "d", Status: models.TaskStatusQueued},
			{ID: "c", Title: "C", Detail: "d", Status: models.TaskStatusDone},
		},
	}
	if err := svc.Save(cwd, def); err != nil {
		t.Fatalf("Save: %v", err)
	}
	svc.RecoverAll([]string{cwd})
	got, err := svc.Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Tasks[0].Status != models.TaskStatusInterrupt {
		t.Errorf("running → interrupt, got %q", got.Tasks[0].Status)
	}
	if got.Tasks[1].Status != models.TaskStatusInterrupt {
		t.Errorf("queued → interrupt, got %q", got.Tasks[1].Status)
	}
	if got.Tasks[2].Status != models.TaskStatusDone {
		t.Errorf("done 应保持, got %q", got.Tasks[2].Status)
	}
}

func TestStart_RestartsStaleRunningAfterRestart(t *testing.T) {
	// 模拟服务重启：tasks.json 仍为 running，但内存无 taskCtx；全部启动应重新排队。
	cwd := t.TempDir()
	mock := &mockTMExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s1", DBSessionID: 1}}
	svc := NewTaskManagerService(mock)
	if err := svc.InitGitRepo(cwd); err != nil {
		t.Fatalf("InitGitRepo: %v", err)
	}
	def := &models.TaskManagerDef{
		MaxParallel: 1,
		Tasks: []models.TaskManagerTask{
			{ID: "stale", Title: "S", Detail: "prompt", AgentType: "demo", Status: models.TaskStatusRunning},
		},
	}
	if err := svc.Save(cwd, def); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := svc.Start(context.Background(), cwd, 1, 1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 等 runTask 结束
	svc.mu.Lock()
	run := svc.runs[cwd]
	svc.mu.Unlock()
	if run != nil {
		run.wg.Wait()
	}
	got, _ := svc.Load(cwd)
	if got.Tasks[0].Status != models.TaskStatusDone {
		t.Fatalf("残留 running 应被重新执行至 done，实际 %q err=%q", got.Tasks[0].Status, got.Tasks[0].Error)
	}
}

// TestRegisterSessionTask_NewManualSession 验证手动新建会话首次发送时被登记到 tasks.json，
// 字段含 db_session_id/session_id/status=running/started_at，task.id 为会话 DB 主键字符串。
func TestRegisterSessionTask_NewManualSession(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	sess := &models.Session{
		ID:         123,
		SessionID:  "acp-uuid-123",
		AgentType:  "mock-agent",
		ModelValue: "gpt-4o",
		Source:     models.SessionSourceManual,
		Title:      "",
	}
	if err := svc.RegisterSessionTask(cwd, sess, "帮我重构 router.go\n第二行"); err != nil {
		t.Fatalf("RegisterSessionTask: %v", err)
	}
	def, err := svc.Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(def.Tasks) != 1 {
		t.Fatalf("tasks 数量 = %d, want 1", len(def.Tasks))
	}
	tk := def.Tasks[0]
	if tk.ID != "123" {
		t.Errorf("task.id = %q, want 123", tk.ID)
	}
	if tk.Title != "帮我重构 router.go" {
		t.Errorf("task.title = %q, want 首行截断", tk.Title)
	}
	if tk.Detail != "帮我重构 router.go\n第二行" {
		t.Errorf("task.detail = %q, want 原始 prompt", tk.Detail)
	}
	if tk.AgentType != "mock-agent" || tk.ModelValue != "gpt-4o" {
		t.Errorf("agent/model 不匹配: %+v", tk)
	}
	if tk.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", tk.Status)
	}
	if tk.DBSessionID == nil || *tk.DBSessionID != 123 {
		t.Errorf("db_session_id = %v, want 123", tk.DBSessionID)
	}
	if tk.SessionID != "acp-uuid-123" {
		t.Errorf("session_id = %q, want acp-uuid-123", tk.SessionID)
	}
	if tk.StartedAt == nil {
		t.Errorf("started_at 应已设置")
	}
}

// TestRegisterSessionTask_DedupByDBSessionID 验证按 db_session_id 去重：重复登记不新增条目。
func TestRegisterSessionTask_DedupByDBSessionID(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	sess := &models.Session{ID: 55, SessionID: "s-55", AgentType: "a", Source: models.SessionSourceManual}
	if err := svc.RegisterSessionTask(cwd, sess, "first"); err != nil {
		t.Fatalf("首次登记: %v", err)
	}
	if err := svc.RegisterSessionTask(cwd, sess, "second"); err != nil {
		t.Fatalf("重复登记: %v", err)
	}
	def, _ := svc.Load(cwd)
	if len(def.Tasks) != 1 {
		t.Fatalf("重复登记后 tasks 数量 = %d, want 1", len(def.Tasks))
	}
	if def.Tasks[0].Detail != "first" {
		t.Errorf("重复登记不应覆盖原任务，detail = %q", def.Tasks[0].Detail)
	}
}

// TestRegisterSessionTask_SkipsNonManual 验证 scheduled/classify/子会话不登记。
func TestRegisterSessionTask_SkipsNonManual(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	parent := uint(7)
	cases := []struct {
		name string
		sess *models.Session
	}{
		{"scheduled", &models.Session{ID: 1, Source: models.SessionSourceScheduled}},
		{"classify", &models.Session{ID: 2, Source: models.SessionSourceClassify}},
		{"child session", &models.Session{ID: 3, Source: models.SessionSourceManual, ParentSessionID: &parent}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.RegisterSessionTask(cwd, tc.sess, "p"); err != nil {
				t.Fatalf("RegisterSessionTask: %v", err)
			}
			def, _ := svc.Load(cwd)
			if len(def.Tasks) != 0 {
				t.Fatalf("%s: tasks 应为空, 实际 %d", tc.name, len(def.Tasks))
			}
		})
	}
}

// TestRegisterSessionTask_EmptyInputs 验证空 cwd / nil sess 不报错。
func TestRegisterSessionTask_EmptyInputs(t *testing.T) {
	svc := NewTaskManagerService(&mockTMExecutor{})
	if err := svc.RegisterSessionTask("", &models.Session{ID: 1, Source: models.SessionSourceManual}, "p"); err != nil {
		t.Fatalf("空 cwd 应无操作: %v", err)
	}
	if err := svc.RegisterSessionTask(t.TempDir(), nil, "p"); err != nil {
		t.Fatalf("nil sess 应无操作: %v", err)
	}
}

// TestUnregisterSessionTask_RemovesRegisteredTask 验证删除会话时同步移除
// tasks.json 中按 db_session_id 登记的任务，与 RegisterSessionTask 对称。
func TestUnregisterSessionTask_RemovesRegisteredTask(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	sess := &models.Session{ID: 77, SessionID: "s-77", AgentType: "a", Source: models.SessionSourceManual}
	if err := svc.RegisterSessionTask(cwd, sess, "prompt"); err != nil {
		t.Fatalf("登记: %v", err)
	}
	if err := svc.UnregisterSessionTask(cwd, sess.ID); err != nil {
		t.Fatalf("UnregisterSessionTask: %v", err)
	}
	def, err := svc.Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(def.Tasks) != 0 {
		t.Fatalf("注销后 tasks 数量 = %d, want 0", len(def.Tasks))
	}
}

// TestUnregisterSessionTask_NoMatchIsNoop 验证无匹配条目时为无操作：
// 不报错且不影响其他任务；空 cwd / 零 id 同样安全。
func TestUnregisterSessionTask_NoMatchIsNoop(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	if err := svc.UpsertTask(cwd, models.TaskManagerTask{ID: "t1", Title: "普通任务"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := svc.UnregisterSessionTask(cwd, 999); err != nil {
		t.Fatalf("无匹配应返回 nil: %v", err)
	}
	def, _ := svc.Load(cwd)
	if len(def.Tasks) != 1 {
		t.Fatalf("无关任务不应被删除，剩余 %d, want 1", len(def.Tasks))
	}
	if err := svc.UnregisterSessionTask("", 1); err != nil {
		t.Fatalf("空 cwd 应无操作: %v", err)
	}
	if err := svc.UnregisterSessionTask(cwd, 0); err != nil {
		t.Fatalf("零 id 应无操作: %v", err)
	}
}
