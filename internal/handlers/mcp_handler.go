package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/acp"
	gatewaymcp "opennexus/internal/mcp/gateway"
)

// MCPHandler 提供全局共享 MCP 配置文件（标准 mcpServers JSON 格式）的读写能力，
// 以及 MCP 聚合网关的状态查询与开关。
type MCPHandler struct {
	configPath string
	gateway    *gatewaymcp.Gateway
}

// NewMCPHandler 创建 MCPHandler。configPath 为 mcp.json 绝对路径，
// gateway 为 MCP 聚合网关（可为 nil，此时网关相关接口返回未启用）。
func NewMCPHandler(configPath string, gateway *gatewaymcp.Gateway) *MCPHandler {
	return &MCPHandler{configPath: configPath, gateway: gateway}
}

type mcpConfigResponse struct {
	Config string `json:"config"` // 文件原始文本（便于前端直接编辑）
	Path   string `json:"path"`   // 配置文件绝对路径
	Count  int    `json:"count"`  // 解析到的 server 数量
}

type mcpConfigRequest struct {
	Config string `json:"config"`
}

// GetMCPConfig GET /api/v1/config/mcp
// 返回 mcp.json 原始内容；文件不存在时返回空串（前端可据此显示空配置）。
func (h *MCPHandler) GetMCPConfig(c *gin.Context) {
	data, err := os.ReadFile(h.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			Success(c, http.StatusOK, mcpConfigResponse{Config: "", Path: h.configPath, Count: 0})
			return
		}
		Fail(c, http.StatusInternalServerError, "MCP_READ_ERROR", "读取 MCP 配置文件失败")
		return
	}
	Success(c, http.StatusOK, mcpConfigResponse{
		Config: string(data),
		Path:   h.configPath,
		Count:  countMCPServersFromBytes(data),
	})
}

// UpdateMCPConfig PUT /api/v1/config/mcp
// 校验 JSON 合法后写回 mcp.json（保存即生效，新建会话自动注入）。
func (h *MCPHandler) UpdateMCPConfig(c *gin.Context) {
	var req mcpConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_JSON", "请求参数格式错误")
		return
	}
	// 校验 JSON 合法性（空串等价于空配置，允许保存）
	trimmed := req.Config
	if trimmed != "" {
		var probe map[string]any
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
			Fail(c, http.StatusBadRequest, "INVALID_MCP_JSON", "MCP 配置不是合法的 JSON: "+err.Error())
			return
		}
	}
	// 确保父目录存在
	if dir := filepath.Dir(h.configPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			Fail(c, http.StatusInternalServerError, "MCP_DIR_ERROR", "创建 MCP 配置目录失败")
			return
		}
	}
	if err := os.WriteFile(h.configPath, []byte(trimmed), 0o644); err != nil {
		Fail(c, http.StatusInternalServerError, "MCP_WRITE_ERROR", "写入 MCP 配置文件失败")
		return
	}
	Success(c, http.StatusOK, mcpConfigResponse{
		Config: trimmed,
		Path:   h.configPath,
		Count:  countMCPServersFromBytes([]byte(trimmed)),
	})
}

// countMCPServersFromBytes 解析 JSON 并返回 mcpServers 数量；非法或无则返回 0。
func countMCPServersFromBytes(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	var file struct {
		McpServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return 0
	}
	return len(file.McpServers)
}

