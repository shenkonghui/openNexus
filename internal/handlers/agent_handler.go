package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/gin-gonic/gin"

	acplocal "opennexus/internal/acp"
	"opennexus/internal/agent"
)

// AgentLister 暴露 agent 列表查询能力（*agent.Router 实现该接口）。
type AgentLister interface {
	ListAgents() []*agent.AgentDescriptor
}

// AgentStatusLister 暴露 agent 连接状态查询能力。
type AgentStatusLister interface {
	ListAgentStatus() []acplocal.AgentStatus
}

// AgentACPInfoProvider 暴露 agent 类型最近一次 ACP 握手能力信息的查询能力。
type AgentACPInfoProvider interface {
	AgentACPInfo(agentType string) (acplocal.AgentACPInfo, bool)
}

// AgentModelProber 返回指定 agent 类型的可用模型 config option（从已有会话缓存获取）。
type AgentModelProber interface {
	CachedModelOptions(agentType string) []acpsdk.SessionConfigOption
}

// AgentConfigProber 创建临时会话探测指定 agent 类型的全部 config options，随后删除该会话。
type AgentConfigProber interface {
	ProbeConfigOptions(ctx context.Context, agentType string, userID uint) ([]acpsdk.SessionConfigOption, error)
}

// AgentPreconnector 异步预连接 agent 与工作目录。
type AgentPreconnector interface {
	PreconnectAgent(agentType, cwd string) error
}

// AgentCommandLister 返回指定 agent 类型缓存的 slash command。
type AgentCommandLister interface {
	CachedCommands(agentType string, cwd string) []acpsdk.AvailableCommand
	ListConfiguredCommands(cwd string) []acplocal.SlashCommand
}

// AgentModeLister 返回指定 agent 类型缓存的 session mode。
type AgentModeLister interface {
	CachedModes(agentType string) []acpsdk.SessionMode
}

// AgentHandler 处理 agent 列表相关请求。
type AgentHandler struct {
	lister       AgentLister
	prober       AgentModelProber
	cfgProber    AgentConfigProber
	preconnector AgentPreconnector
	cmdLister    AgentCommandLister
	modeLister   AgentModeLister
	statusLister AgentStatusLister
	acpInfo      AgentACPInfoProvider
	// selectorFilters 是 config.yaml 中 agents.selector.filters 的正则列表，
	// 随 GET /agents 透出，由前端对 agent+模型 合并下拉项做显示过滤。
	selectorFilters []string
}

// NewAgentHandler 创建 AgentHandler。各依赖可为 nil。
func NewAgentHandler(lister AgentLister, prober AgentModelProber, cfgProber AgentConfigProber, statusLister AgentStatusLister) *AgentHandler {
	h := &AgentHandler{lister: lister, prober: prober, cfgProber: cfgProber, statusLister: statusLister}
	if cl, ok := lister.(AgentCommandLister); ok {
		h.cmdLister = cl
	}
	if ml, ok := lister.(AgentModeLister); ok {
		h.modeLister = ml
	}
	if pc, ok := lister.(AgentPreconnector); ok {
		h.preconnector = pc
	}
	if ip, ok := statusLister.(AgentACPInfoProvider); ok {
		h.acpInfo = ip
	}
	return h
}

// SetSelectorFilters 设置 agent+模型 合并下拉的显示过滤正则（来自配置文件）。
func (h *AgentHandler) SetSelectorFilters(filters []string) {
	h.selectorFilters = filters
}

