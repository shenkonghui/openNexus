package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"opennexus/internal/acp"
	"opennexus/internal/models"
)

// reviewMockExecutor 是 review 单测专用 mock：可编排 reviewer 每轮输出与修复对话回复。
type reviewMockExecutor struct {
	mockTMExecutor
	subAgentResponses []string // RunSubAgent 依次返回；耗尽后复用最后一个
	subAgentErr       error
	subAgentCalls     int
	subAgentPrompts   []string
	fixReplies        []string // 每次 Prompt（修复对话）发回的 agent 回复文本
	fixCalls          int
	lastFixPrompt     string
}

func (m *reviewMockExecutor) RunSubAgent(_ context.Context, cfg acp.SubAgentRunConfig) (string, error) {
	m.subAgentCalls++
	m.subAgentPrompts = append(m.subAgentPrompts, cfg.Prompt)
	if m.subAgentErr != nil {
		return "", m.subAgentErr
	}
	if len(m.subAgentResponses) == 0 {
		return "", errors.New("mock 未配置 reviewer 输出")
	}
	idx := m.subAgentCalls - 1
	if idx >= len(m.subAgentResponses) {
		idx = len(m.subAgentResponses) - 1
	}
	return m.subAgentResponses[idx], nil
}

func (m *reviewMockExecutor) Prompt(_ context.Context, _, prompt string) (<-chan models.Message, error) {
	m.fixCalls++
	m.lastFixPrompt = prompt
	reply := "已修复"
	if m.fixCalls-1 < len(m.fixReplies) {
		reply = m.fixReplies[m.fixCalls-1]
	}
	ch := make(chan models.Message, 1)
	ch <- models.Message{Kind: models.MessageKindAgentMessageChunk, Content: reply}
	close(ch)
	return ch, nil
}

// mockReviewSettings 提供固定的全局 review 配置。
type mockReviewSettings struct {
	settings *models.TaskSettings
	err      error
}

func (m *mockReviewSettings) FindByUserID(_ uint) (*models.TaskSettings, error) {
	return m.settings, m.err
}

// setupReviewSvc 构造带指定任务的 TaskManagerService 与 tasks.json。
func setupReviewSvc(t *testing.T, exec TaskManagerExecutor, task models.TaskManagerTask) (*TaskManagerService, string) {
	t.Helper()
	cwd := t.TempDir()
	svc := NewTaskManagerService(exec)
	def := &models.TaskManagerDef{MaxParallel: 1, Tasks: []models.TaskManagerTask{task}}
	if err := svc.Save(cwd, def); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return svc, cwd
}

func loadReviewTask(t *testing.T, svc *TaskManagerService, cwd string) models.TaskManagerTask {
	t.Helper()
	def, err := svc.Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(def.Tasks) != 1 {
		t.Fatalf("tasks 数量 = %d, want 1", len(def.Tasks))
	}
	return def.Tasks[0]
}

var reviewOKResult = acp.SessionTaskResult{Success: true, SessionID: "s-uuid", DBSessionID: 42, Result: "agent 最终回复"}

// TestReviewTask_DisabledSetsDone 验证未启用 review 时直接置 done 并写回会话关联。
func TestReviewTask_DisabledSetsDone(t *testing.T) {
	mock := &reviewMockExecutor{}
	task := models.TaskManagerTask{ID: "t1", Title: "T", Detail: "d", Status: models.TaskStatusRunning}
	svc, cwd := setupReviewSvc(t, mock, task)

	svc.reviewTask(context.Background(), cwd, &task, 1, reviewOKResult)

	got := loadReviewTask(t, svc, cwd)
	if got.Status != models.TaskStatusDone {
		t.Fatalf("Status = %q, want done", got.Status)
	}
	if got.SessionID != "s-uuid" || got.DBSessionID == nil || *got.DBSessionID != 42 {
		t.Fatalf("会话关联未写回: %+v", got)
	}
	if mock.subAgentCalls != 0 {
		t.Fatalf("review 未启用不应调用 reviewer，实际 %d 次", mock.subAgentCalls)
	}
}

