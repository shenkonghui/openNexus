package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opennexus/internal/acp"
	"opennexus/internal/models"
)

// mockTMExecutor 捕获传入 RunSessionTask 的 cfg，用于断言父会话透传。
type mockTMExecutor struct {
	lastCfg          acp.SessionTaskConfig
	result           acp.SessionTaskResult
	deletedSessions  []string // 记录 DeleteSession 被调用的会话 ID
	canceledSessions []string // 记录 CancelSession 被调用的会话 ID
	lastRunStatus    string   // LastRunStatus 返回值（模拟 running_task 终态）
	activePrompt     bool     // HasActivePrompt 返回值
	enabledGoals     []string // 记录 EnableGoal 被调用的 "sessionID:condition"
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

func (m *mockTMExecutor) RunPromptOnce(_ context.Context, _, _, _ string) (string, error) {
	return "", errors.New("mock 不支持 RunPromptOnce")
}

func (m *mockTMExecutor) Prompt(_ context.Context, _, _ string) (<-chan models.Message, error) {
	ch := make(chan models.Message)
	close(ch)
	return ch, nil
}

func (m *mockTMExecutor) CancelSession(_ context.Context, sessionID string) error {
	m.canceledSessions = append(m.canceledSessions, sessionID)
	return nil
}

func (m *mockTMExecutor) DeleteSession(_ context.Context, sessionID string) error {
	m.deletedSessions = append(m.deletedSessions, sessionID)
	return nil
}

func (m *mockTMExecutor) LastRunStatus(_ uint) string {
	return m.lastRunStatus
}

func (m *mockTMExecutor) HasActivePrompt(_ string) bool {
	return m.activePrompt
}

func (m *mockTMExecutor) EnableGoal(sessionID, condition string) error {
	m.enabledGoals = append(m.enabledGoals, sessionID+":"+condition)
	return nil
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
// 使任务 session 在独立 worktree 内运行。AI 命名不可用（mock 报错）时，
// 分支名回退为标题清洗 + feat/ 前缀，worktree 目录跟随分支名。
func TestExecuteTaskUsesWorktreeCwd(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s-uuid", DBSessionID: 99}}
	svc := NewTaskManagerService(mock)

	if err := svc.InitGitRepo(cwd); err != nil {
		t.Fatalf("InitGitRepo: %v", err)
	}

	task := &models.TaskManagerTask{ID: "task1", Title: "add login", Detail: "prompt", AgentType: "demo"}
	res, err := svc.executeTask(context.Background(), cwd, task, 5, 8)
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if !res.Success {
		t.Fatalf("executeTask result 应成功: %+v", res)
	}
	wantSuffix := filepath.Join(".worktrees", "feat", "add-login")
	if mock.lastCfg.Cwd == "" || !strings.HasSuffix(mock.lastCfg.Cwd, wantSuffix) {
		t.Fatalf("Cwd = %q, 期望以 %q 结尾", mock.lastCfg.Cwd, wantSuffix)
	}
}

// TestExecuteTaskUsesExplicitBranch 验证任务显式指定 Branch 时直接沿用，
// 不走 AI 命名，worktree 目录为 .worktrees/<branch>。
func TestExecuteTaskUsesExplicitBranch(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s-uuid", DBSessionID: 99}}
	svc := NewTaskManagerService(mock)

	if err := svc.InitGitRepo(cwd); err != nil {
		t.Fatalf("InitGitRepo: %v", err)
	}

	task := &models.TaskManagerTask{ID: "task1", Title: "T", Detail: "prompt", AgentType: "demo", Branch: "fix/login-crash"}
	if _, err := svc.executeTask(context.Background(), cwd, task, 5, 8); err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	wantSuffix := filepath.Join(".worktrees", "fix", "login-crash")
	if !strings.HasSuffix(mock.lastCfg.Cwd, wantSuffix) {
		t.Fatalf("Cwd = %q, 期望以 %q 结尾", mock.lastCfg.Cwd, wantSuffix)
	}
}

// TestExecuteTask_GoalAndNoWorktree 验证任务定义的 goal 条件透传给 RunSessionTask，
// 且 NoWorktree 任务不建 worktree、直接在工作区目录运行（无需 git 仓库）。
func TestExecuteTask_GoalAndNoWorktree(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{result: acp.SessionTaskResult{Success: true, SessionID: "s-uuid", DBSessionID: 99}}
	svc := NewTaskManagerService(mock)

	task := &models.TaskManagerTask{ID: "task1", Title: "T", Detail: "prompt", AgentType: "demo",
		GoalCondition: "所有测试通过", NoWorktree: true}
	if _, err := svc.executeTask(context.Background(), cwd, task, 5, 8); err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if mock.lastCfg.Goal != "所有测试通过" {
		t.Fatalf("Goal = %q, want 所有测试通过", mock.lastCfg.Goal)
	}
	if mock.lastCfg.Cwd != cwd {
		t.Fatalf("Cwd = %q, 期望工作区目录 %q（不建 worktree）", mock.lastCfg.Cwd, cwd)
	}
}

// TestSendPrompt_RestoresGoal 验证定义了 goal 的任务在重发/继续对话前
// 会先调用 EnableGoal 恢复 goal 模式（服务重启后 goal 内存态丢失的场景）。
func TestSendPrompt_RestoresGoal(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{}
	svc := NewTaskManagerService(mock)
	def := &models.TaskManagerDef{Tasks: []models.TaskManagerTask{{
		ID: "a", Title: "A", Detail: "d", Status: models.TaskStatusInterrupt,
		SessionID: "sess-1", GoalCondition: "验收通过",
	}}}
	if err := svc.Save(cwd, def); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := svc.SendPrompt(context.Background(), cwd, "a", "继续"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if len(mock.enabledGoals) != 1 || mock.enabledGoals[0] != "sess-1:验收通过" {
		t.Fatalf("EnableGoal 调用 = %v, want [sess-1:验收通过]", mock.enabledGoals)
	}
	// 等待后台消费 goroutine 收尾（mock 返回已关闭 channel，很快结束），
	// 避免其写回 tasks.json 与 TempDir 清理竞争。
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := svc.Load(cwd)
		if err == nil && len(got.Tasks) == 1 && got.Tasks[0].Status != models.TaskStatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待后台收尾超时")
		}
		time.Sleep(10 * time.Millisecond)
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

// TestFirstLine 验证任务标题兜底逻辑：剥离 slash 命令前缀、按 rune 截断避免中文乱码。
func TestFirstLine(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		maxLen int
		want   string
	}{
		{"普通首行", "帮我重构 router.go\n第二行", 40, "帮我重构 router.go"},
		{"剥离命令前缀", "/opennexus-goal docker context 设置为 ssh root@k3s 上的docker", 40, "docker context 设置为 ssh root@k3s 上的docker"},
		{"纯命令无参数保留原样", "/help", 40, "/help"},
		{"中文按rune截断不产生乱码", "/opennexus-goal " + strings.Repeat("设置", 30), 10, "设置设置设置设置设置"},
		{"maxLen为0不截断", "abc", 0, "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.prompt, tc.maxLen); got != tc.want {
				t.Errorf("firstLine(%q, %d) = %q, want %q", tc.prompt, tc.maxLen, got, tc.want)
			}
		})
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
// tasks.json 中按 db_session_id 登记的任务，与 RegisterSessionTask 对称；
// 会话本身正在被删除，不应再回头调用 DeleteSession。
func TestUnregisterSessionTask_RemovesRegisteredTask(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{}
	svc := NewTaskManagerService(mock)
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
	if len(mock.deletedSessions) != 0 {
		t.Fatalf("会话发起的注销不应再删会话，实际删了 %v", mock.deletedSessions)
	}
}

// TestDeleteTask_DeletesLinkedSession 验证删除任务时连带删除其关联会话，
// 使左侧任务列表（读 DB 会话）与右侧任务列表（读 tasks.json）保持同步。
func TestDeleteTask_DeletesLinkedSession(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{}
	svc := NewTaskManagerService(mock)
	dbID := uint(88)
	if err := svc.UpsertTask(cwd, models.TaskManagerTask{ID: "t1", Title: "T", Detail: "d"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := svc.storeFor(cwd).UpdateTaskStatus("t1", func(tk *models.TaskManagerTask) {
		tk.SessionID = "acp-uuid-88"
		tk.DBSessionID = &dbID
	}); err != nil {
		t.Fatalf("写入会话关联: %v", err)
	}
	if err := svc.DeleteTask(cwd, "t1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	def, _ := svc.Load(cwd)
	if len(def.Tasks) != 0 {
		t.Fatalf("删除后 tasks 数量 = %d, want 0", len(def.Tasks))
	}
	if len(mock.deletedSessions) != 1 || mock.deletedSessions[0] != "acp-uuid-88" {
		t.Fatalf("应连带删除关联会话 acp-uuid-88，实际 %v", mock.deletedSessions)
	}
}

// TestDeleteTask_NoSessionIsNoop 验证无关联会话的任务删除时不调用 DeleteSession。
func TestDeleteTask_NoSessionIsNoop(t *testing.T) {
	cwd := t.TempDir()
	mock := &mockTMExecutor{}
	svc := NewTaskManagerService(mock)
	if err := svc.UpsertTask(cwd, models.TaskManagerTask{ID: "t1", Title: "T", Detail: "d"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if err := svc.DeleteTask(cwd, "t1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if len(mock.deletedSessions) != 0 {
		t.Fatalf("无关联会话不应调用 DeleteSession，实际 %v", mock.deletedSessions)
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

// pfExecutor 为 PromptFinished 测试提供会话→工作区 cwd 解析。
type pfExecutor struct {
	mockTMExecutor
	cwd string
}

func (m *pfExecutor) GetSessionByDBID(id uint) (*models.Session, error) {
	wsID := uint(1)
	return &models.Session{ID: id, WorkspaceID: &wsID, Source: models.SessionSourceManual}, nil
}

func (m *pfExecutor) FindWorkspaceByID(_ uint) (*models.Workspace, error) {
	return &models.Workspace{Cwd: m.cwd}, nil
}

// TestPromptFinished_SyncsRegisteredSessionTask 验证会话登记任务的完整状态闭环：
// 首发登记 running → PromptFinished(done) 置 done → 再次发送刷新回 running →
// PromptFinished(interrupted) 置 interrupt。
func TestPromptFinished_SyncsRegisteredSessionTask(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&pfExecutor{cwd: cwd})
	sess := &models.Session{ID: 9, SessionID: "s-9", Source: models.SessionSourceManual}
	if err := svc.RegisterSessionTask(cwd, sess, "p"); err != nil {
		t.Fatalf("登记: %v", err)
	}

	svc.PromptFinished(9, models.RunningTaskStatusDone)
	def, _ := svc.Load(cwd)
	if def.Tasks[0].Status != models.TaskStatusDone {
		t.Fatalf("prompt 结束后应置 done，实际 %q", def.Tasks[0].Status)
	}
	if def.Tasks[0].FinishedAt == nil {
		t.Error("finished_at 应已设置")
	}

	// 同一会话再次发送 prompt：不新增条目，状态刷新回 running
	if err := svc.RegisterSessionTask(cwd, sess, "again"); err != nil {
		t.Fatalf("重复登记: %v", err)
	}
	def, _ = svc.Load(cwd)
	if len(def.Tasks) != 1 {
		t.Fatalf("任务数 = %d, want 1", len(def.Tasks))
	}
	if def.Tasks[0].Status != models.TaskStatusRunning {
		t.Fatalf("再次发送后应回到 running，实际 %q", def.Tasks[0].Status)
	}
	if def.Tasks[0].FinishedAt != nil {
		t.Error("再次发送后 finished_at 应清空")
	}

	svc.PromptFinished(9, models.RunningTaskStatusInterrupted)
	def, _ = svc.Load(cwd)
	if def.Tasks[0].Status != models.TaskStatusInterrupt {
		t.Fatalf("中断结束应置 interrupt，实际 %q", def.Tasks[0].Status)
	}
}

// TestPromptFinished_NoMatchIsNoop 验证无匹配条目/无法解析会话时不 panic、不影响其他任务。
func TestPromptFinished_NoMatchIsNoop(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&pfExecutor{cwd: cwd})
	if err := svc.UpsertTask(cwd, models.TaskManagerTask{ID: "t1", Title: "普通任务"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	svc.PromptFinished(999, models.RunningTaskStatusDone)
	def, _ := svc.Load(cwd)
	if len(def.Tasks) != 1 || def.Tasks[0].Status == models.TaskStatusDone {
		t.Fatalf("无关任务不应受影响: %+v", def.Tasks)
	}
	// GetSessionByDBID 返回 nil（mockTMExecutor 默认行为）也应安全
	svc2 := NewTaskManagerService(&mockTMExecutor{})
	svc2.PromptFinished(1, models.RunningTaskStatusDone)
}

// TestPromptFinished_GoalActiveKeepsRunning 验证 goal 生效期间 turn 正常结束不置 done，
// 保持 running 等 goal 审核（由 GoalStateChanged 收尾）；interrupted 收尾不受 goal 门控，
// 仍置 interrupt 可重发。
func TestPromptFinished_GoalActiveKeepsRunning(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&pfExecutor{cwd: cwd})
	sess := &models.Session{ID: 9, SessionID: "s-9", Source: models.SessionSourceManual}
	if err := svc.RegisterSessionTask(cwd, sess, "p"); err != nil {
		t.Fatalf("登记: %v", err)
	}
	if err := svc.storeFor(cwd).UpdateTaskStatus("9", func(tk *models.TaskManagerTask) {
		tk.Goal = &models.TaskGoalState{Condition: "c", Status: models.TaskGoalStatusActive}
	}); err != nil {
		t.Fatalf("写入 goal: %v", err)
	}

	svc.PromptFinished(9, models.RunningTaskStatusDone)
	def, _ := svc.Load(cwd)
	if def.Tasks[0].Status != models.TaskStatusRunning {
		t.Fatalf("goal 生效中 turn 结束应保持 running，实际 %q", def.Tasks[0].Status)
	}
	if def.Tasks[0].FinishedAt != nil {
		t.Error("goal 生效中不应设置 finished_at")
	}

	svc.PromptFinished(9, models.RunningTaskStatusInterrupted)
	def, _ = svc.Load(cwd)
	if def.Tasks[0].Status != models.TaskStatusInterrupt {
		t.Fatalf("中断收尾不受 goal 门控，应置 interrupt，实际 %q", def.Tasks[0].Status)
	}
}

// TestGoalStateChanged_AchievedSetsDone 验证 goal 达成时任务联动置 done 并写入快照。
func TestGoalStateChanged_AchievedSetsDone(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&pfExecutor{cwd: cwd})
	sess := &models.Session{ID: 9, SessionID: "s-9", Source: models.SessionSourceManual}
	if err := svc.RegisterSessionTask(cwd, sess, "p"); err != nil {
		t.Fatalf("登记: %v", err)
	}

	svc.GoalStateChanged(9, &models.TaskGoalState{Condition: "c", Status: models.TaskGoalStatusAchieved, EvalCount: 1})
	def, _ := svc.Load(cwd)
	tk := def.Tasks[0]
	if tk.Status != models.TaskStatusDone {
		t.Fatalf("goal 达成应置 done，实际 %q", tk.Status)
	}
	if tk.FinishedAt == nil {
		t.Error("finished_at 应已设置")
	}
	if tk.Goal == nil || tk.Goal.Status != models.TaskGoalStatusAchieved || tk.Goal.EvalCount != 1 {
		t.Fatalf("goal 快照应写入任务: %+v", tk.Goal)
	}
}

// TestGoalStateChanged_StoppedSetsFailed 验证 goal 终止时任务置 failed，
// Error 记录终止原因与审计次数。
func TestGoalStateChanged_StoppedSetsFailed(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&pfExecutor{cwd: cwd})
	sess := &models.Session{ID: 9, SessionID: "s-9", Source: models.SessionSourceManual}
	if err := svc.RegisterSessionTask(cwd, sess, "p"); err != nil {
		t.Fatalf("登记: %v", err)
	}

	svc.GoalStateChanged(9, &models.TaskGoalState{Condition: "c", Status: models.TaskGoalStatusStopped, LastReason: "超出最大轮数", EvalCount: 3})
	def, _ := svc.Load(cwd)
	tk := def.Tasks[0]
	if tk.Status != models.TaskStatusFailed {
		t.Fatalf("goal 终止应置 failed，实际 %q", tk.Status)
	}
	if !strings.Contains(tk.Error, "超出最大轮数") || !strings.Contains(tk.Error, "已审计 3 次") {
		t.Fatalf("Error 应含终止原因与审计次数，实际 %q", tk.Error)
	}
}

// TestGoalStateChanged_ClearSettlesRunningTask 验证手动 /goal clear（state 为 nil）：
// 未达终态的 goal 快照置 stopped；无进行中 prompt 时 running 任务直接结算为 done，
// prompt 进行中则保持 running，由 turn 结束时自然收尾。
func TestGoalStateChanged_ClearSettlesRunningTask(t *testing.T) {
	setup := func(active bool) (*TaskManagerService, string) {
		cwd := t.TempDir()
		exec := &pfExecutor{cwd: cwd}
		exec.activePrompt = active
		svc := NewTaskManagerService(exec)
		sess := &models.Session{ID: 9, SessionID: "s-9", Source: models.SessionSourceManual}
		if err := svc.RegisterSessionTask(cwd, sess, "p"); err != nil {
			t.Fatalf("登记: %v", err)
		}
		if err := svc.storeFor(cwd).UpdateTaskStatus("9", func(tk *models.TaskManagerTask) {
			tk.Goal = &models.TaskGoalState{Condition: "c", Status: models.TaskGoalStatusActive}
		}); err != nil {
			t.Fatalf("写入 goal: %v", err)
		}
		return svc, cwd
	}

	// 无进行中 prompt：直接结算为 done
	svc, cwd := setup(false)
	svc.GoalStateChanged(9, nil)
	def, _ := svc.Load(cwd)
	tk := def.Tasks[0]
	if tk.Goal == nil || tk.Goal.Status != models.TaskGoalStatusStopped || tk.Goal.LastReason != "已手动清除" {
		t.Fatalf("清除后 goal 快照应置 stopped/已手动清除: %+v", tk.Goal)
	}
	if tk.Status != models.TaskStatusDone {
		t.Fatalf("无进行中 prompt 时清除 goal 应结算为 done，实际 %q", tk.Status)
	}

	// prompt 进行中：保持 running 由 turn 结束收尾
	svc2, cwd2 := setup(true)
	svc2.GoalStateChanged(9, nil)
	def2, _ := svc2.Load(cwd2)
	if def2.Tasks[0].Status != models.TaskStatusRunning {
		t.Fatalf("prompt 进行中清除 goal 应保持 running，实际 %q", def2.Tasks[0].Status)
	}
}

// TestRecoverAll_StopsZombieGoal 验证重启恢复时残留的 active/evaluating goal 快照
// 被置 stopped（goal 为会话内存态不跨重启），已达成的末态保留。
func TestRecoverAll_StopsZombieGoal(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	def := &models.TaskManagerDef{
		MaxParallel: 1,
		Tasks: []models.TaskManagerTask{
			{ID: "a", Title: "A", Detail: "d", Status: models.TaskStatusRunning,
				Goal: &models.TaskGoalState{Condition: "c", Status: models.TaskGoalStatusActive}},
			{ID: "b", Title: "B", Detail: "d", Status: models.TaskStatusDone,
				Goal: &models.TaskGoalState{Condition: "c", Status: models.TaskGoalStatusAchieved}},
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
	if g := got.Tasks[0].Goal; g == nil || g.Status != models.TaskGoalStatusStopped || g.LastReason != "服务重启中断" {
		t.Errorf("残留 active goal 应置 stopped/服务重启中断: %+v", g)
	}
	if g := got.Tasks[1].Goal; g == nil || g.Status != models.TaskGoalStatusAchieved {
		t.Errorf("已达成 goal 末态应保留: %+v", g)
	}
}

// TestRecoverAll_ReturnsInterruptedGoalTasks 验证 RecoverAll 仅返回被标记中断且
// 定义了 goal 的任务（供重启后自动续跑），普通任务与未运行任务不返回。
func TestRecoverAll_ReturnsInterruptedGoalTasks(t *testing.T) {
	cwd := t.TempDir()
	svc := NewTaskManagerService(&mockTMExecutor{})
	def := &models.TaskManagerDef{
		MaxParallel: 1,
		Tasks: []models.TaskManagerTask{
			{ID: "a", Title: "A", Detail: "d", Status: models.TaskStatusRunning, GoalCondition: "完成", SessionID: "sess-a"},
			{ID: "b", Title: "B", Detail: "d", Status: models.TaskStatusQueued, GoalCondition: "完成"},
			{ID: "c", Title: "C", Detail: "d", Status: models.TaskStatusRunning},                   // 无 goal 不自动续跑
			{ID: "d", Title: "D", Detail: "d", Status: models.TaskStatusDone, GoalCondition: "完成"}, // 未运行不返回
		},
	}
	if err := svc.Save(cwd, def); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := svc.RecoverAll([]string{cwd})
	if len(got) != 2 {
		t.Fatalf("应返回 2 个带 goal 的中断任务，实际 %d: %+v", len(got), got)
	}
	if got[0].TaskID != "a" || !got[0].HasSession || got[0].Cwd != cwd {
		t.Errorf("任务 a 应带会话可继续对话: %+v", got[0])
	}
	if got[1].TaskID != "b" || got[1].HasSession {
		t.Errorf("任务 b 无会话应重新启动: %+v", got[1])
	}
}
