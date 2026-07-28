package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"opennexus/internal/acp"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// DefaultNoteClassifyPrompt 是笔记自动分类与标题的默认提示词模板。
const DefaultNoteClassifyPrompt = `你是一个笔记分类与标题助手。根据笔记内容：
1) 从已有标签中选择或创建合适新标签（小写英文或中文，不含空格和 #）
2) 生成简短标题（建议 ≤40 字，概括主题，不要引号）

已有标签：{{existing_tags}}
笔记内容：
{{content}}

仅输出 JSON 对象，例如 {"tags":["工作","想法"],"title":"周会纪要"}, 不要输出其他任何文字。`

// legacyNoteClassifyPrompt 旧版仅分类默认词；库中若仍存此文案则视同未自定义。
const legacyNoteClassifyPrompt = `你是一个笔记分类助手。根据笔记内容，从已有标签中选择或创建合适的新标签（小写英文或中文，不含空格和 #）。
已有标签：{{existing_tags}}
笔记内容：
{{content}}

仅输出 JSON 数组，例如 ["工作","想法"]，不要输出其他任何文字。`

// EffectiveClassifyPrompt 返回实际使用的分类提示词（空或旧默认 → 新默认）。
func EffectiveClassifyPrompt(stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" || stored == legacyNoteClassifyPrompt {
		return DefaultNoteClassifyPrompt
	}
	return stored
}

const ClassifySessionTitle = "笔记分类"

const (
	DefaultClassifyIntervalMinutes = 5
	MaxClassifyIntervalMinutes     = 1440
)

// NormalizeClassifyIntervalMinutes 规范化分类间隔（分钟）。
func NormalizeClassifyIntervalMinutes(minutes int) int {
	if minutes <= 0 {
		return DefaultClassifyIntervalMinutes
	}
	if minutes > MaxClassifyIntervalMinutes {
		return MaxClassifyIntervalMinutes
	}
	return minutes
}

// ClassifyExecutor 负责创建展示会话并通过临时会话执行分类。
//
// 设计要点（方案 B-2）：
//   - CreateSessionWithSource 仅创建 pending 展示会话（前端进度展示容器），不会被激活、不接收 prompt
//   - RunSubAgent 在独立临时 ACP 会话中执行单次分类，调用结束即关闭，零上下文累积
type ClassifyExecutor interface {
	CreateSessionWithSource(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue string) (*models.Session, error)
	GetSessionByDBID(id uint) (*models.Session, error)
	UpdateTitle(dbSessionID uint, title string) error
	RunSubAgent(ctx context.Context, cfg acp.SubAgentRunConfig) (string, error)
	// AgentAvailable 判断 agent 类型当前是否已注册（设置页停用/删除后为 false）。
	AgentAvailable(agentType string) bool
}

// ErrClassifyAgentUnavailable 表示配置的分类 agent 已被停用或删除。
// 笔记保持待分类状态，待 agent 重新启用后自动继续。
var ErrClassifyAgentUnavailable = errors.New("配置的分类 agent 已停用或未注册")

// NoteClassifier 根据用户配置调用 agent 为笔记打标签。
type NoteClassifier struct {
	settingsRepo *repository.NoteSettingsRepository
	noteRepo     *repository.NoteRepository
	executor     ClassifyExecutor

	// unavailableWarnAt 记录各用户上次输出「agent 已停用」告警的时间，
	// worker 每分钟扫描一轮，用其节流避免同一提示刷屏。
	warnMu            sync.Mutex
	unavailableWarnAt map[uint]time.Time
}

// classifyAgentUnavailableWarnInterval 是「agent 已停用」告警的最小输出间隔。
const classifyAgentUnavailableWarnInterval = 30 * time.Minute

func NewNoteClassifier(
	settingsRepo *repository.NoteSettingsRepository,
	noteRepo *repository.NoteRepository,
	executor ClassifyExecutor,
) *NoteClassifier {
	return &NoteClassifier{
		settingsRepo:      settingsRepo,
		noteRepo:          noteRepo,
		executor:          executor,
		unavailableWarnAt: map[uint]time.Time{},
	}
}

// shouldWarnAgentUnavailable 判断是否需要为该用户输出「agent 已停用」告警（带节流）。
func (c *NoteClassifier) shouldWarnAgentUnavailable(userID uint, now time.Time) bool {
	c.warnMu.Lock()
	defer c.warnMu.Unlock()
	if last, ok := c.unavailableWarnAt[userID]; ok && now.Sub(last) < classifyAgentUnavailableWarnInterval {
		return false
	}
	c.unavailableWarnAt[userID] = now
	return true
}

