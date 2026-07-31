package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"opennexus/internal/config"
)

// errInvalidConfigRoot config.yaml 根节点结构异常（非预期的 YAML 文档）。
var errInvalidConfigRoot = errors.New("config.yaml 根节点结构异常")

// PermissionRuleApplier 在保存权限规则后把规则下发到运行中的连接（由 *agent.Router 实现）。
type PermissionRuleApplier interface {
	ApplyPermissions(mode string, allow, ask, deny []string)
	// ApplySandbox 热更新全局沙箱开关（新 spawn 的 agent 生效；须在 ApplyPermissions 前调用，
	// 影响默认 Ask 名单是否合并）。
	ApplySandbox(enabled bool, mode string)
}

// PermissionSettingsHandler 处理全局权限规则配置（yolo / 白名单 / 黑名单）。
// 配置持久化在 config.yaml 的 permissions 段（不再入库）：GET 读文件，PUT 写文件并热更新。
type PermissionSettingsHandler struct {
	configPath string
	applier    PermissionRuleApplier
}

func NewPermissionSettingsHandler(configPath string, applier PermissionRuleApplier) *PermissionSettingsHandler {
	return &PermissionSettingsHandler{configPath: configPath, applier: applier}
}

type permissionSettingsItem struct {
	Mode    string              `json:"mode"`  // normal | yolo
	Allow   []string            `json:"allow"` // 白名单
	Ask     []string            `json:"ask"`   // 询问名单
	Deny    []string            `json:"deny"`  // 黑名单
	Sandbox sandboxSettingsItem `json:"sandbox"`
}

// sandboxSettingsItem 是全局沙箱开关（持久化在 config.yaml 的 sandbox 段）。
type sandboxSettingsItem struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"` // auto | enforce
}

type permissionSettingsRequest struct {
	Mode  string   `json:"mode"`
	Allow []string `json:"allow"`
	Ask   []string `json:"ask"`
	Deny  []string `json:"deny"`
	// Sandbox 缺省（nil）时保留 config.yaml 现值——兼容仅切 YOLO 的旧调用方。
	Sandbox *sandboxSettingsItem `json:"sandbox"`
}

// GetSettings GET /api/v1/permissions/settings
// 从 config.yaml 的 permissions 段读取当前配置。
func (h *PermissionSettingsHandler) GetSettings(c *gin.Context) {
	root, err := readConfigRaw(h.configPath)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_READ_ERROR", "读取配置文件失败")
		return
	}
	Success(c, http.StatusOK, extractPermissionsView(root))
}

// UpdateSettings PUT /api/v1/permissions/settings
// 写回 config.yaml 的 permissions 段，并把新规则热更新到所有连接。
func (h *PermissionSettingsHandler) UpdateSettings(c *gin.Context) {
	var req permissionSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	root, err := readConfigRaw(h.configPath)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_READ_ERROR", "读取配置文件失败")
		return
	}
	item := permissionSettingsItem{
		Mode:  normalizePermMode(req.Mode),
		Allow: cleanRuleList(req.Allow),
		Ask:   cleanRuleList(req.Ask),
		Deny:  cleanRuleList(req.Deny),
	}
	if req.Sandbox != nil {
		item.Sandbox = sandboxSettingsItem{Enabled: req.Sandbox.Enabled, Mode: normalizeSandboxMode(req.Sandbox.Mode)}
	} else {
		// 请求未带 sandbox：保留 config.yaml 现值（兼容仅切 YOLO 的旧调用方）
		item.Sandbox = extractSandboxView(root)
	}
	if err := upsertPermissionsNode(root, item); err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_WRITE_ERROR", "更新配置失败")
		return
	}
	if err := upsertSandboxNode(root, item.Sandbox); err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_WRITE_ERROR", "更新配置失败")
		return
	}
	if err := writeConfigRaw(h.configPath, root); err != nil {
		Fail(c, http.StatusInternalServerError, "CONFIG_WRITE_ERROR", "写入配置文件失败")
		return
	}

	// 写盘成功后热更新：先沙箱（影响默认 Ask 名单合并）再下发新规则到所有连接的 broker
	if h.applier != nil {
		h.applier.ApplySandbox(item.Sandbox.Enabled, item.Sandbox.Mode)
		h.applier.ApplyPermissions(item.Mode, item.Allow, item.Ask, item.Deny)
	}
	Success(c, http.StatusOK, item)
}

