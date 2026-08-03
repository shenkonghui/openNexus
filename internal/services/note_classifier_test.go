package services

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"opennexus/internal/acp"
	"opennexus/internal/database"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// fakeClassifyExecutor 记录 RunSubAgent 调用，返回预设文本，用于验证分类走临时会话路径。
type fakeClassifyExecutor struct {
	mu               sync.Mutex
	runSubAgentCalls int
	lastPrompt       string
	lastAgentType    string
	lastModelValue   string
	lastUserID       uint
	subAgentText     string
	subAgentErr      error
	// 展示会话相关
	createdSessions []models.Session
	updateTitleArgs []struct {
		dbSessionID uint
		title       string
	}
	// existingSession 模拟 GetSessionByDBID 命中时返回的会话（nil=返回 not found）
	existingSession *models.Session
	// agentUnavailable 模拟 agent 被停用/注销（AgentAvailable 返回 false）
	agentUnavailable bool
}

func (f *fakeClassifyExecutor) AgentAvailable(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.agentUnavailable
}

func (f *fakeClassifyExecutor) CreateSessionWithSource(_ context.Context, agentType string, _ uint, userID uint, _, modelValue string) (*models.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &models.Session{
		SessionID:  "display-session-id",
		AgentType:  agentType,
		Status:     models.SessionStatusPending,
		UserID:     userID,
		Source:     models.SessionSourceClassify,
		ModelValue: modelValue,
	}
	f.createdSessions = append(f.createdSessions, *s)
	return s, nil
}

func (f *fakeClassifyExecutor) GetSessionByDBID(id uint) (*models.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existingSession != nil {
		return f.existingSession, nil
	}
	return nil, repository.ErrSessionNotFound
}

func (f *fakeClassifyExecutor) UpdateTitle(dbSessionID uint, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateTitleArgs = append(f.updateTitleArgs, struct {
		dbSessionID uint
		title       string
	}{dbSessionID, title})
	return nil
}

func (f *fakeClassifyExecutor) RunSubAgent(_ context.Context, cfg acp.SubAgentRunConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runSubAgentCalls++
	f.lastPrompt = cfg.Prompt
	f.lastAgentType = cfg.AgentType
	f.lastModelValue = cfg.ModelValue
	f.lastUserID = cfg.UserID
	if f.subAgentErr != nil {
		return "", f.subAgentErr
	}
	return f.subAgentText, nil
}

// setupClassifierTest 用内存 SQLite 初始化笔记/设置仓库，返回 classifier + fake executor。
func setupClassifierTest(t *testing.T) (*NoteClassifier, *fakeClassifyExecutor, *repository.NoteSettingsRepository, *repository.NoteRepository) {
	t.Helper()
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	db.Exec("DELETE FROM notes")
	db.Exec("DELETE FROM note_settings")
	settingsRepo := repository.NewNoteSettingsRepository(db)
	noteRepo := repository.NewNoteRepository(db)
	executor := &fakeClassifyExecutor{subAgentText: `{"tags":["工作"],"title":"周会纪要"}`}
	c := NewNoteClassifier(settingsRepo, noteRepo, executor)
	return c, executor, settingsRepo, noteRepo
}

// TestClassifyTagsUsesSubAgent 验证：分类调用走 RunSubAgent 临时会话而非 PromptWithExecution，
// 且 prompt 正确拼接、结果正确解析、展示会话被创建。
func TestClassifyTagsUsesSubAgent(t *testing.T) {
	c, executor, settingsRepo, _ := setupClassifierTest(t)

	// 配置分类 agent
	if err := settingsRepo.Upsert(&models.NoteSettings{
		UserID:    1,
		AgentType: "test-agent",
	}); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}

	note := &models.Note{
		UserID:          1,
		Title:           "原标题",
		Content:         "今天开了周会，讨论了项目进度",
		Tags:            "[]",
		ClassifyPending: true,
		UpdatedAt:       time.Now().Add(-1 * time.Hour),
	}
	ctx := context.Background()
	tags, title, err := c.classifyTags(ctx, note.UserID, note.ID, note.Content, []string{})
	if err != nil {
		t.Fatalf("classifyTags 失败: %v", err)
	}

	// 必须调用 RunSubAgent 一次（临时会话路径）
	if executor.runSubAgentCalls != 1 {
		t.Fatalf("RunSubAgent 调用次数 = %d, 期望 1", executor.runSubAgentCalls)
	}
	// 参数透传正确
	if executor.lastAgentType != "test-agent" {
		t.Errorf("AgentType = %q, 期望 test-agent", executor.lastAgentType)
	}
	if executor.lastUserID != 1 {
		t.Errorf("UserID = %d, 期望 1", executor.lastUserID)
	}
	// prompt 应包含笔记内容
	if !strings.Contains(executor.lastPrompt, "周会") {
		t.Errorf("prompt 未包含笔记内容: %q", executor.lastPrompt)
	}
	// 结果解析正确
	if title != "周会纪要" {
		t.Errorf("title = %q, 期望 周会纪要", title)
	}
	if !reflect.DeepEqual(tags, []string{"工作"}) {
		t.Errorf("tags = %v, 期望 [工作]", tags)
	}
	// 展示会话应被创建一次
	if len(executor.createdSessions) != 1 {
		t.Fatalf("展示会话创建次数 = %d, 期望 1", len(executor.createdSessions))
	}
	if executor.createdSessions[0].Source != models.SessionSourceClassify {
		t.Errorf("展示会话 source = %q, 期望 classify", executor.createdSessions[0].Source)
	}
}