// ProcessPending 处理一批待分类笔记，返回成功处理数量。
func (c *NoteClassifier) ProcessPending(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	notes, err := c.noteRepo.FindPendingClassify(limit * 5)
	if err != nil {
		return 0, err
	}
	if len(notes) == 0 {
		return 0, nil
	}
	settingsCache := map[uint]int{}
	touchedUsers := map[uint]struct{}{}
	// agent 已停用的用户本轮整体跳过，只提示一次，避免逐条报错刷日志。
	unavailableUsers := map[uint]struct{}{}
	now := time.Now()
	done := 0
	for i := range notes {
		touchedUsers[notes[i].UserID] = struct{}{}
		if done >= limit {
			break
		}
		if _, skip := unavailableUsers[notes[i].UserID]; skip {
			continue
		}
		interval, err := c.userClassifyInterval(settingsCache, notes[i].UserID)
		if err != nil {
			log.Printf("笔记 %d 读取分类设置失败: %v", notes[i].ID, err)
			continue
		}
		if now.Sub(notes[i].UpdatedAt) < time.Duration(interval)*time.Minute {
			continue
		}
		if err := c.classifyNote(ctx, &notes[i]); err != nil {
			if errors.Is(err, ErrClassifyAgentUnavailable) {
				unavailableUsers[notes[i].UserID] = struct{}{}
				if c.shouldWarnAgentUnavailable(notes[i].UserID, now) {
					log.Printf("用户 %d 的分类 agent 已停用，暂停自动分类（笔记保持待分类）: %v", notes[i].UserID, err)
				}
				continue
			}
			log.Printf("笔记 %d 分类失败: %v", notes[i].ID, err)
			continue
		}
		done++
	}
	for uid := range touchedUsers {
		c.refreshClassifySessionTitle(uid)
	}
	return done, nil
}

func (c *NoteClassifier) userClassifyInterval(cache map[uint]int, userID uint) (int, error) {
	if interval, ok := cache[userID]; ok {
		return interval, nil
	}
	settings, err := c.settingsRepo.FindByUserID(userID)
	if err != nil {
		return DefaultClassifyIntervalMinutes, err
	}
	interval := NormalizeClassifyIntervalMinutes(settings.ClassifyIntervalMinutes)
	cache[userID] = interval
	return interval, nil
}

func (c *NoteClassifier) classifyNote(ctx context.Context, note *models.Note) error {
	manualTags := tagsFromJSON(note.Tags)
	tags, title, err := c.classifyTags(ctx, note.UserID, note.ID, note.Content, manualTags)
	if err != nil {
		return err
	}
	note.Tags = tagsToJSON(tags)
	if title != "" {
		note.Title = truncateNoteTitle(title)
	}
	note.ClassifyPending = false
	if err := c.noteRepo.Update(note); err != nil {
		return err
	}
	c.refreshClassifySessionTitle(note.UserID)
	return nil
}

