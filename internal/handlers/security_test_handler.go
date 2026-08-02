package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	acplocal "opennexus/internal/acp"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

// AgentSecurityTester 对指定 agent 类型执行沙箱测试（发送危险 prompt，检测是否尝试执行）。
type AgentSecurityTester interface {
	TestAgentSecurity(ctx context.Context, agentType string, cases []acplocal.SecurityTestCaseInput, modelValue string) (acplocal.SecurityTestReport, error)
	TestAllAgentSecurity(ctx context.Context, cases []acplocal.SecurityTestCaseInput) (acplocal.SecurityTestBatchResult, error)
	LastSecurityTest(agentType string) (acplocal.SecurityTestReport, bool)
}

// SecurityTestHandler 处理沙箱测试用例管理与测试执行请求。
type SecurityTestHandler struct {
	caseRepo *repository.SecurityTestCaseRepository
	tester   AgentSecurityTester
}

// NewSecurityTestHandler 创建 SecurityTestHandler。tester 可为 nil（沙箱测试不可用时返回 503）。
func NewSecurityTestHandler(caseRepo *repository.SecurityTestCaseRepository, tester any) *SecurityTestHandler {
	h := &SecurityTestHandler{caseRepo: caseRepo}
	if t, ok := tester.(AgentSecurityTester); ok {
		h.tester = t
	}
	return h
}

// securityTestCaseItem 是返回给前端的用例结构。
type securityTestCaseItem struct {
	ID            uint   `json:"id"`
	Name          string `json:"name"`
	Category      string `json:"category"`
	Prompt        string `json:"prompt"`
	Enabled       bool   `json:"enabled"`
	SortOrder     int    `json:"sort_order"`
	ExpectBlocked bool   `json:"expect_blocked"`
}

// GetCases GET /api/v1/security-tests/cases — 返回用户的沙箱测试用例列表（首次访问种子默认用例）。
func (h *SecurityTestHandler) GetCases(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	cases, err := h.caseRepo.FindByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询沙箱测试用例失败")
		return
	}
	items := make([]securityTestCaseItem, 0, len(cases))
	for _, tc := range cases {
		items = append(items, securityTestCaseItem{
			ID: tc.ID, Name: tc.Name, Category: tc.Category, Prompt: tc.Prompt,
			Enabled: tc.Enabled, SortOrder: tc.SortOrder, ExpectBlocked: tc.ExpectBlocked,
		})
	}
	Success(c, http.StatusOK, gin.H{"cases": items})
}

// UpdateCases PUT /api/v1/security-tests/cases — 替换用户全部沙箱测试用例。
func (h *SecurityTestHandler) UpdateCases(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	var req struct {
		Cases []securityTestCaseItem `json:"cases"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if len(req.Cases) > 100 {
		Fail(c, http.StatusBadRequest, "TOO_MANY_CASES", "测试用例数量超过上限（100）")
		return
	}
	cases := make([]models.SecurityTestCase, 0, len(req.Cases))
	for i, item := range req.Cases {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = "未命名用例"
		}
		prompt := strings.TrimSpace(item.Prompt)
		if prompt == "" {
			continue // 跳过空 prompt 用例
		}
		category := strings.TrimSpace(item.Category)
		if category == "" {
			category = models.SecTestFSWriteOutside
		}
		cases = append(cases, models.SecurityTestCase{
			Name: name, Category: category, Prompt: prompt,
			Enabled: item.Enabled, SortOrder: i, ExpectBlocked: item.ExpectBlocked,
		})
	}
	if err := h.caseRepo.ReplaceAll(uid, cases); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "保存沙箱测试用例失败")
		return
	}
	saved, err := h.caseRepo.FindByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询沙箱测试用例失败")
		return
	}
	items := make([]securityTestCaseItem, 0, len(saved))
	for _, tc := range saved {
		items = append(items, securityTestCaseItem{
			ID: tc.ID, Name: tc.Name, Category: tc.Category, Prompt: tc.Prompt,
			Enabled: tc.Enabled, SortOrder: tc.SortOrder, ExpectBlocked: tc.ExpectBlocked,
		})
	}
	Success(c, http.StatusOK, gin.H{"cases": items})
}

// enabledCases 把用户用例转为沙箱测试输入，仅保留启用且非空的。
func (h *SecurityTestHandler) enabledCases(uid uint) ([]acplocal.SecurityTestCaseInput, error) {
	all, err := h.caseRepo.FindByUserID(uid)
	if err != nil {
		return nil, err
	}
	out := make([]acplocal.SecurityTestCaseInput, 0, len(all))
	for _, tc := range all {
		if !tc.Enabled || strings.TrimSpace(tc.Prompt) == "" {
			continue
		}
		out = append(out, acplocal.SecurityTestCaseInput{
			ID: tc.ID, Name: tc.Name, Category: tc.Category, Prompt: tc.Prompt,
			ExpectBlocked: tc.ExpectBlocked,
		})
	}
	return out, nil
}

// RunTest POST /api/v1/agents/:type/security-test — 对指定 agent 执行沙箱测试。
// body: {"model_value": "..."}（可选，空=自动选取 agent 运行模型）。
func (h *SecurityTestHandler) RunTest(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.tester == nil {
		Fail(c, http.StatusServiceUnavailable, "SECTEST_UNAVAILABLE", "当前服务不支持沙箱测试")
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	var req struct {
		ModelValue string `json:"model_value"`
	}
	_ = c.ShouldBindJSON(&req)
	cases, err := h.enabledCases(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询沙箱测试用例失败")
		return
	}
	report, err := h.tester.TestAgentSecurity(c.Request.Context(), agentType, cases, req.ModelValue)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "SECTEST_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, report)
}

// LastTest GET /api/v1/agents/:type/security-test — 返回最近一次沙箱测试报告（内存缓存）。
func (h *SecurityTestHandler) LastTest(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.tester == nil {
		Fail(c, http.StatusServiceUnavailable, "SECTEST_UNAVAILABLE", "当前服务不支持沙箱测试")
		return
	}
	report, ok := h.tester.LastSecurityTest(agentType)
	if !ok {
		Success(c, http.StatusOK, gin.H{"agent_type": agentType, "available": false})
		return
	}
	Success(c, http.StatusOK, gin.H{"agent_type": agentType, "available": true, "report": report})
}

// RunAllTests POST /api/v1/agents/security-test-all — 对所有已接入 agent 并行执行沙箱测试。
func (h *SecurityTestHandler) RunAllTests(c *gin.Context) {
	if h.tester == nil {
		Fail(c, http.StatusServiceUnavailable, "SECTEST_UNAVAILABLE", "当前服务不支持沙箱测试")
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	cases, err := h.enabledCases(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "查询沙箱测试用例失败")
		return
	}
	result, err := h.tester.TestAllAgentSecurity(c.Request.Context(), cases)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "SECTEST_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, result)
}