// TestClassifyTagsDisplaySessionReuse 验证：展示会话已存在时不重复创建。
func TestClassifyTagsDisplaySessionReuse(t *testing.T) {
	c, executor, settingsRepo, _ := setupClassifierTest(t)

	// 预先创建展示会话引用，模拟二次分类
	if err := settingsRepo.Upsert(&models.NoteSettings{
		UserID:              2,
		AgentType:           "test-agent",
		ClassifyDBSessionID: 100, // 已有引用
	}); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	// 让 GetSessionByDBID 返回已存在的会话（AgentType 匹配 → 跳过重建）
	executor.existingSession = &models.Session{AgentType: "test-agent"}

	ctx := context.Background()
	_, _, err := c.classifyTags(ctx, 2, 1, "内容", []string{})
	if err != nil {
		t.Fatalf("classifyTags 失败: %v", err)
	}
	// 已有会话则不应再 CreateSessionWithSource（createdSessions 不新增）
	if len(executor.createdSessions) != 0 {
		t.Errorf("展示会话不应重建，当前数 = %d", len(executor.createdSessions))
	}
}

// TestClassifyTagsSubAgentError 验证：RunSubAgent 返回错误时正确传递，并保留手动标签。
func TestClassifyTagsSubAgentError(t *testing.T) {
	c, executor, settingsRepo, _ := setupClassifierTest(t)
	executor.subAgentText = ""
	executor.subAgentErr = errors.New("agent 响应超时")

	if err := settingsRepo.Upsert(&models.NoteSettings{
		UserID:    3,
		AgentType: "test-agent",
	}); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}

	ctx := context.Background()
	tags, _, err := c.classifyTags(ctx, 3, 1, "内容", []string{"已有标签"})
	if err == nil {
		t.Fatal("期望返回错误，实际 nil")
	}
	// 出错时应保留手动标签
	if !reflect.DeepEqual(tags, []string{"已有标签"}) {
		t.Errorf("出错时 tags = %v, 期望保留手动标签 [已有标签]", tags)
	}
}

// TestProcessPendingAgentUnavailable 验证：分类 agent 被停用后，
// ProcessPending 跳过该用户笔记（保持 pending）、不调用 RunSubAgent、不创建展示会话。
func TestProcessPendingAgentUnavailable(t *testing.T) {
	c, executor, settingsRepo, noteRepo := setupClassifierTest(t)
	executor.agentUnavailable = true

	if err := settingsRepo.Upsert(&models.NoteSettings{
		UserID:    5,
		AgentType: "disabled-agent",
	}); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	note := &models.Note{
		UserID:          5,
		Content:         "待分类内容",
		Tags:            "[]",
		ClassifyPending: true,
	}
	if err := noteRepo.Create(note); err != nil {
		t.Fatalf("创建笔记失败: %v", err)
	}
	// 回退 UpdatedAt 使其超过分类间隔（共享内存库，重连即同库）
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.Model(note).Update("updated_at", time.Now().Add(-1*time.Hour)).Error; err != nil {
		t.Fatalf("回退更新时间失败: %v", err)
	}

	done, err := c.ProcessPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("ProcessPending 失败: %v", err)
	}
	if done != 0 {
		t.Errorf("done = %d, 期望 0（agent 停用应跳过）", done)
	}
	if executor.runSubAgentCalls != 0 {
		t.Errorf("RunSubAgent 调用次数 = %d, 期望 0", executor.runSubAgentCalls)
	}
	if len(executor.createdSessions) != 0 {
		t.Errorf("不应创建展示会话，当前数 = %d", len(executor.createdSessions))
	}
	got, err := noteRepo.FindByID(note.ID)
	if err != nil {
		t.Fatalf("查询笔记失败: %v", err)
	}
	if !got.ClassifyPending {
		t.Error("笔记应保持待分类状态（重新启用 agent 后自动继续）")
	}
}

