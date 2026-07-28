package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"opennexus/internal/models"
	"opennexus/internal/repository"
	"opennexus/internal/services"
)

// TaskSettingsHandler 处理任务自动分类 / 标题生成设置。
type TaskSettingsHandler struct {
	settingsRepo *repository.TaskSettingsRepository
}

func NewTaskSettingsHandler(settingsRepo *repository.TaskSettingsRepository) *TaskSettingsHandler {
	return &TaskSettingsHandler{settingsRepo: settingsRepo}
}

type taskSettingsItem struct {
	AutoTagEnabled   bool     `json:"auto_tag_enabled"`
	AutoTitleEnabled bool     `json:"auto_title_enabled"`
	AgentType        string   `json:"agent_type"`
	ModelValue       string   `json:"model_value"`
	Tags             []string `json:"tags"`
	TagPrompt        string   `json:"tag_prompt"`
	TitlePrompt      string   `json:"title_prompt"`
	DocEditPrompt    string   `json:"doc_edit_prompt"`
	ReviewEnabled    bool     `json:"review_enabled"`
	ReviewAgentType  string   `json:"review_agent_type"`
	ReviewModelValue string   `json:"review_model_value"`
	ReviewMaxRounds  int      `json:"review_max_rounds"`
	ReviewPrompt     string   `json:"review_prompt"`
}

type taskSettingsRequest struct {
	AutoTagEnabled   bool   `json:"auto_tag_enabled"`
	AutoTitleEnabled bool   `json:"auto_title_enabled"`
	AgentType        string `json:"agent_type"`
	ModelValue       string `json:"model_value"`
	Tags             []string `json:"tags"`
	TagPrompt        string `json:"tag_prompt"`
	TitlePrompt      string `json:"title_prompt"`
	DocEditPrompt    string `json:"doc_edit_prompt"`
	ReviewEnabled    bool   `json:"review_enabled"`
	ReviewAgentType  string `json:"review_agent_type"`
	ReviewModelValue string `json:"review_model_value"`
	ReviewMaxRounds  int    `json:"review_max_rounds"`
	ReviewPrompt     string `json:"review_prompt"`
}

// toItem 把存储模型转换为返回给前端的结构（标签 JSON 解析为数组，空提示词填默认）。
func (h *TaskSettingsHandler) toItem(s *models.TaskSettings) taskSettingsItem {
	tagPrompt := strings.TrimSpace(s.TagPrompt)
	if tagPrompt == "" {
		tagPrompt = services.DefaultTaskTagPrompt
	}
	titlePrompt := strings.TrimSpace(s.TitlePrompt)
	if titlePrompt == "" {
		titlePrompt = services.DefaultTaskTitlePrompt
	}
	docEditPrompt := strings.TrimSpace(s.DocEditPrompt)
	if docEditPrompt == "" {
		docEditPrompt = services.DefaultDocEditPrompt
	}
	reviewPrompt := strings.TrimSpace(s.ReviewPrompt)
	if reviewPrompt == "" {
		reviewPrompt = services.DefaultTaskReviewPrompt
	}
	reviewMaxRounds := s.ReviewMaxRounds
	if reviewMaxRounds <= 0 {
		reviewMaxRounds = services.DefaultTaskReviewMaxRounds
	}
	tags := []string{}
	if raw := strings.TrimSpace(s.Tags); raw != "" {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			tags = arr
		}
	}
	if len(tags) == 0 {
		tags = services.DefaultPredefinedTags
	}
	return taskSettingsItem{
		AutoTagEnabled:   s.AutoTagEnabled,
		AutoTitleEnabled: s.AutoTitleEnabled,
		AgentType:        s.AgentType,
		ModelValue:       s.ModelValue,
		Tags:             tags,
		TagPrompt:        tagPrompt,
		TitlePrompt:      titlePrompt,
		DocEditPrompt:    docEditPrompt,
		ReviewEnabled:    s.ReviewEnabled,
		ReviewAgentType:  s.ReviewAgentType,
		ReviewModelValue: s.ReviewModelValue,
		ReviewMaxRounds:  reviewMaxRounds,
		ReviewPrompt:     reviewPrompt,
	}
}

// GetSettings GET /api/v1/tasks/settings
func (h *TaskSettingsHandler) GetSettings(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	s, err := h.settingsRepo.FindByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询任务设置失败")
		return
	}
	Success(c, http.StatusOK, h.toItem(s))
}

// UpdateSettings PUT /api/v1/tasks/settings
func (h *TaskSettingsHandler) UpdateSettings(c *gin.Context) {
	var req taskSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	tagBytes, _ := json.Marshal(uniqTags(req.Tags))
	reviewMaxRounds := req.ReviewMaxRounds
	if reviewMaxRounds < 0 {
		reviewMaxRounds = 0
	}
	s := &models.TaskSettings{
		UserID:          uid,
		AutoTagEnabled:  req.AutoTagEnabled,
		AutoTitleEnabled: req.AutoTitleEnabled,
		AgentType:       strings.TrimSpace(req.AgentType),
		ModelValue:      strings.TrimSpace(req.ModelValue),
		Tags:            string(tagBytes),
		TagPrompt:       strings.TrimSpace(req.TagPrompt),
		TitlePrompt:     strings.TrimSpace(req.TitlePrompt),
		DocEditPrompt:   strings.TrimSpace(req.DocEditPrompt),
		ReviewEnabled:    req.ReviewEnabled,
		ReviewAgentType:  strings.TrimSpace(req.ReviewAgentType),
		ReviewModelValue: strings.TrimSpace(req.ReviewModelValue),
		ReviewMaxRounds:  reviewMaxRounds,
		ReviewPrompt:     strings.TrimSpace(req.ReviewPrompt),
	}
	if err := h.settingsRepo.Upsert(s); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "保存任务设置失败")
		return
	}
	saved, err := h.settingsRepo.FindByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询任务设置失败")
		return
	}
	Success(c, http.StatusOK, h.toItem(saved))
}

func uniqTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
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
