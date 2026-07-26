package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"opennexus/internal/acp"
	"opennexus/internal/middleware"
	"opennexus/internal/repository"
)

// legacyMCPNames 列出已下线的内置 MCP server 条目名，同步时从 mcp.json 清理残留。
// opennexus-subagent 已删除（run_subagent 与原生 subagent 重叠，会话类工具并入编排体系）。
var legacyMCPNames = []string{"opennexus-subagent"}

// OrchestrationMCPName 是 task MCP server 在 mcp.json 中的条目名。
// 注：server 名为 opennexus-task（去 orchestration 概念），HTTP 路由仍为 /mcp/orchestration。
const OrchestrationMCPName = "opennexus-task"

// builtinMCPServers 列出需要同步到 mcp.json 的内置 MCP server 及其挂载路径。
var builtinMCPServers = []struct {
	Name string
	Path string
}{
	{OrchestrationMCPName, "/mcp/orchestration"},
}

// SubAgentHandler 负责内置 MCP 条目的同步自愈。
type SubAgentHandler struct {
	settingsRepo  *repository.NoteSettingsRepository
	mcpConfigPath string
	publicBaseURL string
}

func NewSubAgentHandler(
	settingsRepo *repository.NoteSettingsRepository,
	mcpConfigPath string,
	publicBaseURL string,
) *SubAgentHandler {
	return &SubAgentHandler{
		settingsRepo:  settingsRepo,
		mcpConfigPath: strings.TrimSpace(mcpConfigPath),
		publicBaseURL: strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
	}
}

// RegisterRoutes 把 sub-agents 路由挂到 protected 组。
// 仅保留 MCP 同步接口；subagent 定义的增删改查通过编辑 markdown 文件完成。
func (h *SubAgentHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/sub-agents/mcp/sync", h.SyncMCPServer)
}

func (h *SubAgentHandler) currentUserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get(middleware.UserIDKey())
	if !exists {
		return 0, false
	}
	uid, ok := v.(uint)
	return uid, ok && uid > 0
}

// ====== MCP 配置同步 ======

// SyncMCPServer 手动同步内置 MCP 条目到 mcp.json（按当前用户 token）。
// 该接口在 token 生成后由前端调用，确保 agent 会话能发现内置 MCP server。
func (h *SubAgentHandler) SyncMCPServer(c *gin.Context) {
	uid, ok := h.currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	st, err := h.settingsRepo.FindByUserID(uid)
	if err != nil || strings.TrimSpace(st.McpToken) == "" {
		Fail(c, http.StatusBadRequest, "NO_MCP_TOKEN", "请先在笔记设置生成 MCP Token")
		return
	}
	if err := h.syncSubagentMCPServer(st.McpToken); err != nil {
		Fail(c, http.StatusInternalServerError, "MCP_SYNC_FAILED", err.Error())
		return
	}
	Success(c, http.StatusOK, gin.H{"synced": true})
}

// syncSubagentMCPServer 把内置 MCP server 条目写入全局 mcp.json，并清理已下线条目的残留。
// 写入失败返回 error（启动自愈场景调用方决定是否仅记日志）。
func (h *SubAgentHandler) syncSubagentMCPServer(token string) error {
	if h.mcpConfigPath == "" || h.publicBaseURL == "" || token == "" {
		return errors.New("mcp 配置缺失：mcpConfigPath / publicBaseURL / token 任一为空")
	}
	h.removeLegacyMCPEntries()
	for _, s := range builtinMCPServers {
		entry := acp.MCPServerEntry{
			Type:    acp.MCPTypeHTTP,
			Url:     h.publicBaseURL + s.Path,
			Headers: map[string]string{"Authorization": "Bearer " + token},
		}
		if err := acp.UpsertMCPServerEntry(h.mcpConfigPath, s.Name, entry); err != nil {
			return err
		}
	}
	return nil
}

// SyncAllSubagentMCP 启动自愈：遍历有 token 的用户，确保内置 MCP server 条目存在于 mcp.json。
// 复用与 SyncAllNotesMCP 一致的全局共享 token 策略（取 list[0] 的 token）。
func (h *SubAgentHandler) SyncAllSubagentMCP() {
	if h.mcpConfigPath == "" || h.publicBaseURL == "" || h.settingsRepo == nil {
		return
	}
	list, err := h.settingsRepo.FindAllWithMcpToken()
	if err != nil {
		log.Printf("加载笔记 MCP token 列表失败（subagent 同步跳过）: %v", err)
		return
	}
	if len(list) == 0 {
		return
	}
	h.removeLegacyMCPEntries()
	token := strings.TrimSpace(list[0].McpToken)
	for _, s := range builtinMCPServers {
		want := acp.MCPServerEntry{
			Type:    acp.MCPTypeHTTP,
			Url:     h.publicBaseURL + s.Path,
			Headers: map[string]string{"Authorization": "Bearer " + token},
		}
		if existing := h.findMCPEntry(s.Name); existing != nil && mcpEntryEqual(*existing, want) {
			log.Printf("%s MCP 已存在于 %s 且配置一致，跳过更新", s.Name, h.mcpConfigPath)
			continue
		}
		if err := acp.UpsertMCPServerEntry(h.mcpConfigPath, s.Name, want); err != nil {
			log.Printf("写入 %s MCP 到 %s 失败: %v", s.Name, h.mcpConfigPath, err)
			continue
		}
		log.Printf("已更新内置 MCP (%s) 到 %s", s.Name, h.mcpConfigPath)
	}
}

// removeLegacyMCPEntries 从 mcp.json 清理已下线内置 MCP server 的残留条目，
// 避免 agent 会话反复尝试连接已不存在的 endpoint。
func (h *SubAgentHandler) removeLegacyMCPEntries() {
	for _, name := range legacyMCPNames {
		if h.findMCPEntry(name) == nil {
			continue
		}
		if err := acp.RemoveMCPServerEntry(h.mcpConfigPath, name); err != nil {
			log.Printf("清理已下线 MCP 条目 %s 失败: %v", name, err)
			continue
		}
		log.Printf("已从 %s 清理下线的 MCP 条目: %s", h.mcpConfigPath, name)
	}
}

func (h *SubAgentHandler) findMCPEntry(name string) *acp.MCPServerEntry {
	entries, err := acp.LoadMCPServerEntries(h.mcpConfigPath)
	if err != nil {
		return nil
	}
	for _, ne := range entries {
		if ne.Name == name {
			e := ne.Entry
			return &e
		}
	}
	return nil
}