func TestParseClassifyTags(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{`["work","idea"]`, []string{"work", "idea"}},
		{"```json\n[\"todo\"]\n```", []string{"todo"}},
		{"标签：[\"学习\", \"工作\"]", []string{"学习", "工作"}},
		{"invalid", nil},
	}
	for _, tc := range tests {
		got := parseClassifyTags(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseClassifyTags(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseClassifyResult(t *testing.T) {
	tags, title, ok := parseClassifyResult(`{"tags":["工作"],"title":"周会"}`)
	if !ok || title != "周会" || !reflect.DeepEqual(tags, []string{"工作"}) {
		t.Fatalf("object: tags=%v title=%q ok=%v", tags, title, ok)
	}
	tags, title, ok = parseClassifyResult("```json\n{\"tags\":[\"todo\"],\"title\":\"清单\"}\n```")
	if !ok || title != "清单" || !reflect.DeepEqual(tags, []string{"todo"}) {
		t.Fatalf("fenced: tags=%v title=%q ok=%v", tags, title, ok)
	}
	tags, title, ok = parseClassifyResult(`["a","b"]`)
	if !ok || title != "" || !reflect.DeepEqual(tags, []string{"a", "b"}) {
		t.Fatalf("array: tags=%v title=%q ok=%v", tags, title, ok)
	}
	_, _, ok = parseClassifyResult("invalid")
	if ok {
		t.Fatal("invalid should not ok")
	}
	// Agent 偶发重复推送整段 JSON，拼接后仍应解析出第一段
	dup := `{"tags":["k8s"],"title":"kubectl 查看 Pod 扩展信息"}{"tags":["k8s"],"title":"kubectl 查看 Pod 扩展信息"}`
	tags, title, ok = parseClassifyResult(dup)
	if !ok || title != "kubectl 查看 Pod 扩展信息" || !reflect.DeepEqual(tags, []string{"k8s"}) {
		t.Fatalf("dup chunks: tags=%v title=%q ok=%v", tags, title, ok)
	}
}

func TestEffectiveClassifyPrompt(t *testing.T) {
	if got := EffectiveClassifyPrompt(""); got != DefaultNoteClassifyPrompt {
		t.Fatalf("empty: got unexpected prompt")
	}
	if got := EffectiveClassifyPrompt(legacyNoteClassifyPrompt); got != DefaultNoteClassifyPrompt {
		t.Fatalf("legacy: should upgrade to new default")
	}
	custom := "自定义 {{content}}"
	if got := EffectiveClassifyPrompt(custom); got != custom {
		t.Fatalf("custom: got %q", got)
	}
}

func TestFormatClassifySessionTitle(t *testing.T) {
	if got := FormatClassifySessionTitle(3, 12); got != "笔记分类 (3/12)" {
		t.Fatalf("got %q", got)
	}
	if got := FormatClassifySessionTitle(0, 0); got != "笔记分类 (0/0)" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeClassifyIntervalMinutes(t *testing.T) {
	if got := NormalizeClassifyIntervalMinutes(0); got != DefaultClassifyIntervalMinutes {
		t.Fatalf("zero = %d, want %d", got, DefaultClassifyIntervalMinutes)
	}
	if got := NormalizeClassifyIntervalMinutes(10); got != 10 {
		t.Fatalf("10 = %d", got)
	}
	if got := NormalizeClassifyIntervalMinutes(9999); got != MaxClassifyIntervalMinutes {
		t.Fatalf("9999 = %d, want %d", got, MaxClassifyIntervalMinutes)
	}
}

func TestMergeTags(t *testing.T) {
	got := mergeTags([]string{"a", "b"}, []string{"b", "c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeTags = %v, want %v", got, want)
	}
}