// agentItem 是对外暴露的 agent 描述（隐藏 Backend 等内部字段）。
type agentItem struct {
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// List GET /api/v1/agents — 列出可用 agent 类型。
// selector_filters 是配置的 agent+模型 显示过滤正则（匹配串 "agentType/modelValue"）。
func (h *AgentHandler) List(c *gin.Context) {
	descs := h.lister.ListAgents()
	items := make([]agentItem, 0, len(descs))
	for _, d := range descs {
		items = append(items, agentItem{
			Type:        d.Type,
			DisplayName: d.DisplayName,
			Description: d.Description,
		})
	}
	filters := h.selectorFilters
	if filters == nil {
		filters = []string{}
	}
	Success(c, http.StatusOK, gin.H{"agents": items, "selector_filters": filters})
}

// Status GET /api/v1/agents/status — 返回所有 agent 类型的 ACP 连接状态。
func (h *AgentHandler) Status(c *gin.Context) {
	if h.statusLister == nil {
		Success(c, http.StatusOK, gin.H{"agents": []agentItem{}})
		return
	}
	statuses := h.statusLister.ListAgentStatus()
	Success(c, http.StatusOK, gin.H{"agents": statuses})
}

// acpMethodItem 描述单个 ACP 方法的支持情况（供设置页能力展示）。
type acpMethodItem struct {
	Method    string `json:"method"`
	Supported bool   `json:"supported"`
	// Gate 是门控该方法的 capability 字段路径；空表示协议基线方法（无需声明）。
	Gate     string `json:"gate,omitempty"`
	Unstable bool   `json:"unstable,omitempty"`
}

// acpAuthMethodItem 描述 agent 声明的一种认证方式。
type acpAuthMethodItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "agent" | "env_var" | "terminal"
}

// buildAgentMethodItems 根据握手响应推导 agent-side method（client → agent）支持情况。
// 基线方法协议保证支持；其余按 initialize 返回的 AgentCapabilities 门控。
func buildAgentMethodItems(initResp acpsdk.InitializeResponse) []acpMethodItem {
	caps := initResp.AgentCapabilities
	sc := caps.SessionCapabilities
	nes := caps.Nes != nil
	providers := caps.Providers != nil
	return []acpMethodItem{
		{Method: acpsdk.AgentMethodInitialize, Supported: true},
		{Method: acpsdk.AgentMethodAuthenticate, Supported: len(initResp.AuthMethods) > 0, Gate: "authMethods"},
		{Method: acpsdk.AgentMethodLogout, Supported: caps.Auth.Logout != nil, Gate: "auth.logout"},
		{Method: acpsdk.AgentMethodSessionNew, Supported: true},
		{Method: acpsdk.AgentMethodSessionPrompt, Supported: true},
		{Method: acpsdk.AgentMethodSessionCancel, Supported: true},
		{Method: acpsdk.AgentMethodSessionSetMode, Supported: true},
		{Method: acpsdk.AgentMethodSessionSetConfigOption, Supported: true},
		{Method: acpsdk.AgentMethodSessionLoad, Supported: caps.LoadSession, Gate: "loadSession"},
		{Method: acpsdk.AgentMethodSessionList, Supported: sc.List != nil, Gate: "sessionCapabilities.list"},
		{Method: acpsdk.AgentMethodSessionResume, Supported: sc.Resume != nil, Gate: "sessionCapabilities.resume"},
		{Method: acpsdk.AgentMethodSessionClose, Supported: sc.Close != nil, Gate: "sessionCapabilities.close"},
		{Method: acpsdk.AgentMethodSessionFork, Supported: sc.Fork != nil, Gate: "sessionCapabilities.fork", Unstable: true},
		{Method: acpsdk.AgentMethodSessionDelete, Supported: sc.Delete != nil, Gate: "sessionCapabilities.delete", Unstable: true},
		{Method: acpsdk.AgentMethodMcpMessage, Supported: caps.McpCapabilities.Acp, Gate: "mcpCapabilities.acp", Unstable: true},
		{Method: acpsdk.AgentMethodNesStart, Supported: nes, Gate: "nes", Unstable: true},
		{Method: acpsdk.AgentMethodNesSuggest, Supported: nes, Gate: "nes", Unstable: true},
		{Method: acpsdk.AgentMethodNesAccept, Supported: nes, Gate: "nes", Unstable: true},
		{Method: acpsdk.AgentMethodNesReject, Supported: nes, Gate: "nes", Unstable: true},
		{Method: acpsdk.AgentMethodNesClose, Supported: nes, Gate: "nes", Unstable: true},
		{Method: acpsdk.AgentMethodProvidersList, Supported: providers, Gate: "providers", Unstable: true},
		{Method: acpsdk.AgentMethodProvidersSet, Supported: providers, Gate: "providers", Unstable: true},
		{Method: acpsdk.AgentMethodProvidersDisable, Supported: providers, Gate: "providers", Unstable: true},
	}
}

