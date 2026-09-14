package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"opennexus/internal/config"
	"opennexus/internal/services"
)

// TunnelHandler 提供 Cloudflare 公网隧道的状态查询、启停与配置读写。
// 配置持久化在 config.yaml 的 tunnel 段；运行状态由 TunnelService 管理。
type TunnelHandler struct {
	configPath string
	svc        *services.TunnelService
}

func NewTunnelHandler(configPath string, svc *services.TunnelService) *TunnelHandler {
	return &TunnelHandler{configPath: configPath, svc: svc}
}

// tunnelView 是返回给前端的隧道视图：运行状态 + 当前配置（token 不回传明文）。
type tunnelView struct {
	services.TunnelStatus
	Enabled         bool   `json:"enabled"`   // 启动时自动开启
	Hostname        string `json:"hostname"`  // token 模式对外域名（展示用）
	HasToken        bool   `json:"has_token"` // 是否已配置 token
	CloudflaredPath string `json:"cloudflared_path"`
}

// Get GET /api/v1/tunnel
func (h *TunnelHandler) Get(c *gin.Context) {
	view := tunnelView{TunnelStatus: h.svc.Status()}
	if root, err := readConfigRaw(h.configPath); err == nil {
		cfg := extractTunnelView(root)
		view.Enabled = cfg.Enabled
		view.Hostname = cfg.Hostname
		view.HasToken = cfg.Token != ""
		view.CloudflaredPath = cfg.CloudflaredPath
	}
	Success(c, http.StatusOK, view)
}

// tunnelConfigRequest 是 PUT /api/v1/tunnel 的请求体。
// Token 为指针：缺省（null/未传）保留 config.yaml 现值，避免每次保存必须重填。
type tunnelConfigRequest struct {
	Enabled         bool    `json:"enabled"`
	Mode            string  `json:"mode"`
	Token           *string `json:"token"`
	Hostname        string  `json:"hostname"`
	CloudflaredPath string  `json:"cloudflared_path"`
}

// Update PUT /api/v1/tunnel
// 写回 config.yaml 的 tunnel 段并热更新到 TunnelService（影响下次 Start；运行中的隧道不中断）。
func (h *TunnelHandler) Update(c *gin.Context) {
	var req tunnelConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode != config.TunnelModeQuick && mode != config.TunnelModeToken {
		mode = config.TunnelModeQuick
	}

	root, err := readConfigRaw(h.configPath)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_READ_ERROR", "读取配置文件失败")
		return
	}
	item := config.TunnelConfig{
		Enabled:         req.Enabled,
		Mode:            mode,
		Hostname:        strings.TrimSpace(req.Hostname),
		CloudflaredPath: strings.TrimSpace(req.CloudflaredPath),
	}
	if req.Token != nil {
		item.Token = strings.TrimSpace(*req.Token)
	} else {
		item.Token = extractTunnelView(root).Token // 未传则保留现值
	}
	if mode == config.TunnelModeToken && item.Token == "" {
		Fail(c, http.StatusBadRequest, "INVALID_CONFIG", "token 模式需要填写 Tunnel Token")
		return
	}
	if err := upsertTunnelNode(root, item); err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_WRITE_ERROR", "更新配置失败")
		return
	}
	if err := writeConfigRaw(h.configPath, root); err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_WRITE_ERROR", "写入配置文件失败")
		return
	}

	// 走 Load+Validate 复用 normalize（模式兜底、~ 路径展开），再热更新到服务
	if cfg, err := config.Load(h.configPath); err == nil {
		if err := cfg.Validate(); err == nil {
			h.svc.SetConfig(cfg.Tunnel)
		}
	}
	Success(c, http.StatusOK, gin.H{"message": "隧道配置已保存"})
}

// Start POST /api/v1/tunnel/start
// 异步就绪：立即返回当前状态（starting），前端轮询 GET 等待 running/error。
// 启动即失败（未安装 / 缺 token）也返回状态而非 500，前端从 error 字段展示原因。
func (h *TunnelHandler) Start(c *gin.Context) {
	_ = h.svc.Start()
	Success(c, http.StatusOK, h.svc.Status())
}

// Stop POST /api/v1/tunnel/stop
func (h *TunnelHandler) Stop(c *gin.Context) {
	h.svc.Stop()
	Success(c, http.StatusOK, h.svc.Status())
}

// extractTunnelView 从 yaml.Node 中提取 tunnel 段（缺失时返回默认值）。
func extractTunnelView(root *yaml.Node) config.TunnelConfig {
	item := config.TunnelConfig{Mode: config.TunnelModeQuick}
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return item
	}
	tNode := findMappingValue(root.Content[0], "tunnel")
	if tNode == nil || tNode.Kind != yaml.MappingNode {
		return item
	}
	if n := findMappingValue(tNode, "enabled"); n != nil && n.Kind == yaml.ScalarNode {
		item.Enabled = n.Value == "true"
	}
	if n := findMappingValue(tNode, "mode"); n != nil && n.Kind == yaml.ScalarNode {
		m := strings.TrimSpace(n.Value)
		if m == config.TunnelModeQuick || m == config.TunnelModeToken {
			item.Mode = m
		}
	}
	if n := findMappingValue(tNode, "token"); n != nil && n.Kind == yaml.ScalarNode {
		item.Token = n.Value
	}
	if n := findMappingValue(tNode, "hostname"); n != nil && n.Kind == yaml.ScalarNode {
		item.Hostname = n.Value
	}
	if n := findMappingValue(tNode, "cloudflared_path"); n != nil && n.Kind == yaml.ScalarNode {
		item.CloudflaredPath = n.Value
	}
	return item
}

// upsertTunnelNode 在根映射中新建或替换 tunnel 段。
func upsertTunnelNode(root *yaml.Node, item config.TunnelConfig) error {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return errInvalidConfigRoot
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return errInvalidConfigRoot
	}
	enabledVal := "false"
	if item.Enabled {
		enabledVal = "true"
	}
	str := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		str("enabled"), {Kind: yaml.ScalarNode, Tag: "!!bool", Value: enabledVal},
		str("mode"), str(item.Mode),
		str("token"), str(item.Token),
		str("hostname"), str(item.Hostname),
		str("cloudflared_path"), str(item.CloudflaredPath),
	}}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "tunnel" {
			mapping.Content[i+1] = valueNode
			return nil
		}
	}
	mapping.Content = append(mapping.Content, str("tunnel"), valueNode)
	return nil
}