// normalizePermMode 兜底权限模式（空或非法值回退 normal）。
func normalizePermMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode != config.PermissionModeNormal && mode != config.PermissionModeYolo {
		return config.PermissionModeNormal
	}
	return mode
}

// cleanRuleList 去空白、去重、去空行。
func cleanRuleList(list []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(list))
	for _, r := range list {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// extractPermissionsView 从 yaml.Node 中提取 permissions 段（缺失时返回默认 normal + 空列表）。
func extractPermissionsView(root *yaml.Node) permissionSettingsItem {
	item := permissionSettingsItem{Mode: config.PermissionModeNormal, Allow: []string{}, Ask: []string{}, Deny: []string{}}
	item.Sandbox = extractSandboxView(root)
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return item
	}
	permNode := findMappingValue(root.Content[0], "permissions")
	if permNode == nil || permNode.Kind != yaml.MappingNode {
		return item
	}
	if modeNode := findMappingValue(permNode, "mode"); modeNode != nil && modeNode.Kind == yaml.ScalarNode {
		item.Mode = normalizePermMode(modeNode.Value)
	}
	if n := findMappingValue(permNode, "allow"); n != nil {
		item.Allow = extractStringList(n)
	}
	if n := findMappingValue(permNode, "ask"); n != nil {
		item.Ask = extractStringList(n)
	}
	if n := findMappingValue(permNode, "deny"); n != nil {
		item.Deny = extractStringList(n)
	}
	return item
}

// upsertPermissionsNode 在根映射中新建或替换 permissions 段。
func upsertPermissionsNode(root *yaml.Node, item permissionSettingsItem) error {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return errInvalidConfigRoot
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return errInvalidConfigRoot
	}
	valueNode := buildPermissionsNode(item)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "permissions" {
			mapping.Content[i+1] = valueNode
			return nil
		}
	}
	// 不存在则追加 permissions 键
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "permissions"}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return nil
}

// buildPermissionsNode 构造 permissions 映射节点（mode 标量 + allow/ask/deny 序列）。
func buildPermissionsNode(item permissionSettingsItem) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendScalar := func(key, val string) {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val},
		)
	}
	appendSeq := func(key string, values []string) {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: make([]*yaml.Node, len(values))}
		for i, v := range values {
			seq.Content[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
		}
		// 空序列用流式 [] 表示，避免写出裸键
		if len(values) == 0 {
			seq.Style = yaml.FlowStyle
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			seq,
		)
	}
	appendScalar("mode", item.Mode)
	appendSeq("allow", item.Allow)
	appendSeq("ask", item.Ask)
	appendSeq("deny", item.Deny)
	return node
}

// normalizeSandboxMode 兜底沙箱模式（空或非法值回退 auto）。
func normalizeSandboxMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode != config.SandboxModeAuto && mode != config.SandboxModeEnforce {
		return config.SandboxModeAuto
	}
	return mode
}

// extractSandboxView 从 yaml.Node 中提取 sandbox 段（缺失时返回关闭 + auto）。
func extractSandboxView(root *yaml.Node) sandboxSettingsItem {
	item := sandboxSettingsItem{Enabled: false, Mode: config.SandboxModeAuto}
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return item
	}
	sbNode := findMappingValue(root.Content[0], "sandbox")
	if sbNode == nil || sbNode.Kind != yaml.MappingNode {
		return item
	}
	if n := findMappingValue(sbNode, "enabled"); n != nil && n.Kind == yaml.ScalarNode {
		item.Enabled = n.Value == "true"
	}
	if n := findMappingValue(sbNode, "mode"); n != nil && n.Kind == yaml.ScalarNode {
		item.Mode = normalizeSandboxMode(n.Value)
	}
	return item
}

// upsertSandboxNode 在根映射中新建或替换 sandbox 段。
func upsertSandboxNode(root *yaml.Node, item sandboxSettingsItem) error {
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
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
		{Kind: yaml.ScalarNode, Tag: "!!bool", Value: enabledVal},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mode"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: item.Mode},
	}}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "sandbox" {
			mapping.Content[i+1] = valueNode
			return nil
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "sandbox"}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return nil
}