// ClassifyNow 忽略间隔，立即对指定笔记执行一次分类（需属主匹配）。
func (c *NoteClassifier) ClassifyNow(ctx context.Context, userID, noteID uint) (*models.Note, error) {
	if c == nil || c.noteRepo == nil {
		return nil, fmt.Errorf("分类服务未就绪")
	}
	note, err := c.noteRepo.FindByID(noteID)
	if err != nil {
		return nil, err
	}
	if note.UserID != userID {
		return nil, repository.ErrNoteNotFound
	}
	settings, err := c.settingsRepo.FindByUserID(userID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(settings.AgentType) == "" {
		return nil, fmt.Errorf("未配置笔记分类 Agent")
	}
	note.ClassifyPending = true
	if err := c.noteRepo.Update(note); err != nil {
		return nil, err
	}
	if err := c.classifyNote(ctx, note); err != nil {
		return nil, err
	}
	return c.noteRepo.FindByID(noteID)
}

func (c *NoteClassifier) classifyTags(ctx context.Context, userID, noteID uint, content string, manualTags []string) ([]string, string, error) {
	settings, err := c.settingsRepo.FindByUserID(userID)
	if err != nil {
		return manualTags, "", err
	}
	agentType := strings.TrimSpace(settings.AgentType)
	if agentType == "" || c.executor == nil {
		return manualTags, "", nil
	}
	// agent 被停用/删除时跳过本次分类（笔记保持 pending，重新启用后自动继续），
	// 避免 worker 每轮都触发 "agent 类型未注册" 报错。
	if !c.executor.AgentAvailable(agentType) {
		return manualTags, "", fmt.Errorf("%w: %s", ErrClassifyAgentUnavailable, agentType)
	}
	promptTpl := EffectiveClassifyPrompt(settings.ClassifyPrompt)
	existing, err := c.noteRepo.ListTags(userID)
	if err != nil {
		return manualTags, "", err
	}
	prompt := buildClassifyPrompt(promptTpl, content, existing)

	// 确保展示会话存在（仅创建一次 pending 会话，作为前端进度展示容器，不接收 prompt）。
	c.ensureDisplaySession(ctx, userID, agentType, settings)

	// 真正的分类调用走独立临时 ACP 会话：每条笔记上下文隔离，避免标签漂移。
	text, runErr := c.executor.RunSubAgent(ctx, acp.SubAgentRunConfig{
		AgentType:  agentType,
		ModelValue: settings.ModelValue,
		Prompt:     prompt,
		UserID:     userID, // 继承全局 mcpServers，与原 sessionMCPServers(userID) 行为一致
		// SystemPrompt 留空：分类 prompt 模板本身已含角色定义
	})
	if runErr != nil {
		return manualTags, "", runErr
	}
	if text == "" {
		return manualTags, "", fmt.Errorf("agent 未返回分类结果")
	}
	autoTags, title, ok := parseClassifyResult(text)
	if !ok {
		return manualTags, "", fmt.Errorf("无法解析分类结果")
	}
	return mergeTags(manualTags, autoTags), title, nil
}

// ensureDisplaySession 幂等确保展示会话存在。
//
// 与旧 ensureClassifySession 的区别（方案 B-2）：
//   - 展示会话保持 pending 状态，永不激活（不创建 ACP 会话、不接收 prompt）
//   - 不再 ResumeSession：展示会话无需保持 active，前端 ListSessions 不过滤 status
//   - AgentType 变更时重建展示会话
func (c *NoteClassifier) ensureDisplaySession(ctx context.Context, userID uint, agentType string, settings *models.NoteSettings) {
	if settings.ClassifyDBSessionID > 0 {
		session, err := c.executor.GetSessionByDBID(settings.ClassifyDBSessionID)
		if err == nil && session.AgentType == agentType {
			// 展示会话已存在且类型匹配，无需操作
			return
		}
	}
	session, err := c.executor.CreateSessionWithSource(ctx, agentType, 0, userID, models.SessionSourceClassify, settings.ModelValue)
	if err != nil {
		log.Printf("创建笔记分类展示会话失败: %v", err)
		return
	}
	if err := c.settingsRepo.UpdateSessionRef(userID, session.SessionID, session.ID); err != nil {
		log.Printf("保存分类会话引用失败: %v", err)
	}
}

// refreshClassifySessionTitle 将分类会话标题更新为「笔记分类 (已完成/总数)」。
func (c *NoteClassifier) refreshClassifySessionTitle(userID uint) {
	if c.executor == nil || c.noteRepo == nil || c.settingsRepo == nil {
		return
	}
	settings, err := c.settingsRepo.FindByUserID(userID)
	if err != nil || settings.ClassifyDBSessionID == 0 {
		return
	}
	done, total, err := c.noteRepo.CountClassifyProgress(userID)
	if err != nil {
		return
	}
	_ = c.executor.UpdateTitle(settings.ClassifyDBSessionID, FormatClassifySessionTitle(done, total))
}

// FormatClassifySessionTitle 生成分类会话标题。
func FormatClassifySessionTitle(done, total int64) string {
	return fmt.Sprintf("%s (%d/%d)", ClassifySessionTitle, done, total)
}

func tagsFromJSON(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return []string{}
	}
	return tags
}

func tagsToJSON(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func buildClassifyPrompt(template, content string, existingTags []string) string {
	tagsStr := "无"
	if len(existingTags) > 0 {
		tagsStr = strings.Join(existingTags, ", ")
	}
	p := strings.ReplaceAll(template, "{{content}}", content)
	return strings.ReplaceAll(p, "{{existing_tags}}", tagsStr)
}

func stripCodeFence(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return text
	}
	lines = lines[1:]
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// extractBalanced 从 text 中提取以 open 开头的第一段平衡括号内容。
func extractBalanced(text string, open, close byte) string {
	start := strings.IndexByte(text, open)
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

func parseClassifyResult(text string) (tags []string, title string, ok bool) {
	text = stripCodeFence(text)
	var obj struct {
		Tags  []string `json:"tags"`
		Title string   `json:"title"`
	}
	tryObj := func(s string) bool {
		obj = struct {
			Tags  []string `json:"tags"`
			Title string   `json:"title"`
		}{}
		if err := json.Unmarshal([]byte(s), &obj); err != nil {
			return false
		}
		if obj.Tags == nil && obj.Title == "" {
			return false
		}
		tags = normalizeTags(obj.Tags)
		title = strings.TrimSpace(obj.Title)
		return true
	}
	if tryObj(text) {
		return tags, title, true
	}
	if s := extractBalanced(text, '{', '}'); s != "" && tryObj(s) {
		return tags, title, true
	}
	var arr []string
	if err := json.Unmarshal([]byte(text), &arr); err == nil {
		return normalizeTags(arr), "", true
	}
	if s := extractBalanced(text, '[', ']'); s != "" {
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return normalizeTags(arr), "", true
		}
	}
	return nil, "", false
}

func parseClassifyTags(text string) []string {
	tags, _, ok := parseClassifyResult(text)
	if !ok {
		return nil
	}
	return tags
}

func truncateNoteTitle(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= 80 {
		return s
	}
	runes := []rune(s)
	return string(runes[:80]) + "…"
}

func normalizeTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(strings.ToLower(t))
		t = strings.TrimPrefix(t, "#")
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func mergeTags(manual, auto []string) []string {
	if len(auto) == 0 {
		return manual
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(manual)+len(auto))
	for _, t := range manual {
		t = strings.TrimSpace(strings.ToLower(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range auto {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