// GetMCPStatus GET /api/v1/config/mcp/status
// 探测 mcp.json 中配置的所有 MCP server 的连接状态与工具列表。
// 并发探测，每个 server 独立超时；整体兜底超时 60s。
func (h *MCPHandler) GetMCPStatus(c *gin.Context) {
	entries, err := acp.LoadMCPServerEntries(h.configPath)
	if err != nil {
		// 文件不存在或解析失败时返回空列表（而非错误），前端显示"未配置"。
		Success(c, http.StatusOK, gin.H{"servers": []any{}, "error": err.Error()})
		return
	}
	if len(entries) == 0 {
		Success(c, http.StatusOK, gin.H{"servers": []any{}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	servers := acp.ProbeMCPServers(ctx, entries)
	Success(c, http.StatusOK, gin.H{"servers": servers})
}

// GetGatewayStatus GET /api/v1/config/mcp/gateway
// 返回聚合网关的启用状态、endpoint、token 以及各上游的聚合情况。
func (h *MCPHandler) GetGatewayStatus(c *gin.Context) {
	if h.gateway == nil {
		Success(c, http.StatusOK, gatewaymcp.Status{})
		return
	}
	uid, _ := currentUserID(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	Success(c, http.StatusOK, h.gateway.Status(ctx, uid))
}

// SetGatewayEnabled POST /api/v1/config/mcp/gateway
// body: {"enabled": true|false}
// 启用会把 opennexus-gateway 条目写入 mcp.json，此后其余 http/sse 上游收敛到网关之后；
// 停用则移除该条目，恢复各上游直接注入会话。
func (h *MCPHandler) SetGatewayEnabled(c *gin.Context) {
	if h.gateway == nil {
		Fail(c, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE", "MCP 聚合网关未启用")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_JSON", "请求参数格式错误")
		return
	}

	var err error
	if req.Enabled {
		err = h.gateway.EnableEntry()
	} else {
		err = h.gateway.DisableEntry()
	}
	if err != nil {
		Fail(c, http.StatusBadRequest, "GATEWAY_TOGGLE_FAILED", err.Error())
		return
	}

	uid, _ := currentUserID(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	Success(c, http.StatusOK, h.gateway.Status(ctx, uid))
}

// DisableUpstream POST /api/v1/config/mcp/gateway/upstreams/:name/disable
// 在网关层面禁用指定上游（不修改 mcp.json），禁用后该上游的工具不再暴露。
func (h *MCPHandler) DisableUpstream(c *gin.Context) {
	if h.gateway == nil {
		Fail(c, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE", "MCP 聚合网关未启用")
		return
	}
	name := c.Param("name")
	if name == "" {
		Fail(c, http.StatusBadRequest, "INVALID_NAME", "上游名不能为空")
		return
	}
	if err := h.gateway.DisableUpstream(name); err != nil {
		Fail(c, http.StatusBadRequest, "GATEWAY_DISABLE_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"name": name, "disabled": true})
}

// EnableUpstream POST /api/v1/config/mcp/gateway/upstreams/:name/enable
// 解除对指定上游的禁用。
func (h *MCPHandler) EnableUpstream(c *gin.Context) {
	if h.gateway == nil {
		Fail(c, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE", "MCP 聚合网关未启用")
		return
	}
	name := c.Param("name")
	if name == "" {
		Fail(c, http.StatusBadRequest, "INVALID_NAME", "上游名不能为空")
		return
	}
	if err := h.gateway.EnableUpstream(name); err != nil {
		Fail(c, http.StatusBadRequest, "GATEWAY_ENABLE_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"name": name, "disabled": false})
}

// AddCustomServer POST /api/v1/config/mcp/gateway/custom-servers
// 在网关层面添加自定义上游（不写入 mcp.json），body 为标准 MCPServerEntry。
func (h *MCPHandler) AddCustomServer(c *gin.Context) {
	if h.gateway == nil {
		Fail(c, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE", "MCP 聚合网关未启用")
		return
	}
	var req struct {
		Name  string `json:"name"`
		Entry struct {
			Type    string            `json:"type"`
			Url     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"entry"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_JSON", "请求参数格式错误")
		return
	}
	if req.Name == "" {
		Fail(c, http.StatusBadRequest, "INVALID_NAME", "上游名不能为空")
		return
	}
	// 网关只接管 http/sse：stdio 的工作目录语义依赖会话 cwd，
	// 交由网关代管会改变行为，因此拒绝 stdio 类型的自定义上游。
	typ := strings.TrimSpace(req.Entry.Type)
	if typ == "" {
		typ = acp.MCPTypeHTTP
	}
	if typ != acp.MCPTypeHTTP && typ != acp.MCPTypeSSE {
		Fail(c, http.StatusBadRequest, "INVALID_TYPE", "网关自定义上游仅支持 http / sse 类型（stdio 请配置到 mcp.json 由会话直接注入）")
		return
	}
	if strings.TrimSpace(req.Entry.Url) == "" {
		Fail(c, http.StatusBadRequest, "INVALID_URL", "url 不能为空")
		return
	}
	entry := acp.MCPServerEntry{
		Type:    typ,
		Url:     strings.TrimSpace(req.Entry.Url),
		Headers: req.Entry.Headers,
	}
	if err := h.gateway.AddCustomServer(req.Name, entry); err != nil {
		Fail(c, http.StatusBadRequest, "GATEWAY_ADD_CUSTOM_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"name": req.Name, "added": true})
}

// RemoveCustomServer DELETE /api/v1/config/mcp/gateway/custom-servers/:name
// 移除自定义上游。不存在时无操作。
func (h *MCPHandler) RemoveCustomServer(c *gin.Context) {
	if h.gateway == nil {
		Fail(c, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE", "MCP 聚合网关未启用")
		return
	}
	name := c.Param("name")
	if name == "" {
		Fail(c, http.StatusBadRequest, "INVALID_NAME", "上游名不能为空")
		return
	}
	if err := h.gateway.RemoveCustomServer(name); err != nil {
		Fail(c, http.StatusBadRequest, "GATEWAY_REMOVE_CUSTOM_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"name": name, "removed": true})
}