// buildClientMethodItems 根据本服务握手声明的 client 能力推导 client-side method（agent → client）支持情况。
// elicitation/mcp 等 unstable 方法本服务未实现，固定为不支持。
func buildClientMethodItems(caps acpsdk.ClientCapabilities) []acpMethodItem {
	return []acpMethodItem{
		{Method: acpsdk.ClientMethodSessionRequestPermission, Supported: true},
		{Method: acpsdk.ClientMethodSessionUpdate, Supported: true},
		{Method: acpsdk.ClientMethodFsReadTextFile, Supported: caps.Fs.ReadTextFile, Gate: "fs.readTextFile"},
		{Method: acpsdk.ClientMethodFsWriteTextFile, Supported: caps.Fs.WriteTextFile, Gate: "fs.writeTextFile"},
		{Method: acpsdk.ClientMethodTerminalCreate, Supported: caps.Terminal, Gate: "terminal"},
		{Method: acpsdk.ClientMethodTerminalOutput, Supported: caps.Terminal, Gate: "terminal"},
		{Method: acpsdk.ClientMethodTerminalWaitForExit, Supported: caps.Terminal, Gate: "terminal"},
		{Method: acpsdk.ClientMethodTerminalKill, Supported: caps.Terminal, Gate: "terminal"},
		{Method: acpsdk.ClientMethodTerminalRelease, Supported: caps.Terminal, Gate: "terminal"},
		{Method: acpsdk.ClientMethodElicitationCreate, Supported: false, Gate: "elicitation", Unstable: true},
		{Method: acpsdk.ClientMethodElicitationComplete, Supported: false, Gate: "elicitation", Unstable: true},
		{Method: acpsdk.ClientMethodMcpConnect, Supported: false, Gate: "mcp", Unstable: true},
		{Method: acpsdk.ClientMethodMcpDisconnect, Supported: false, Gate: "mcp", Unstable: true},
		{Method: acpsdk.ClientMethodMcpMessage, Supported: false, Gate: "mcp", Unstable: true},
	}
}

// buildAuthMethodItems 把握手返回的认证方式转为对外展示结构。
func buildAuthMethodItems(methods []acpsdk.AuthMethod) []acpAuthMethodItem {
	items := make([]acpAuthMethodItem, 0, len(methods))
	for _, m := range methods {
		switch {
		case m.EnvVar != nil:
			items = append(items, acpAuthMethodItem{ID: m.EnvVar.Id, Name: m.EnvVar.Name, Type: "env_var"})
		case m.Terminal != nil:
			items = append(items, acpAuthMethodItem{ID: m.Terminal.Id, Name: m.Terminal.Name, Type: "terminal"})
		case m.Agent != nil:
			items = append(items, acpAuthMethodItem{ID: m.Agent.Id, Name: m.Agent.Name, Type: "agent"})
		}
	}
	return items
}

