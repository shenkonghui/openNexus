package services

import (
	"context"
	"testing"

	"opennexus/internal/acp"
	"opennexus/internal/models"
)

// mockOrchExecutor 捕获传入 RunSessionTask 的 cfg，用于断言父会话透传。
type mockOrchExecutor struct {
	lastCfg acp.SessionTaskConfig
	result  acp.SessionTaskResult
}

func (m *mockOrchExecutor) RunSessionTask(_ context.Context, cfg acp.SessionTaskConfig) (acp.SessionTaskResult, error) {
	m.lastCfg = cfg
	return m.result, nil
}

func (m *mockOrchExecutor) FindWorkspaceByID(_ uint) (*models.Workspace, error) {
	return &models.Workspace{}, nil
}

func (m *mockOrchExecutor) GetSessionByDBID(_ uint) (*models.Session, error) {
	return nil, nil
}

func (m *mockOrchExecutor) DefaultAgentType() string {
	return "mock-agent"
}

// TestSetParentSession 验证 SetParentSession 将 parent_session_id 写入 tasks.json。
func TestSetParentSession(t *testing.T) {
	cwd := t.TempDir()
	svc := NewOrchestratorService(&mockOrchExecutor{})

	if err := svc.SetParentSession(cwd, 42); err != nil {
		t.Fatalf("SetParentSession: %v", err)
	}

	def, err := svc.Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if def.ParentSessionID == nil {
		t.Fatal("ParentSessionID 应已持久化，实际为 nil")
	}
	if *def.ParentSessionID != 42 {
		t.Fatalf("ParentSessionID = %d, want 42", *def.ParentSessionID)
	}
}

// TestSetParentSessionPreservedAcrossSave 验证 SetParentSession 后再保存其它字段不会丢失父会话。
func TestSetParentSessionPreservedOnUpsert(t *testing.T) {
	cwd := t.TempDir()
	svc := NewOrchestratorService(&mockOrchExecutor{})

	if err := svc.SetParentSession(cwd, 7); err != nil {
		t.Fatalf("SetParentSession: %v", err)
	}
	if err := svc.UpsertTask(cwd, models.OrchestrationTask{ID: "t1", Title: "T", Detail: "d", AgentType: "x"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	def, err := svc.Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if def.ParentSessionID == nil || *def.ParentSessionID != 7 {
		t.Fatalf("UpsertTask 后 ParentSessionID 丢失: %v", def.ParentSessionID)
	}
}

// TestExecuteTaskCarriesParentSession 验证 executeTask 将 parentSessionID 带入 SessionTaskConfig。
func TestExecuteTaskCarriesParentSession(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockOrchExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s-uuid", DBSessionID: 99}}
	svc := NewOrchestratorService(mock)

	// executeTask 需在 git 仓库内运行（创建 worktree 隔离）。
	if err := svc.InitGitRepo(cwd); err != nil {
		t.Fatalf("InitGitRepo: %v", err)
	}

	parent := uint(123)
	task := &models.OrchestrationTask{ID: "task1", Title: "T", Detail: "prompt", AgentType: "demo"}
	res, err := svc.executeTask(context.Background(), cwd, task, 5, 8, &parent)
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if !res.Success {
		t.Fatalf("executeTask result 应成功: %+v", res)
	}
	if mock.lastCfg.ParentSessionID == nil {
		t.Fatal("SessionTaskConfig.ParentSessionID 应被设置，实际为 nil")
	}
	if *mock.lastCfg.ParentSessionID != parent {
		t.Fatalf("ParentSessionID = %d, want %d", *mock.lastCfg.ParentSessionID, parent)
	}
	if mock.lastCfg.Source != models.SessionSourceOrchestration {
		t.Fatalf("Source = %q, want orchestration", mock.lastCfg.Source)
	}
}

func TestRecoverAll_MarksRunningAsInterrupt(t *testing.T) {
	cwd := t.TempDir()
	svc := NewOrchestratorService(&mockOrchExecutor{})
	def := &models.OrchestrationDef{
		MaxParallel: 1,
		Tasks: []models.OrchestrationTask{
			{ID: "a", Title: "A", Detail: "d", Status: models.OrchTaskStatusRunning},
			{ID: "b", Title: "B", Detail: "d", Status: models.OrchTaskStatusQueued},
			{ID: "c", Title: "C", Detail: "d", Status: models.OrchTaskStatusDone},
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
	if got.Tasks[0].Status != models.OrchTaskStatusInterrupt {
		t.Errorf("running → interrupt, got %q", got.Tasks[0].Status)
	}
	if got.Tasks[1].Status != models.OrchTaskStatusInterrupt {
		t.Errorf("queued → interrupt, got %q", got.Tasks[1].Status)
	}
	if got.Tasks[2].Status != models.OrchTaskStatusDone {
		t.Errorf("done 应保持, got %q", got.Tasks[2].Status)
	}
}

func TestStart_RestartsStaleRunningAfterRestart(t *testing.T) {
	// 模拟服务重启：tasks.json 仍为 running，但内存无 taskCtx；全部启动应重新排队。
	cwd := t.TempDir()
	mock := &mockOrchExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s1", DBSessionID: 1}}
	svc := NewOrchestratorService(mock)
	if err := svc.InitGitRepo(cwd); err != nil {
		t.Fatalf("InitGitRepo: %v", err)
	}
	def := &models.OrchestrationDef{
		MaxParallel: 1,
		Tasks: []models.OrchestrationTask{
			{ID: "stale", Title: "S", Detail: "prompt", AgentType: "demo", Status: models.OrchTaskStatusRunning},
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
	if got.Tasks[0].Status != models.OrchTaskStatusDone {
		t.Fatalf("残留 running 应被重新执行至 done，实际 %q err=%q", got.Tasks[0].Status, got.Tasks[0].Error)
	}
}