// TestReviewTask_PassFirstRound 验证首轮审查通过即置 done 并写回 review 结果。
func TestReviewTask_PassFirstRound(t *testing.T) {
	mock := &reviewMockExecutor{subAgentResponses: []string{`{"passed": true, "feedback": ""}`}}
	task := models.TaskManagerTask{
		ID: "t1", Title: "标题", Detail: "要求", Status: models.TaskStatusRunning,
		Review: &models.TaskReviewConfig{Enabled: true},
	}
	svc, cwd := setupReviewSvc(t, mock, task)

	svc.reviewTask(context.Background(), cwd, &task, 1, reviewOKResult)

	got := loadReviewTask(t, svc, cwd)
	if got.Status != models.TaskStatusDone {
		t.Fatalf("Status = %q, want done", got.Status)
	}
	if got.ReviewRounds != 1 || got.ReviewPassed == nil || !*got.ReviewPassed {
		t.Fatalf("review 结果未写回: rounds=%d passed=%v", got.ReviewRounds, got.ReviewPassed)
	}
	if mock.fixCalls != 0 {
		t.Fatalf("通过后不应追加修复对话，实际 %d 次", mock.fixCalls)
	}
	// 审查 prompt 应包含任务标题/要求/agent 回复占位内容
	p := mock.subAgentPrompts[0]
	for _, want := range []string{"标题", "要求", "agent 最终回复"} {
		if !strings.Contains(p, want) {
			t.Errorf("审查 prompt 缺少 %q", want)
		}
	}
}

// TestReviewTask_FailThenPass 验证首轮未通过时把审查意见追加回原会话，二轮通过置 done。
func TestReviewTask_FailThenPass(t *testing.T) {
	mock := &reviewMockExecutor{subAgentResponses: []string{
		"```json\n{\"passed\": false, \"feedback\": \"缺少单测\"}\n```",
		`{"passed": true, "feedback": ""}`,
	}}
	task := models.TaskManagerTask{
		ID: "t1", Title: "T", Detail: "d", Status: models.TaskStatusRunning,
		Review: &models.TaskReviewConfig{Enabled: true},
	}
	svc, cwd := setupReviewSvc(t, mock, task)

	svc.reviewTask(context.Background(), cwd, &task, 1, reviewOKResult)

	got := loadReviewTask(t, svc, cwd)
	if got.Status != models.TaskStatusDone {
		t.Fatalf("Status = %q, want done", got.Status)
	}
	if got.ReviewRounds != 2 || got.ReviewPassed == nil || !*got.ReviewPassed {
		t.Fatalf("review 结果错误: rounds=%d passed=%v", got.ReviewRounds, got.ReviewPassed)
	}
	if mock.fixCalls != 1 {
		t.Fatalf("修复对话次数 = %d, want 1", mock.fixCalls)
	}
	if !strings.Contains(mock.lastFixPrompt, "缺少单测") {
		t.Fatalf("修复 prompt 应包含审查意见，实际 %q", mock.lastFixPrompt)
	}
}