// Capabilities GET /api/v1/agents/:type/capabilities — 返回指定 agent 类型最近一次 ACP 握手的能力信息。
// 包含 agent-side / client-side method 支持情况；若从未握手则 available=false（前端可引导预连接）。
func (h *AgentHandler) Capabilities(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.acpInfo == nil {
		Fail(c, http.StatusServiceUnavailable, "CAPS_UNAVAILABLE", "当前服务不支持能力查询")
		return
	}
	info, ok := h.acpInfo.AgentACPInfo(agentType)
	if !ok {
		Success(c, http.StatusOK, gin.H{"agent_type": agentType, "available": false})
		return
	}
	agentName, agentVersion := "", ""
	if ai := info.InitResp.AgentInfo; ai != nil {
		agentName, agentVersion = ai.Name, ai.Version
		if ai.Title != nil && *ai.Title != "" {
			agentName = *ai.Title
		}
	}
	pc := info.InitResp.AgentCapabilities.PromptCapabilities
	mc := info.InitResp.AgentCapabilities.McpCapabilities
	Success(c, http.StatusOK, gin.H{
		"agent_type":       agentType,
		"available":        true,
		"protocol_version": int(info.InitResp.ProtocolVersion),
		"agent_name":       agentName,
		"agent_version":    agentVersion,
		"auth_methods":     buildAuthMethodItems(info.InitResp.AuthMethods),
		"prompt_capabilities": gin.H{
			"image":            pc.Image,
			"audio":            pc.Audio,
			"embedded_context": pc.EmbeddedContext,
		},
		"mcp_capabilities": gin.H{"http": mc.Http, "sse": mc.Sse, "acp": mc.Acp},
		"agent_methods":    buildAgentMethodItems(info.InitResp),
		"client_methods":   buildClientMethodItems(info.ClientCaps),
	})
}

// modelOptionItem 是对外暴露的模型 config option 描述。
type modelOptionItem struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	CurrentValue string              `json:"current_value"`
	Options      []configOptionValue `json:"options"`
}

// Models GET /api/v1/agents/:type/models — 返回指定 agent 类型的可用模型列表。
// 从已有会话缓存获取；若该 agent 类型尚无会话则返回空列表。
func (h *AgentHandler) Models(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.prober == nil {
		Success(c, http.StatusOK, gin.H{"model_options": []modelOptionItem{}})
		return
	}
	opts := h.prober.CachedModelOptions(agentType)
	items := make([]modelOptionItem, 0, len(opts))
	for _, opt := range opts {
		if opt.Select == nil {
			continue
		}
		item := modelOptionItem{
			ID:           string(opt.Select.Id),
			Name:         opt.Select.Name,
			CurrentValue: string(opt.Select.CurrentValue),
			// 初始化为空切片，避免 JSON 序列化为 null 导致前端 o.options.length 崩溃
			Options: []configOptionValue{},
		}
		if opt.Select.Options.Ungrouped != nil {
			for _, o := range *opt.Select.Options.Ungrouped {
				desc := ""
				if o.Description != nil {
					desc = *o.Description
				}
				item.Options = append(item.Options, configOptionValue{
					Value:       string(o.Value),
					Name:        o.Name,
					Description: desc,
				})
			}
		}
		if opt.Select.Options.Grouped != nil {
			for _, g := range *opt.Select.Options.Grouped {
				for _, o := range g.Options {
					desc := ""
					if o.Description != nil {
						desc = *o.Description
					}
					item.Options = append(item.Options, configOptionValue{
						Value:       string(o.Value),
						Name:        o.Name,
						Description: desc,
					})
				}
			}
		}
		items = append(items, item)
	}
	Success(c, http.StatusOK, gin.H{"model_options": items})
}

// Probe POST /api/v1/agents/:type/probe — 创建临时会话探测该 agent 的全部 config options，随后删除。
// 返回与 GET /sessions/:id/config-options 相同结构的 config_options 列表（含模型及其他配置）。
func (h *AgentHandler) Probe(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.cfgProber == nil {
		Fail(c, http.StatusServiceUnavailable, "PROBE_UNAVAILABLE", "当前服务不支持配置探测")
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	opts, err := h.cfgProber.ProbeConfigOptions(c.Request.Context(), agentType, uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "PROBE_FAILED", err.Error())
		return
	}
	items := make([]configOptionItem, 0, len(opts))
	for _, opt := range opts {
		item := configOptionItem{Type: "boolean", Options: []configOptionValue{}}
		if opt.Select != nil {
			item.ID = string(opt.Select.Id)
			item.Name = opt.Select.Name
			item.Type = "select"
			item.CurrentValue = string(opt.Select.CurrentValue)
			if opt.Select.Category != nil {
				item.Category = string(*opt.Select.Category)
			}
			if opt.Select.Options.Ungrouped != nil {
				for _, o := range *opt.Select.Options.Ungrouped {
					desc := ""
					if o.Description != nil {
						desc = *o.Description
					}
					item.Options = append(item.Options, configOptionValue{
						Value:       string(o.Value),
						Name:        o.Name,
						Description: desc,
					})
				}
			}
			if opt.Select.Options.Grouped != nil {
				for _, g := range *opt.Select.Options.Grouped {
					for _, o := range g.Options {
						desc := ""
						if o.Description != nil {
							desc = *o.Description
						}
						item.Options = append(item.Options, configOptionValue{
							Value:       string(o.Value),
							Name:        o.Name,
							Description: desc,
						})
					}
				}
			}
		}
		items = append(items, item)
	}
	Success(c, http.StatusOK, gin.H{"config_options": items})
}

// Preconnect POST /api/v1/agents/:type/preconnect — 异步预连接 agent。
// 使用 probeCwd 建立共享连接（供新建会话页预热），cwd 参数仅在 NewSession 时传入。
func (h *AgentHandler) Preconnect(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.preconnector == nil {
		Fail(c, http.StatusServiceUnavailable, "PRECONNECT_UNAVAILABLE", "当前服务不支持预连接")
		return
	}
	// 始终使用空 cwd 建立共享连接，实际会话 cwd 由 NewSession 传入
	if err := h.preconnector.PreconnectAgent(agentType, ""); err != nil {
		if errors.Is(err, agent.ErrAgentNotFound) {
			Fail(c, http.StatusBadRequest, "AGENT_NOT_FOUND", "未知的 agent 类型")
			return
		}
		Fail(c, http.StatusInternalServerError, "PRECONNECT_FAILED", err.Error())
		return
	}
	Success(c, http.StatusAccepted, gin.H{"status": "accepted"})
}

// Commands GET /api/v1/agents/:type/commands — 返回 agent 类型缓存的 slash command（新建任务页用）。
func (h *AgentHandler) Commands(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.cmdLister == nil {
		Success(c, http.StatusOK, gin.H{"commands": []commandItem{}})
		return
	}
	cmds := h.cmdLister.CachedCommands(agentType, strings.TrimSpace(c.Query("path")))
	configured := h.cmdLister.ListConfiguredCommands(strings.TrimSpace(c.Query("path")))
	items := buildCommandItems(cmds, configured)
	Success(c, http.StatusOK, gin.H{"commands": items})
}

// Modes GET /api/v1/agents/:type/modes — 返回 agent 类型缓存的 session mode（新建任务页用）。
func (h *AgentHandler) Modes(c *gin.Context) {
	agentType := strings.TrimSpace(c.Param("type"))
	if agentType == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "缺少 agent 类型")
		return
	}
	if h.modeLister == nil {
		Success(c, http.StatusOK, gin.H{"modes": []modeItem{}})
		return
	}
	modes := h.modeLister.CachedModes(agentType)
	items := make([]modeItem, 0, len(modes))
	for _, m := range modes {
		desc := ""
		if m.Description != nil {
			desc = *m.Description
		}
		items = append(items, modeItem{
			ID:          string(m.Id),
			Name:        m.Name,
			Description: desc,
		})
	}
	Success(c, http.StatusOK, gin.H{"modes": items})
}