// TestReviewTask_RoundsExhaustedFails 验证轮数耗尽仍未通过时置 failed，Error 含最后一轮意见。
func TestReviewTask_RoundsExhaustedFails(t *testing.T) {
	mock := &reviewMockExecutor{subAgentResponses: []string{`{"passed": false, "feedback": "还差国际化"}`}}
	task := models.TaskManagerTask{
		ID: "t1", Title: "T", Detail: "d", Status: models.TaskStatusRunning,
		Review: &models.TaskReviewConfig{Enabled: true, MaxRounds: 2},
	}
	svc, cwd := setupReviewSvc(t, mock, task)

	svc.reviewTask(context.Background(), cwd, &task, 1, reviewOKResult)

	got := loadReviewTask(t, svc, cwd)
	if got.Status != models.TaskStatusFailed {
		t.Fatalf("Status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "还差国际化") {
		t.Fatalf("Error 应包含审查意见，实际 %q", got.Error)
	}
	if got.ReviewRounds != 2 || got.ReviewPassed == nil || *got.ReviewPassed {
		t.Fatalf("review 结果错误: rounds=%d passed=%v", got.ReviewRounds, got.ReviewPassed)
	}
	if mock.subAgentCalls != 2 {
		t.Fatalf("审查次数 = %d, want 2", mock.subAgentCalls)
	}
}

// TestReviewTask_NonJSONTreatedAsPassed 验证 reviewer 输出不可解析时容错视为通过。
func TestReviewTask_NonJSONTreatedAsPassed(t *testing.T) {
	mock := &reviewMockExecutor{subAgentResponses: []string{"我觉得做得不错！"}}
	task := models.TaskManagerTask{
		ID: "t1", Title: "T", Detail: "d", Status: models.TaskStatusRunning,
		Review: &models.TaskReviewConfig{Enabled: true},
	}
	svc, cwd := setupReviewSvc(t, mock, task)

	svc.reviewTask(context.Background(), cwd, &task, 1, reviewOKResult)

	got := loadReviewTask(t, svc, cwd)
	if got.Status != models.TaskStatusDone {
		t.Fatalf("Status = %q, want done", got.Status)
	}
	if got.ReviewPassed == nil || !*got.ReviewPassed {
		t.Fatalf("解析失败应容错视为通过: %+v", got.ReviewPassed)
	}
}

// TestReviewTask_CanceledCtxLeavesStatus 验证 ctx 取消（Stop）时立即退出，
// 不改写状态（canceled 由 Stop 负责写入）。
func TestReviewTask_CanceledCtxLeavesStatus(t *testing.T) {
	mock := &reviewMockExecutor{subAgentErr: context.Canceled}
	task := models.TaskManagerTask{
		ID: "t1", Title: "T", Detail: "d", Status: models.TaskStatusRunning,
		Review: &models.TaskReviewConfig{Enabled: true},
	}
	svc, cwd := setupReviewSvc(t, mock, task)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.reviewTask(ctx, cwd, &task, 1, reviewOKResult)

	got := loadReviewTask(t, svc, cwd)
	if got.Status != models.TaskStatusReviewing {
		t.Fatalf("Status = %q, 取消后应停在 reviewing 由 Stop 收尾", got.Status)
	}
}

// TestEffectiveReviewConfig_GlobalAndOverride 验证全局默认与任务级覆盖的合成规则。
func TestEffectiveReviewConfig_GlobalAndOverride(t *testing.T) {
	mock := &reviewMockExecutor{}
	svc := NewTaskManagerService(mock)
	svc.SetReviewSettingsSource(&mockReviewSettings{settings: &models.TaskSettings{
		ReviewEnabled:    true,
		ReviewAgentType:  "global-agent",
		ReviewModelValue: "global-model",
		ReviewMaxRounds:  3,
	}})

	// 任务未覆盖：跟随全局
	rc := svc.effectiveReviewConfig(&models.TaskManagerTask{ID: "t1", AgentType: "task-agent"}, 1)
	if !rc.Enabled || rc.AgentType != "global-agent" || rc.ModelValue != "global-model" || rc.MaxRounds != 3 {
		t.Fatalf("全局配置合成错误: %+v", rc)
	}
	if rc.Prompt != DefaultTaskReviewPrompt {
		t.Fatalf("空 ReviewPrompt 应回退默认模板")
	}

	// 任务级覆盖：关闭全局开启的 review
	rc = svc.effectiveReviewConfig(&models.TaskManagerTask{
		ID: "t1", Review: &models.TaskReviewConfig{Enabled: false},
	}, 1)
	if rc.Enabled {
		t.Fatalf("任务级关闭应覆盖全局开启")
	}

	// 任务级覆盖 reviewer agent/模型/轮数
	rc = svc.effectiveReviewConfig(&models.TaskManagerTask{
		ID: "t1", Review: &models.TaskReviewConfig{Enabled: true, AgentType: "ta", ModelValue: "tm", MaxRounds: 5},
	}, 1)
	if rc.AgentType != "ta" || rc.ModelValue != "tm" || rc.MaxRounds != 5 {
		t.Fatalf("任务级覆盖错误: %+v", rc)
	}

	// 全局与任务均未指定 reviewer agent：回退任务自身 agent
	svc2 := NewTaskManagerService(mock)
	rc = svc2.effectiveReviewConfig(&models.TaskManagerTask{
		ID: "t1", AgentType: "task-agent", Review: &models.TaskReviewConfig{Enabled: true},
	}, 1)
	if rc.AgentType != "task-agent" {
		t.Fatalf("reviewer 应回退任务自身 agent，实际 %q", rc.AgentType)
	}
	if rc.MaxRounds != DefaultTaskReviewMaxRounds {
		t.Fatalf("未配置轮数应用默认值 %d，实际 %d", DefaultTaskReviewMaxRounds, rc.MaxRounds)
	}

	// 无任何配置且任务 agent 为空：回退首个已注册 agent
	rc = svc2.effectiveReviewConfig(&models.TaskManagerTask{ID: "t1", Review: &models.TaskReviewConfig{Enabled: true}}, 1)
	if rc.AgentType != "mock-agent" {
		t.Fatalf("reviewer 应回退默认 agent，实际 %q", rc.AgentType)
	}
}

// TestParseReviewVerdict 验证 JSON 判定解析：裸 JSON、围栏、夹杂文本、非法输入。
func TestParseReviewVerdict(t *testing.T) {
	v, err := parseReviewVerdict(`{"passed": true, "feedback": ""}`)
	if err != nil || !v.Passed {
		t.Fatalf("裸 JSON 解析失败: %+v, %v", v, err)
	}
	v, err = parseReviewVerdict("```json\n{\"passed\": false, \"feedback\": \"缺东西\"}\n```")
	if err != nil || v.Passed || v.Feedback != "缺东西" {
		t.Fatalf("围栏 JSON 解析失败: %+v, %v", v, err)
	}
	v, err = parseReviewVerdict("审查结论如下：{\"passed\": false, \"feedback\": \"x\"} 以上。")
	if err != nil || v.Passed {
		t.Fatalf("夹杂文本 JSON 解析失败: %+v, %v", v, err)
	}
	if _, err = parseReviewVerdict("完全不是 JSON"); err == nil {
		t.Fatalf("非法输入应报错")
	}
}
