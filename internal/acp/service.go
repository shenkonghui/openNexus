package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"opennexus/internal/config"
	"opennexus/internal/logging"
	"opennexus/internal/models"
	"opennexus/internal/repository"
	"opennexus/internal/workspacemeta"
)

var (
	ErrBackendNotFound  = errors.New("后端未注册")
	ErrSessionNotFound  = errors.New("会话不存在")
	ErrSessionNotActive = errors.New("会话不在活跃状态")
	ErrSessionClosed    = errors.New("会话已关闭，无法恢复")
)

// 连接状态常量。
const (
	connStateConnected    = "connected"
	connStateConnecting   = "connecting"
	connStateDisconnected = "disconnected"
)

// 健康检查与自动重连参数。
const (
	healthCheckInterval = 30 * time.Second // 健康检查周期
	reconnectBaseDelay  = 5 * time.Second  // 重连基础退避
	reconnectMaxDelay   = 60 * time.Second // 重连最大退避
)

// defaultPromptMaxDuration 是单轮 prompt 的默认最大存活时间。
// prompt 的生命周期独立于 HTTP/SSE 请求（解耦后用 context.Background 派生），
// 此超时兜底防止 agent 卡死导致 goroutine 永久泄漏。超时后标记 interrupted。
const defaultPromptMaxDuration = 30 * time.Minute

// Service 是 ACP 客户端高层服务，串联后端、连接、工作区与持久化。
//
// 连接池模型：每个 agent 类型 + 工作目录共享一条 ACP 连接（一个 agent 进程），
// 多个会话通过 SessionId 在同一条连接上多路复用。
// sessionPoolKey 维护 sessionID → 连接池键 的路由，用于定位所属连接。
//
// 连接生命周期由后台健康检查 goroutine 管理：
//   - 进程退出后标记 disconnected，定时任务自动重连
//   - 重连带指数退避，避免频繁重试
type Service struct {
	sessions   *repository.SessionRepository
	messages   *repository.MessageRepository
	workspaces *repository.WorkspaceRepository
	backends   map[string]Backend
	// pool 按 agentType+cwd 共享一条 Connection（进程 cwd 与 ACP session cwd 对齐）。
	pool map[string]*Connection
	// states 记录每个连接池键的状态（connecting/connected/disconnected）。
	states map[string]string
	// connectDone 在 connecting 期间供其他 goroutine 等待，完成后 close。
	connectDone map[string]chan struct{}
	// sessionPoolKey 记录 sessionID → 连接池键，用于定位所属连接。
	sessionPoolKey map[string]string
	commands       map[string][]acp.AvailableCommand
	configs        map[string][]acp.SessionConfigOption
	modes          map[string][]acp.SessionMode
	// probeCache 缓存探测结果，按 agentType 存储，避免重复创建临时会话探测。
	probeCache map[string][]acp.SessionConfigOption
	// agentInitInfo 按 agentType 缓存最近一次 ACP 握手响应（能力/认证方式/协议版本），
	// 断开后保留最后一次握手结果，供设置页展示 ACP 能力。
	agentInitInfo map[string]acp.InitializeResponse
	// agentCommands / agentModes 按 agentType 缓存，供新建任务页使用（无会话时）。
	agentCommands    map[string][]acp.AvailableCommand
	agentModes       map[string][]acp.SessionMode
	probeLock        sync.Mutex // 缓存未命中时串行探测，避免并发重复建临时 session
	mu               sync.RWMutex
	wsConfig         config.WorkspaceConfig
	skillUserDirs    []string
	skillProjectDirs []string
	noteSettings     *repository.NoteSettingsRepository
	publicBaseURL    string
	mcpConfigPath    string
	// gatewayEndpoint / gatewayToken 由主程序通过 SetGatewayEndpoint 注入。
	// 非空时 configuredMCPServers 会默认把网关 endpoint 注入给所有会话，
	// 并收敛被网关代理的 http/sse 上游，无需用户手动启用网关条目。
	gatewayEndpoint     string
	gatewayToken        string
	commandUserDirs     []string
	commandProjectDirs  []string
	ruleUserDirs        []string
	ruleProjectDirs     []string
	subAgentUserDirs    []string
	subAgentProjectDirs []string

	// 健康检查与自动重连控制
	hcCtx        context.Context
	hcCancel     context.CancelFunc
	hcWG         sync.WaitGroup
	shuttingDown atomic.Bool
	// hcStartOnce / hcStopOnce 保证 Start/StopHealthCheck 幂等：
	// 多次调用 Start 会用 sync.Once 丢弃后续调用，避免覆盖 hcCancel 造成 goroutine 永久泄漏；
	// 多次调用 Stop 同样安全。
	hcStartOnce sync.Once
	hcStopOnce  sync.Once

	// activePrompts 记录每个会话正在进行的 prompt 广播器，支持多客户端订阅（断点续传重连）。
	activePrompts map[string]*msgBroadcaster
	// runningTasks 记录进行中任务，用于服务重启后的中断恢复。
	runningTasks *repository.RunningTaskRepository

	// taskMetaTrigger 可选：发起任务时异步触发自动打标签 / 标题生成。nil 则跳过。
	taskMetaTrigger TaskMetaTrigger

	// promptFinished 可选：prompt 流结束时通知外部（如任务管理同步 tasks.json 状态）。nil 则跳过。
	promptFinished PromptFinishedNotifier

	// toolCallRecords 可选：工具调用历史仓库（SetToolCallRecordRepo 注入）。nil 则不记录。
	toolCallRecords *repository.ToolCallRecordRepository

	// dbg 可选：ACP 协议调试捕获器。nil 或未启用时零开销。
	dbg *ACPDebugger

	// acpConnRepo 可选：acp_connections 心跳表仓库，与独立 watchdog 进程通信。
	// 为 nil 时（测试环境）心跳与活动记录静默跳过，不影响核心连接逻辑。
	acpConnRepo *repository.ACPConnectionRepository

	// promptMaxDuration 单轮 prompt 最大存活时间。prompt 的 ctx 用 context.Background 派生
	// 并挂此超时，使 prompt 生命周期独立于发起它的 HTTP/SSE 请求（SSE 断开不再误杀 agent），
	// 同时兜底防 agent 卡死导致 goroutine 永久泄漏。SetPromptMaxDuration 注入；0=默认 30min。
	promptMaxDuration time.Duration

	// failedTaskAutoRetryOnce 运行中 agent 崩溃时是否自动重连并重发同一 prompt（仅一次）。
	// 默认 true；由 SetFailedTaskAutoRetryOnce 注入。
	failedTaskAutoRetryOnce bool

	// activePermRules 当前生效的全局权限规则（白/询问/黑名单，全局下发到所有连接的 broker）。
	// 规则来自 config.yaml 的 permissions 段：启动时由 ApplyPermissions 下发，
	// 设置页保存后热更新。nil=无规则（未开会话 yolo 时全部询问）。
	activePermRules atomic.Pointer[PermissionRules]
	// sessionYolo 按 ACP SessionId 记录会话级 YOLO 开关（名单仍走 activePermRules）。
	sessionYolo sync.Map

	// terminalBridge 是 ACP terminal 能力桥接器（所有连接共享）：代 agent 执行 shell
	// 并把执行事件按 DB session ID 广播给前端终端面板。
	terminalBridge *TerminalBridge
	// terminalEnabled 握手时是否向 agent 声明 terminal 能力。默认 true；
	// 由 SetTerminalEnabled 注入（config.yaml agents.terminal_enabled）。
	terminalEnabled bool

	// goals 按稳定 session_id 记录生效中的通用 goal（/opennexus-goal 命令，内存态不跨重启）。
	// goalSettings 可选：评估 agent/模型与限制条件（SetGoalSettingsRepo 注入）。nil 用默认限制。
	goals        map[string]*sessionGoal
	goalMu       sync.Mutex
	goalSettings *repository.GoalSettingsRepository
}

// TaskMetaTrigger 由任务元数据服务实现，发起任务时异步调用以打标签和生成标题。
type TaskMetaTrigger interface {
	ProcessTask(userID, dbSessionID uint, prompt string)
}

// SetTaskMetaTrigger 注入任务元数据触发器。
func (s *Service) SetTaskMetaTrigger(t TaskMetaTrigger) {
	s.taskMetaTrigger = t
}

// PromptFinishedNotifier 在会话 prompt 流结束时被回调（status 为 RunningTaskStatus* 常量），
// 由 *services.TaskManagerService 实现：同步 tasks.json 中会话登记任务的运行状态。
type PromptFinishedNotifier interface {
	PromptFinished(dbSessionID uint, status string)
}

// SetPromptFinishedNotifier 注入 prompt 结束通知器。
func (s *Service) SetPromptFinishedNotifier(n PromptFinishedNotifier) {
	s.promptFinished = n
}

// NewService 创建新的 Service。
// messagesDir 为会话消息 JSONL 目录（通常为 {data-dir}/messages）。
func NewService(db *gorm.DB, messagesDir string, wsConfig config.WorkspaceConfig, skillsConfig config.SkillsConfig, commandsConfig config.CommandsConfig, rulesConfig config.RulesConfig, subAgentsConfig config.SubAgentsConfig) *Service {
	svc := &Service{
		sessions:                repository.NewSessionRepository(db),
		messages:                repository.NewMessageRepository(messagesDir),
		workspaces:              repository.NewWorkspaceRepository(db),
		backends:                make(map[string]Backend),
		pool:                    make(map[string]*Connection),
		states:                  make(map[string]string),
		connectDone:             make(map[string]chan struct{}),
		sessionPoolKey:          make(map[string]string),
		commands:                make(map[string][]acp.AvailableCommand),
		configs:                 make(map[string][]acp.SessionConfigOption),
		modes:                   make(map[string][]acp.SessionMode),
		probeCache:              make(map[string][]acp.SessionConfigOption),
		agentInitInfo:           make(map[string]acp.InitializeResponse),
		agentCommands:           make(map[string][]acp.AvailableCommand),
		agentModes:              make(map[string][]acp.SessionMode),
		activePrompts:           make(map[string]*msgBroadcaster),
		goals:                   make(map[string]*sessionGoal),
		runningTasks:            repository.NewRunningTaskRepository(db),
		wsConfig:                wsConfig,
		skillUserDirs:           append([]string(nil), skillsConfig.UserDirs...),
		skillProjectDirs:        append([]string(nil), skillsConfig.ProjectDirs...),
		commandUserDirs:         append([]string(nil), commandsConfig.UserDirs...),
		commandProjectDirs:      append([]string(nil), commandsConfig.ProjectDirs...),
		ruleUserDirs:            append([]string(nil), rulesConfig.UserDirs...),
		ruleProjectDirs:         append([]string(nil), rulesConfig.ProjectDirs...),
		subAgentUserDirs:        append([]string(nil), subAgentsConfig.UserDirs...),
		subAgentProjectDirs:     append([]string(nil), subAgentsConfig.ProjectDirs...),
		failedTaskAutoRetryOnce: true, // 默认开启；可由 SetFailedTaskAutoRetryOnce 覆盖
		terminalEnabled:         true, // 默认开启；可由 SetTerminalEnabled 覆盖
	}
	// terminal 事件按 agent_session_id 反查 DB 会话路由到前端；查不到（如临时探测会话）则不路由
	svc.terminalBridge = NewTerminalBridge(func(sid acp.SessionId) uint {
		sess, err := svc.sessions.FindByAgentSessionID(string(sid))
		if err != nil {
			return 0
		}
		return sess.ID
	})
	return svc
}

// SetTerminalEnabled 设置握手时是否向 agent 声明 terminal 能力（仅对之后新建的连接生效）。
func (s *Service) SetTerminalEnabled(enabled bool) {
	s.terminalEnabled = enabled
}

// SubscribeAgentTerminal 按 DB 会话 ID 订阅 agent 终端事件（供前端 WebSocket 桥接）。
// 返回订阅时刻的活跃终端快照、事件 channel 与取消函数。
func (s *Service) SubscribeAgentTerminal(dbSessionID uint) ([]TerminalSnapshot, <-chan TerminalEvent, func()) {
	return s.terminalBridge.Subscribe(dbSessionID)
}

// SetNotesMCP 注入笔记 MCP 设置仓库与对外 Base URL（供 NewSession 注入）。
func (s *Service) SetNotesMCP(settings *repository.NoteSettingsRepository, publicBaseURL string) {
	s.noteSettings = settings
	s.publicBaseURL = strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
}

// SetMCPConfigPath 注入全局共享 MCP server 配置文件路径（供 NewSession 注入）。
// 该文件（标准 mcpServers 格式）中的 server 会注入给所有 agent 会话。
func (s *Service) SetMCPConfigPath(path string) {
	s.mcpConfigPath = strings.TrimSpace(path)
}

// SetGatewayEndpoint 注入 MCP 聚合网关的对外 endpoint 与共享 token。
// 两者均非空时，configuredMCPServers 会默认把网关注入给所有会话，
// 并收敛被网关代理的 http/sse 上游——无需用户手动启用网关条目。
// token 为空（用户尚未生成 MCP Token）时不注入网关，退回原行为。
func (s *Service) SetGatewayEndpoint(endpoint, token string) {
	s.gatewayEndpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	s.gatewayToken = strings.TrimSpace(token)
}

// SetScanDirs 热刷新 skill/command/rule/subagent 的扫描目录配置。
// 用于"软重载":config.yaml 改动后无需重启进程，调用此方法刷新内存中固化的目录副本，
// 随后 ListSkills / ListConfiguredCommands / ListSubAgents / 新建会话注入 additionalDirectories 都会用新目录。
// 同时清空 probe/commands/modes 缓存，下次请求自动重扫。
// 注意：已存在的会话其 systemPrompt / additionalDirectories 已固化注入，不受影响（仅对新建会话生效）。
func (s *Service) SetScanDirs(skills config.SkillsConfig, commands config.CommandsConfig, rules config.RulesConfig, subAgents config.SubAgentsConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skillUserDirs = append([]string(nil), skills.UserDirs...)
	s.skillProjectDirs = append([]string(nil), skills.ProjectDirs...)
	s.commandUserDirs = append([]string(nil), commands.UserDirs...)
	s.commandProjectDirs = append([]string(nil), commands.ProjectDirs...)
	s.ruleUserDirs = append([]string(nil), rules.UserDirs...)
	s.ruleProjectDirs = append([]string(nil), rules.ProjectDirs...)
	s.subAgentUserDirs = append([]string(nil), subAgents.UserDirs...)
	s.subAgentProjectDirs = append([]string(nil), subAgents.ProjectDirs...)
	// 清缓存：让下次 probe / list commands / list modes 重新扫描
	s.probeCache = make(map[string][]acp.SessionConfigOption)
	s.agentCommands = make(map[string][]acp.AvailableCommand)
	s.agentModes = make(map[string][]acp.SessionMode)
}

// configuredMCPServers 读取全局共享 MCP 配置文件并转换为 ACP server 列表。
// 文件不存在或解析失败时返回 nil 并记日志，不影响会话创建。
//
// 若主程序已注入网关 endpoint（SetGatewayEndpoint），则默认把网关注入给所有会话，
// 并收敛被网关代理的 http/sse 上游——无需用户在 mcp.json 里手动启用网关条目。
// 网关不接管的 stdio server 仍走 session/new 原路注入。
//
// caps 为 agent 握手声明的 MCP 传输能力：agent 不支持 http 时（如 devin），
// 网关降级为 stdio 桥形态（`opennexus mcp-bridge` 子进程）注入，工具集不变。
func (s *Service) configuredMCPServers(caps acp.McpCapabilities) []acp.McpServer {
	if s.mcpConfigPath == "" {
		return nil
	}
	entries, err := LoadMCPServerEntries(s.mcpConfigPath)
	if err != nil {
		slog.Warn("加载全局 MCP 配置失败，跳过注入", "path", s.mcpConfigPath, "err", err)
		return nil
	}
	// 主程序默认启用网关：endpoint + token 就绪时直接注入，不依赖 mcp.json 条目。
	if s.gatewayEndpoint != "" && s.gatewayToken != "" {
		gwEntry := gatewayHTTPEntry(s.gatewayEndpoint, s.gatewayToken)
		if !caps.Http {
			// 不支持 http 传输的 agent：网关降级为 stdio 桥注入。
			// 桥不可用（取不到主程序路径）时保留 http 条目，交由末端能力过滤兜底。
			if bridge, ok := s.gatewayBridgeEntry(); ok {
				gwEntry = bridge
			}
		}
		return ConvertMCPServers(collapseWithGatewayEntry(entries, gwEntry))
	}
	// 兼容旧路径：mcp.json 里已手动写入网关条目时仍按原逻辑收敛。
	return ConvertMCPServers(CollapseViaGateway(entries))
}

// gatewayBridgeEntry 构造 stdio 形态的网关条目：通过 `opennexus mcp-bridge` 子进程
// 把网关的 Streamable HTTP endpoint 桥接为 stdio MCP server，
// 供握手声明 http:false 的 agent（如 devin）接入。
// endpoint/token 经 env 传递，避免 token 暴露在进程参数里。
func (s *Service) gatewayBridgeEntry() (NamedMCPServerEntry, bool) {
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("获取主程序可执行文件路径失败，无法注入 stdio 网关桥", "err", err)
		return NamedMCPServerEntry{}, false
	}
	return NamedMCPServerEntry{
		Name: GatewayMCPName,
		Entry: MCPServerEntry{
			Type:    MCPTypeStdio,
			Command: exe,
			Args:    []string{"mcp-bridge"},
			Env: map[string]string{
				"OPENNEXUS_GATEWAY_URL":   s.gatewayEndpoint,
				"OPENNEXUS_GATEWAY_TOKEN": s.gatewayToken,
			},
		},
	}, true
}

// sessionMCPServers 汇总注入给指定会话的全部 MCP server：全局共享 + 笔记 MCP。
//
// 去重：若全局 mcp.json 已含 opennexus-notes 条目（生成 token 时自动写入），
// 则不再追加按用户 token 动态注入的笔记 MCP，避免同名 server 重复注入。
// 聚合网关启用时笔记 MCP 已被网关代理并从列表中收敛，同样不再单独注入。
//
// 最终列表按 caps 做能力过滤：agent 不支持的传输类型（http/sse）丢弃并告警。
func (s *Service) sessionMCPServers(userID uint, caps acp.McpCapabilities) []acp.McpServer {
	configured := s.configuredMCPServers(caps)
	servers := configured
	if !hasServerNamed(configured, notesMCPName) && !hasServerNamed(configured, GatewayMCPName) {
		servers = append(configured, s.notesMCPServers(userID)...)
	}
	return filterByMcpCapabilities(servers, caps)
}

// notesMCPName 是笔记 MCP server 的固定名称（与 mcp.json 中写入的条目名一致）。
const notesMCPName = "opennexus-notes"

// hasServerNamed 判断 server 列表中是否存在指定名称的条目（任意传输类型）。
func hasServerNamed(servers []acp.McpServer, name string) bool {
	for _, s := range servers {
		switch {
		case s.Stdio != nil && s.Stdio.Name == name:
			return true
		case s.Http != nil && s.Http.Name == name:
			return true
		case s.Sse != nil && s.Sse.Name == name:
			return true
		case s.Acp != nil && s.Acp.Name == name:
			return true
		}
	}
	return false
}

// SetDebugConfig 注入 ACP 调试配置；enabled=false 时清空 debugger。
func (s *Service) SetDebugConfig(cfg config.DebugConfig) {
	if !cfg.ACP.Enabled {
		s.dbg = nil
		return
	}
	s.dbg = NewACPDebugger(DebugConfig{Enabled: true, Dir: cfg.ACP.Dir})
}

// Debugger 返回 ACP 调试器（可能为 nil）。
func (s *Service) Debugger() *ACPDebugger {
	return s.dbg
}

// SetACPConnectionRepo 注入 acp_connections 心跳表仓库。
// 主 server 启动时注入（与 watchdog 共享同一 SQLite）；测试环境可不注入，相关写表静默跳过。
func (s *Service) SetACPConnectionRepo(repo *repository.ACPConnectionRepository) {
	s.acpConnRepo = repo
}

// SetPromptMaxDuration 注入单轮 prompt 最大存活时间。d<=0 时恢复默认 30min。
// prompt 的生命周期独立于 HTTP/SSE 请求，此超时兜底防 agent 卡死导致 goroutine 永久泄漏。
func (s *Service) SetPromptMaxDuration(d time.Duration) {
	if d > 0 {
		s.promptMaxDuration = d
	} else {
		// 0/负值=恢复默认（让 effectivePromptMaxDuration 返回 defaultPromptMaxDuration）
		s.promptMaxDuration = 0
	}
}

// SetFailedTaskAutoRetryOnce 注入失败任务自动重试开关（崩溃/断连时 ResumeSession 并重发，仅一次）。
func (s *Service) SetFailedTaskAutoRetryOnce(enabled bool) {
	s.failedTaskAutoRetryOnce = enabled
}

// effectivePromptMaxDuration 返回生效的 prompt 最大存活时间（未设置时取默认）。
func (s *Service) effectivePromptMaxDuration() time.Duration {
	if s.promptMaxDuration > 0 {
		return s.promptMaxDuration
	}
	return defaultPromptMaxDuration
}

// applyRulesToConnection 把当前生效的权限规则与 YOLO 查询下发到指定连接的 broker。
func (s *Service) applyRulesToConnection(conn *Connection) {
	if conn == nil {
		return
	}
	conn.Client().perm.setRules(s.activePermRules.Load())
	conn.Client().SetYoloCheck(s.isACPSessionYolo)
}

// isACPSessionYolo 供 permissionBroker 查询会话 YOLO（按 ACP SessionId）。
func (s *Service) isACPSessionYolo(id acp.SessionId) bool {
	v, ok := s.sessionYolo.Load(string(id))
	return ok && v.(bool)
}

// bindACPSessionYolo 绑定/清除 ACP 会话的 YOLO 运行态。
func (s *Service) bindACPSessionYolo(acpSID string, yolo bool) {
	if acpSID == "" {
		return
	}
	if yolo {
		s.sessionYolo.Store(acpSID, true)
		return
	}
	s.sessionYolo.Delete(acpSID)
}

// rebindACPSessionYolo 在 ACP SessionId 轮换时迁移 YOLO 运行态。
func (s *Service) rebindACPSessionYolo(oldACP, newACP string, yolo bool) {
	if oldACP != "" && oldACP != newACP {
		s.sessionYolo.Delete(oldACP)
	}
	s.bindACPSessionYolo(newACP, yolo)
}

// applyRulesToAllConnections 把当前生效的权限规则下发到所有活跃连接的 broker。
func (s *Service) applyRulesToAllConnections() {
	rules := s.activePermRules.Load()
	s.mu.RLock()
	conns := make([]*Connection, 0, len(s.pool))
	for _, conn := range s.pool {
		conns = append(conns, conn)
	}
	s.mu.RUnlock()
	for _, conn := range conns {
		conn.Client().perm.setRules(rules)
		conn.Client().SetYoloCheck(s.isACPSessionYolo)
	}
}

// ApplyPermissions 用给定的权限规则更新生效规则并下发到所有连接的 broker。
// 规则来自 config.yaml 的 permissions 段：启动时下发一次（须在预连接前，使新连接建连即拿到规则），
// 设置页保存后再次调用热更新。mode 为空/非法时兜底 normal。
func (s *Service) ApplyPermissions(mode string, allow, ask, deny []string) {
	if mode != config.PermissionModeNormal && mode != config.PermissionModeYolo {
		mode = config.PermissionModeNormal
	}
	s.activePermRules.Store(&PermissionRules{
		Mode:  mode,
		Allow: cleanRules(allow),
		Ask:   cleanRules(ask),
		Deny:  cleanRules(deny),
	})
	s.applyRulesToAllConnections()
}

// CurrentPermissions 返回当前生效的权限规则（供设置页读取）。无规则时返回 normal + 空列表。
func (s *Service) CurrentPermissions() (mode string, allow, ask, deny []string) {
	r := s.activePermRules.Load()
	if r == nil {
		return config.PermissionModeNormal, nil, nil, nil
	}
	return r.Mode, r.Allow, r.Ask, r.Deny
}

// cleanRules 去空白、去空项；无有效项返回 nil。
func cleanRules(list []string) []string {
	out := make([]string, 0, len(list))
	for _, r := range list {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// recordConnectionUpsert 写入/更新心跳表的一行（建连与续约时调用）。
// repo 未注入时静默跳过。
func (s *Service) recordConnectionUpsert(poolKey, agentType, cwd string, pid int) {
	if s.acpConnRepo == nil {
		return
	}
	if err := s.acpConnRepo.Upsert(poolKey, agentType, cwd, pid); err != nil {
		slog.Warn("写 acp_connections 心跳失败", "poolKey", poolKey, "err", err)
	}
}

// recordConnectionActivity 刷新指定连接的最后活动时间（发 prompt 时调用）。
func (s *Service) recordConnectionActivity(poolKey string) {
	if s.acpConnRepo == nil {
		return
	}
	if err := s.acpConnRepo.TouchActivity(poolKey); err != nil {
		slog.Warn("刷新 acp_connections 活动时间失败", "poolKey", poolKey, "err", err)
	}
}

// recordConnectionDelete 进程退出/连接关闭后从心跳表删除一行。
func (s *Service) recordConnectionDelete(poolKey string) {
	if s.acpConnRepo == nil {
		return
	}
	if err := s.acpConnRepo.Delete(poolKey); err != nil {
		slog.Warn("删 acp_connections 心跳失败", "poolKey", poolKey, "err", err)
	}
}

// heartbeatAllConnections 全表续约 server 心跳，供独立 watchdog 判定主程序存活。
// repo 未注入时静默跳过。
func (s *Service) heartbeatAllConnections() {
	if s.acpConnRepo == nil {
		return
	}
	if err := s.acpConnRepo.TouchHeartbeat(); err != nil {
		slog.Warn("续约 acp_connections server 心跳失败", "err", err)
	}
}

func (s *Service) debugLog(dbSessionID uint, event, acpSessionID string, detail any) {
	if s.dbg == nil {
		return
	}
	s.dbg.LogEvent(fmt.Sprintf("%d", dbSessionID), event, acpSessionID, detail)
}

func (s *Service) debugRegister(acpSessionID string, dbSessionID uint) {
	if s.dbg == nil {
		return
	}
	s.dbg.RegisterSession(acpSessionID, fmt.Sprintf("%d", dbSessionID))
}

func (s *Service) debugUnregister(acpSessionID string) {
	if s.dbg == nil {
		return
	}
	s.dbg.Unregister(acpSessionID)
}

func (s *Service) debugCleanup(dbSessionID uint) {
	if s.dbg == nil || dbSessionID == 0 {
		return
	}
	s.dbg.CleanupSession(fmt.Sprintf("%d", dbSessionID))
}

func (s *Service) debugBindPending(agentType string, dbSessionID uint) {
	if s.dbg == nil {
		return
	}
	s.dbg.BindPending(agentType, fmt.Sprintf("%d", dbSessionID))
}

func (s *Service) debugClearPending(agentType string) {
	if s.dbg == nil {
		return
	}
	s.dbg.ClearPending(agentType)
}

// agentSessionID 返回调 ACP 用的 sessionId；优先 AgentSessionID，兼容旧数据。
func agentSessionID(session *models.Session) string {
	if session.AgentSessionID != "" {
		return session.AgentSessionID
	}
	return session.SessionID
}

func (s *Service) notesMCPServers(userID uint) []acp.McpServer {
	if s.noteSettings == nil || userID == 0 || s.publicBaseURL == "" {
		return nil
	}
	st, err := s.noteSettings.FindByUserID(userID)
	if err != nil || strings.TrimSpace(st.McpToken) == "" {
		return nil
	}
	return []acp.McpServer{{
		Http: &acp.McpServerHttpInline{
			Name: "opennexus-notes",
			Type: "http",
			Url:  s.publicBaseURL + "/mcp/notes",
			Headers: []acp.HttpHeader{{
				Name:  "Authorization",
				Value: "Bearer " + st.McpToken,
			}},
		},
	}}
}

// connectionKey 生成 agent 连接池键（agentType + 绝对 cwd）。
func connectionKey(agentType, cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil || abs == "" {
		abs = cwd
	}
	return agentType + "\x00" + abs
}

func splitConnectionKey(key string) (agentType, cwd string) {
	if i := strings.IndexByte(key, '\x00'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

func (s *Service) agentTypeForSession(sessionID string) (string, bool) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return "", false
	}
	return session.AgentType, true
}

// RegisterBackend 注册一个 agent 后端。
func (s *Service) RegisterBackend(b Backend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backends[b.Name()] = b
}

// ReplaceBackend 注册或覆盖一个 agent 后端（用于动态更新配置）。
// 若该类型已有连接，先关闭旧连接（停止旧进程），下次使用时按新配置重建。
func (s *Service) ReplaceBackend(b Backend) {
	s.mu.Lock()
	s.closeConnectionsForAgentLocked(b.Name())
	s.backends[b.Name()] = b
	delete(s.probeCache, b.Name())
	delete(s.agentCommands, b.Name())
	delete(s.agentModes, b.Name())
	s.mu.Unlock()
}

// UnregisterBackend 注销一个 agent 后端，并关闭对应的共享连接。
func (s *Service) UnregisterBackend(name string) {
	s.mu.Lock()
	s.closeConnectionsForAgentLocked(name)
	delete(s.backends, name)
	delete(s.probeCache, name)
	delete(s.agentCommands, name)
	delete(s.agentModes, name)
	s.mu.Unlock()
}

func (s *Service) closeConnectionsForAgentLocked(agentType string) {
	var toClose []*Connection
	for key, conn := range s.pool {
		at, _ := splitConnectionKey(key)
		if at != agentType {
			continue
		}
		delete(s.pool, key)
		delete(s.states, key)
		toClose = append(toClose, conn)
	}
	for _, conn := range toClose {
		_ = conn.Close()
	}
}

// GetBackend 查找已注册的后端。
func (s *Service) GetBackend(name string) (Backend, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.backends[name]
	if !ok {
		return nil, ErrBackendNotFound
	}
	return b, nil
}

// ensureConnection 获取或创建指定 agentType + cwd 的共享连接。
// 若池中连接已断开（Done() 已关闭），自动清理并重建。
// 状态流转：disconnected → connecting → connected（或 connecting → disconnected 失败）。
func (s *Service) ensureConnection(ctx context.Context, agentType, cwd string) (*Connection, error) {
	if s.shuttingDown.Load() {
		return nil, context.Canceled
	}
	key := connectionKey(agentType, cwd)
	// 快速路径：池中有存活连接
	s.mu.RLock()
	conn, ok := s.pool[key]
	if ok {
		select {
		case <-conn.Done():
			conn = nil
		default:
			s.mu.RUnlock()
			return conn, nil
		}
	}
	s.mu.RUnlock()

	// 慢路径：需要建立连接。先抢锁设置 connecting 状态，避免并发重复建连。
	s.mu.Lock()
	// 再次检查：可能其他 goroutine 已建好
	if existing, ok2 := s.pool[key]; ok2 {
		select {
		case <-existing.Done():
		default:
			s.mu.Unlock()
			return existing, nil
		}
	}
	// 已有 connecting 状态的 goroutine 在跑：等待完成后重试，避免重复建连或返回错误。
	if s.states[key] == connStateConnecting {
		done := s.connectDone[key]
		s.mu.Unlock()
		if done == nil {
			return nil, fmt.Errorf("agent %s 正在连接中，请稍后重试", agentType)
		}
		select {
		case <-done:
			return s.ensureConnection(ctx, agentType, cwd)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	doneCh := make(chan struct{})
	s.connectDone[key] = doneCh
	s.states[key] = connStateConnecting
	// 清理已断开的旧连接
	if oldConn, ok2 := s.pool[key]; ok2 {
		delete(s.pool, key)
		s.markSessionsErrorForPoolKeyLocked(key, oldConn)
		_ = oldConn.Close()
	}
	s.mu.Unlock()

	// 在锁外执行耗时的进程启动与握手
	conn, err := s.buildConnection(ctx, agentType, cwd)
	s.mu.Lock()
	close(doneCh)
	delete(s.connectDone, key)
	if err != nil {
		s.states[key] = connStateDisconnected
		s.mu.Unlock()
		return nil, err
	}
	if s.shuttingDown.Load() {
		s.states[key] = connStateDisconnected
		s.mu.Unlock()
		_ = conn.Close()
		return nil, context.Canceled
	}
	// 并发场景：可能已有其他 goroutine 先建好，复用之并关闭多余的
	if existing, ok2 := s.pool[key]; ok2 {
		select {
		case <-existing.Done():
		default:
			s.mu.Unlock()
			_ = conn.Close()
			return existing, nil
		}
	}
	s.pool[key] = conn
	s.states[key] = connStateConnected
	s.mu.Unlock()

	// 记录心跳表：让独立 watchdog 知道此进程的存在与 PGID，供空闲回收与主程序死亡时清理
	s.recordConnectionUpsert(key, agentType, cwd, conn.Pid())
	// 下发当前生效的全局权限规则到新连接的 broker
	s.applyRulesToConnection(conn)

	go s.watchConnection(key, conn)
	return conn, nil
}

// buildConnection 执行实际的进程启动与 ACP 握手（无锁，可阻塞）。
func (s *Service) buildConnection(ctx context.Context, agentType, cwd string) (*Connection, error) {
	backend, err := s.GetBackend(agentType)
	if err != nil {
		return nil, err
	}
	// 若后端需要预处理（如 BinaryBackend 下载二进制），在启动进程前执行。
	// 失败时透传错误，让用户看到真正的失败原因（如下载失败）而非误导性的 PATH 错误。
	if p, ok := backend.(Preparable); ok {
		if err := p.Prepare(); err != nil {
			slog.Error("建立 agent 连接失败：准备后端失败",
				"agent", agentType, "err", err)
			return nil, fmt.Errorf("准备 agent 后端: %w", err)
		}
	}
	slog.Info("开始建立 agent 连接",
		"agent", agentType,
		"cwd", cwd,
		"command", backend.Command(),
		"args", backend.Args(),
	)
	if cwd != "" {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			slog.Error("建立 agent 连接失败：创建工作目录失败",
				"agent", agentType, "cwd", cwd, "err", err)
			return nil, fmt.Errorf("创建工作目录 %s: %w", cwd, err)
		}
	}
	newConn, err := NewConnection(backend, cwd, s.dbg, s.terminalEnabled)
	if err != nil {
		slog.Error("建立 agent 连接失败：启动 agent 进程失败",
			"agent", agentType,
			"cwd", cwd,
			"command", backend.Command(),
			"args", backend.Args(),
			"err", err)
		return nil, fmt.Errorf("建立共享连接: %w", err)
	}
	// 注入 terminal 桥接器：握手声明能力后 agent 的 terminal/* 请求由 bridge 代执行
	newConn.Client().SetTerminalBridge(s.terminalBridge)
	initResp, err := newConn.Initialize(ctx)
	if err != nil {
		// 握手失败时先诊断进程状态（必须在 Close 之前），给出可操作的失败原因。
		diag := newConn.InspectFailure()
		_ = newConn.Close()
		slog.Error("建立 agent 连接失败：ACP 握手失败",
			"agent", agentType,
			"command", backend.Command(),
			"args", backend.Args(),
			"diagnosis", diag,
			"err", err)
		return nil, fmt.Errorf("ACP 握手失败: %w（诊断: %s）", err, diag)
	}
	if err := newConn.AuthenticateIfRequired(ctx, initResp); err != nil {
		diag := newConn.InspectFailure()
		_ = newConn.Close()
		slog.Error("建立 agent 连接失败：ACP 认证失败",
			"agent", agentType, "diagnosis", diag, "err", err)
		return nil, fmt.Errorf("ACP 认证失败: %w（诊断: %s）", err, diag)
	}
	slog.Debug("建立 agent 连接成功",
		"agent", agentType, "cwd", cwd, "protocol", initResp.ProtocolVersion)
	// 缓存握手响应（按 agentType，同类型多连接以最近一次为准），供设置页展示 ACP 能力
	s.mu.Lock()
	s.agentInitInfo[agentType] = initResp
	s.mu.Unlock()
	return newConn, nil
}

// watchConnection 监控共享连接，进程退出时标记 disconnected。
// 不立即将会话标记为 error——由健康检查任务自动重连，重连成功后会话可继续使用。
// 仅当重连持续失败超过阈值时，才将会话标记为 error。
func (s *Service) watchConnection(poolKey string, conn *Connection) {
	<-conn.Done()
	agentType, _ := splitConnectionKey(poolKey)
	s.logWarn("agent 进程退出，标记为 disconnected", agentType)

	s.mu.Lock()
	// 仅当池中仍是该连接时才清理（避免清理已被重建替换的新连接）
	if cur, ok := s.pool[poolKey]; ok && cur == conn {
		delete(s.pool, poolKey)
		s.states[poolKey] = connStateDisconnected
		// 连接断开后清空探测缓存，重连时重新探测
		delete(s.probeCache, agentType)
	}
	s.mu.Unlock()
	// 进程已退出，从心跳表删除该行（PGID 失效）；重连成功后 ensureConnection 会写入新 PID
	s.recordConnectionDelete(poolKey)
	// 健康检查 goroutine 会自动尝试重连
}

// markSessionsErrorForPoolKeyLocked 将指定连接池键下所有活跃会话标记为 error，
// 并清理对应的 sessionPoolKey 路由。oldConn 非空时同步清掉旧连接上各会话的挂起权限，
// 避免残留死权限（接收方 agent 进程已退出，respond 永远无意义）。调用方需持有 s.mu 写锁。
func (s *Service) markSessionsErrorForPoolKeyLocked(poolKey string, oldConn *Connection) {
	for sid, key := range s.sessionPoolKey {
		if key != poolKey {
			continue
		}
		delete(s.sessionPoolKey, sid)
		delete(s.commands, sid)
		delete(s.configs, sid)
		delete(s.modes, sid)
		if sess, err := s.sessions.FindBySessionID(sid); err == nil {
			// 清掉旧 agent 会话的挂起权限（按 acpSID 索引），避免死权限残留
			if oldConn != nil && sess.AgentSessionID != "" {
				oldConn.Client().CancelPermissions(acp.SessionId(sess.AgentSessionID))
			}
			_ = s.sessions.UpdateStatus(sess.ID, models.SessionStatusError, nil)
		}
	}
}

// PreconnectAsync 异步为指定 agent + cwd 预建立共享连接，供新建会话页提前预热。
func (s *Service) PreconnectAsync(agentType, cwd string) {
	if cwd == "" {
		cwd = s.probeCwd()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := s.ensureConnection(ctx, agentType, cwd); err != nil {
			slog.Debug("工作区预连接失败", "agent", agentType, "cwd", cwd, "err", err)
		}
	}()
}

// PreconnectAllAsync 异步为所有已注册后端预建立共享连接。
// 每个 agent 独立 goroutine 连接，互不阻塞，立即返回。
// 连接失败的后端由健康检查任务自动重连。
func (s *Service) PreconnectAllAsync() {
	s.mu.RLock()
	types := make([]string, 0, len(s.backends))
	for name := range s.backends {
		types = append(types, name)
	}
	s.mu.RUnlock()

	for _, agentType := range types {
		go func(at string) {
			cwd := s.probeCwd()
			slog.Info("开始预连接 agent", "agent", at, "cwd", cwd)
			if _, err := s.ensureConnection(s.hcCtx, at, cwd); err != nil {
				slog.Error("预连接 agent 失败",
					"agent", at,
					"cwd", cwd,
					"command", s.backendCommandSafe(at),
					"err", err)
				return
			}
			slog.Info("预连接 agent 成功", "agent", at, "cwd", cwd)
			s.prefetchProbeConfig(s.hcCtx, at)
			// 探测用的 cwd (probeCwd) 是 session 根目录，与实际会话的 cwd（每次随机的
			// nexus-XXX 子目录）永远不同，连接池无法复用这条连接。探测结果已缓存到
			// probeCache[agentType]，连接本身留着只会白占一个进程。探测完立即释放。
			s.releaseConnection(at, cwd)
		}(agentType)
	}
}

// releaseConnection 从连接池移除并关闭指定 agentType+cwd 的连接（杀进程组）。
// 用于预连接探测后释放不会被实际会话复用的废连接。
// watchConnection goroutine 会在 conn.Done() 后自动清理心跳表行。
func (s *Service) releaseConnection(agentType, cwd string) {
	key := connectionKey(agentType, cwd)
	s.mu.Lock()
	conn, ok := s.pool[key]
	if ok {
		delete(s.pool, key)
		s.states[key] = connStateDisconnected
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	slog.Info("释放预连接（探测完成，cwd 不会被实际会话复用）", "agent", agentType, "cwd", cwd, "pid", conn.Pid())
	_ = conn.Close()
}

// heartbeatInterval 是主 server 向 acp_connections 心跳表全表续约的周期。
// watchdog 据此判断主程序是否存活：若 heartbeat 持续超过 watchdogHBStale 未更新，视为主程序已死。
const heartbeatInterval = 30 * time.Second

// StartHealthCheck 启动后台健康检查与自动重连 goroutine。
// 定期检查所有已注册 backend 的连接状态，断开的自动重连（带指数退避）。
// 必须在所有 backend 注册完成后调用。
// 幂等：多次调用仅启动一次健康检查循环，避免覆盖 hcCancel 造成旧 goroutine 永久泄漏。
// 同时启动心跳续约 goroutine，向 acp_connections 表全表续约，供独立 watchdog 判定主程序存活。
func (s *Service) StartHealthCheck() {
	s.hcStartOnce.Do(func() {
		s.hcCtx, s.hcCancel = context.WithCancel(context.Background())
		s.hcWG.Add(1)
		go s.healthCheckLoop()
		// 独立的心跳续约 goroutine：供 watchdog 判活；与 healthCheckLoop 共享 hcCtx/hcWG 生命周期
		s.hcWG.Add(1)
		go s.heartbeatLoop()
	})
}

// heartbeatLoop 周期性全表续约 server 心跳。
// 主程序异常退出（崩溃/被 SIGKILL）后，此 goroutine 随之消亡，
// 心跳不再更新；watchdog 据此判定主程序死亡并清理全部 acp 进程。
func (s *Service) heartbeatLoop() {
	defer s.hcWG.Done()
	// 先立即续约一次，确保 watchdog 启动时能看到主程序存活
	s.heartbeatAllConnections()
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.hcCtx.Done():
			return
		case <-ticker.C:
			s.heartbeatAllConnections()
		}
	}
}

// StopHealthCheck 停止健康检查 goroutine 并关闭所有共享连接。
// 幂等：多次调用安全（sync.Once 保护）。同时关闭残留的 activePrompts 广播器，
// 防止卡死的 prompt goroutine 永久占用 256 缓冲与订阅者 channel。
func (s *Service) StopHealthCheck() {
	s.hcStopOnce.Do(func() {
		s.shuttingDown.Store(true)
		if s.hcCancel != nil {
			s.hcCancel()
		}

		// 关闭所有残留的 activePrompts 广播器（prompt goroutine 卡死时会永久残留）
		s.mu.Lock()
		broadcasters := make([]*msgBroadcaster, 0, len(s.activePrompts))
		for sid, bc := range s.activePrompts {
			broadcasters = append(broadcasters, bc)
			delete(s.activePrompts, sid)
		}
		s.mu.Unlock()
		for _, bc := range broadcasters {
			bc.close()
		}

		// 立即终止 agent 子进程，不等待健康检查循环结束
		s.mu.Lock()
		conns := make([]*Connection, 0, len(s.pool))
		for _, conn := range s.pool {
			conns = append(conns, conn)
		}
		s.pool = make(map[string]*Connection)
		s.mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}

		done := make(chan struct{})
		go func() {
			s.hcWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			slog.Warn("健康检查 goroutine 退出超时，继续关闭")
		}
	})
}

// healthCheckLoop 定期检查连接状态并自动重连断开的 agent。
func (s *Service) healthCheckLoop() {
	defer s.hcWG.Done()

	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	// 记录每个 agent 的重连退避延迟
	delays := make(map[string]time.Duration)

	for {
		select {
		case <-s.hcCtx.Done():
			return
		case <-ticker.C:
			s.checkAndReconnect(delays)
		}
	}
}

// checkAndReconnect 检查所有连接池键的状态，对断开的尝试重连。
func (s *Service) checkAndReconnect(delays map[string]time.Duration) {
	s.mu.RLock()
	keys := make([]string, 0, len(s.states))
	for key := range s.states {
		keys = append(keys, key)
	}
	s.mu.RUnlock()

	for _, key := range keys {
		if s.hcCtx.Err() != nil {
			return
		}
		s.checkConnectionKey(key, delays)
	}
}

// checkConnectionKey 检查单个连接池键的状态，必要时重连。
func (s *Service) checkConnectionKey(poolKey string, delays map[string]time.Duration) {
	agentType, cwd := splitConnectionKey(poolKey)
	s.mu.RLock()
	state := s.states[poolKey]
	conn, hasConn := s.pool[poolKey]
	s.mu.RUnlock()

	// 已连接且存活 → 重置退避
	if hasConn && state == connStateConnected {
		select {
		case <-conn.Done():
			// 连接已断开但状态未更新（watchConnection 可能还没跑到）
			s.mu.Lock()
			if cur, ok := s.pool[poolKey]; ok && cur == conn {
				delete(s.pool, poolKey)
				s.states[poolKey] = connStateDisconnected
			}
			s.mu.Unlock()
		default:
			delays[poolKey] = 0
			return
		}
	}

	// connecting 状态 → 跳过，等它完成
	if state == connStateConnecting {
		return
	}

	// disconnected → 尝试重连（带退避）
	delay, ok := delays[poolKey]
	if !ok || delay == 0 {
		delay = reconnectBaseDelay
	}

	slog.Info("尝试重连 agent",
		"agent", agentType, "cwd", cwd,
		"poolKey", poolKey, "prevState", state, "delay", delay)
	select {
	case <-s.hcCtx.Done():
		return
	case <-time.After(delay):
	}

	if _, err := s.ensureConnection(s.hcCtx, agentType, cwd); err != nil {
		// 指数退避，上限 reconnectMaxDelay
		next := delay * 2
		if next > reconnectMaxDelay {
			next = reconnectMaxDelay
		}
		delays[poolKey] = next
		slog.Error("重连 agent 失败",
			"agent", agentType,
			"cwd", cwd,
			"poolKey", poolKey,
			"prevState", state,
			"delay", delay,
			"nextDelay", next,
			"command", s.backendCommandSafe(agentType),
			"err", err)
		return
	}

	slog.Info("重连 agent 成功", "agent", agentType, "cwd", cwd, "delay", delay)
	delays[poolKey] = 0
	s.prefetchProbeConfig(s.hcCtx, agentType)
}

// CreateSession 创建新的 ACP 会话。
// workspaceID 非 0 时使用指定 workspace 的 cwd，否则自动创建默认 workspace。
// modelValue 非空时在会话创建后立即设置该模型。
func (s *Service) CreateSession(ctx context.Context, agentType string, workspaceID uint, userID uint, modelValue string) (*models.Session, error) {
	return s.CreateSessionWithSource(ctx, agentType, workspaceID, userID, models.SessionSourceManual, modelValue)
}

// CreateSessionWithSource 创建会话并指定来源（manual/scheduled）。
// workspaceID 非 0 时使用指定 workspace，否则查找或创建默认 temporary workspace。
// modelValue 非空时将在首次 Prompt 时应用。
// 为提升页面跳转速度，不再同步创建 ACP 会话，而是返回 pending 状态；
// ACP 连接与会话创建延迟到首次 Prompt（PromptWithExecution）时完成。
func (s *Service) CreateSessionWithSource(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue string) (*models.Session, error) {
	return s.createSessionFull(ctx, agentType, workspaceID, userID, source, modelValue, nil, "")
}

// CreateSessionWithCwd 创建会话并将 cwd 固定为用户指定的目录（如已存在的 git worktree）。
// cwd 非空时覆盖工作区 cwd；空字符串时等价于 CreateSessionWithSource（跟随工作区）。
func (s *Service) CreateSessionWithCwd(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue, cwd string) (*models.Session, error) {
	return s.createSessionFull(ctx, agentType, workspaceID, userID, source, modelValue, nil, cwd)
}

func (s *Service) createSessionFull(ctx context.Context, agentType string, workspaceID uint, userID uint, source, modelValue string, parentSessionID *uint, cwdOverride string) (*models.Session, error) {
	if _, err := s.GetBackend(agentType); err != nil {
		return nil, err
	}

	var ws *Workspace
	var dbWS *models.Workspace
	createdDefaultWS := false
	if workspaceID > 0 {
		var err error
		dbWS, err = s.workspaces.FindByID(workspaceID)
		if err != nil {
			return nil, fmt.Errorf("工作区不存在: %w", err)
		}
		if dbWS.UserID != userID {
			return nil, errors.New("无权访问该工作区")
		}
		ws = &Workspace{Mode: dbWS.Mode, Cwd: dbWS.Cwd, TempDir: dbWS.TempDir, Directories: dbWS.Directories}
		if err := EnsureWorkspaceDir(dbWS.Mode, dbWS.Cwd); err != nil {
			return nil, err
		}
	} else {
		var err error
		dbWS, err = s.workspaces.FindDefaultByUserID(userID)
		if err != nil {
			tempWs, tErr := NewTemporaryWorkspace(s.wsConfig.SessionDir, s.wsConfig.TempDirPrefix)
			if tErr != nil {
				return nil, tErr
			}
			newWS := &models.Workspace{
				UserID:  userID,
				Name:    "默认工作区",
				Cwd:     tempWs.Cwd,
				Mode:    models.WorkspaceModeTemporary,
				TempDir: tempWs.TempDir,
			}
			if cErr := s.workspaces.Create(newWS); cErr != nil {
				_ = tempWs.Cleanup()
				return nil, fmt.Errorf("创建默认工作区: %w", cErr)
			}
			dbWS = newWS
			ws = tempWs
			createdDefaultWS = true
		} else {
			ws = &Workspace{Mode: dbWS.Mode, Cwd: dbWS.Cwd, TempDir: dbWS.TempDir, Directories: dbWS.Directories}
			if err := EnsureWorkspaceDir(dbWS.Mode, dbWS.Cwd); err != nil {
				return nil, err
			}
		}
		workspaceID = dbWS.ID
	}

	rollbackNewDefaultWS := func() {
		if !createdDefaultWS || dbWS == nil {
			return
		}
		_ = s.workspaces.Delete(dbWS.ID)
		_ = ws.Cleanup()
	}

	// 生成稳定 SessionID，不创建 ACP 会话，快速返回 pending 状态
	tempSessionID := uuid.New().String()
	wid := dbWS.ID
	// cwdOverride 非空时覆盖工作区 cwd（用于编排任务在其 git worktree 内运行）。
	// 普通会话 cwdOverride 为空，保持 session.Cwd = ws.Cwd 的既有行为。
	finalCwd := ws.Cwd
	if cwdOverride != "" {
		finalCwd = cwdOverride
	}
	session := &models.Session{
		SessionID:       tempSessionID,
		AgentType:       agentType,
		Cwd:             finalCwd,
		Status:          models.SessionStatusPending,
		UserID:          userID,
		WorkspaceID:     &wid,
		Source:          source,
		ModelValue:      modelValue,
		ParentSessionID: parentSessionID,
	}
	if err := s.sessions.Create(session); err != nil {
		rollbackNewDefaultWS()
		return nil, fmt.Errorf("会话落库: %w", err)
	}
	session.Workspace = *dbWS

	slog.Info("会话已创建（pending）", "agent", agentType, "session", tempSessionID, "cwd", finalCwd)
	return session, nil
}

// connForSession 通过 sessionPoolKey 路由查找 session 所属的共享连接。
func (s *Service) connForSession(sessionID string) (*Connection, bool) {
	s.mu.RLock()
	poolKey, ok := s.sessionPoolKey[sessionID]
	if !ok {
		s.mu.RUnlock()
		return nil, false
	}
	conn, ok := s.pool[poolKey]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	select {
	case <-conn.Done():
		return nil, false
	default:
		return conn, true
	}
}

// connForSessionOrResume 查找会话所属连接；内存路由丢失（如服务重启后 DB 状态仍为 active，
// 但连接池已清空）时自动走 ResumeSession 重建连接再返回。恢复失败返回 ErrSessionNotActive。
// 供切换模式 / 切换模型等配置类操作复用，使其与发送 prompt 一样具备自动重连能力。
func (s *Service) connForSessionOrResume(ctx context.Context, sessionID string) (*Connection, error) {
	if conn, ok := s.connForSession(sessionID); ok {
		return conn, nil
	}
	slog.Warn("会话连接丢失，尝试自动恢复", "session", sessionID)
	if _, err := s.ResumeSession(ctx, sessionID); err != nil {
		slog.Error("自动恢复会话失败", "session", sessionID, "err", err)
		return nil, ErrSessionNotActive
	}
	conn, ok := s.connForSession(sessionID)
	if !ok {
		slog.Error("ResumeSession 后仍无法找到连接", "session", sessionID)
		return nil, ErrSessionNotActive
	}
	return conn, nil
}

// Prompt 向会话发送 prompt，返回流式 Message channel。
// 每条 SessionUpdate 会被映射为 models.Message 并持久化到数据库后转发给调用方。
func (s *Service) Prompt(ctx context.Context, sessionID, prompt string) (<-chan models.Message, error) {
	return s.PromptWithExecution(ctx, sessionID, prompt, nil)
}

// PromptWithExecution 与 Prompt 相同，并为本次执行的所有消息写入 executionID。
// executionID 为 nil 时表示手动会话（不标记执行块）。
// pending 状态的会话在首次调用时自动完成连接建立与 ACP 会话创建。
func (s *Service) PromptWithExecution(ctx context.Context, sessionID, prompt string, executionID *uint) (<-chan models.Message, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	// 内置 -opennexus 命令拦截（客户端侧处理，不发给 agent，不与原生命令冲突）：
	// /opennexus-yolo-on|off|status 开关会话 YOLO（on/off 可附带任务内容，
	// 切换后把发给 agent 的 prompt 改写为剩余任务）；/opennexus-goal 由通用 goal 控制器处理
	// （status/clear 本地合成回复直接返回；set 把发给 agent 的 prompt 改写为 goal directive）。
	promptForAgent := prompt
	if handled, yoloCh := s.interceptYolo(session, sessionID, prompt, executionID, &promptForAgent); handled {
		return yoloCh, nil
	}
	if handled, goalCh := s.interceptGoal(session, sessionID, prompt, executionID, &promptForAgent); handled {
		return goalCh, nil
	}
	// error/closed 会话：发送前尝试自动恢复（复用共享连接、重建 ACP 会话并注入最近历史），
	// 成功后状态回到 active 继续发送；恢复失败才返回 ErrSessionNotActive。
	reconnected := false
	if session.Status == models.SessionStatusError || session.Status == models.SessionStatusClosed {
		slog.Warn("会话非活跃，发送前尝试自动恢复", "session", sessionID, "status", session.Status, "agent", session.AgentType)
		if _, recErr := s.ResumeSession(ctx, sessionID); recErr != nil {
			slog.Error("自动恢复非活跃会话失败", "session", sessionID, "err", recErr)
			return nil, ErrSessionNotActive
		}
		slog.Info("会话重连成功", "session", sessionID, "status", session.Status, "agent", session.AgentType)
		reconnected = true
		if session, err = s.GetSession(sessionID); err != nil {
			return nil, err
		}
	}
	if session.Status != models.SessionStatusActive && session.Status != models.SessionStatusPending {
		return nil, ErrSessionNotActive
	}

	// 延迟激活：pending 会话在首次 Prompt 时创建 ACP 会话
	if session.Status == models.SessionStatusPending {
		cwd := sessionCwd(session, s.workspaces)
		conn, actErr := s.ensureConnection(ctx, session.AgentType, cwd)
		if actErr != nil {
			return nil, fmt.Errorf("激活会话-建立连接: %w", actErr)
		}
		s.debugBindPending(session.AgentType, session.ID)
		newAgentSID, configOptions, modes, actErr := conn.NewSession(ctx, cwd, s.sessionAdditionalDirs(session, cwd), s.sessionMCPServers(session.UserID, conn.McpCapabilities()), s.rulesSystemPrompt(cwd))
		s.debugClearPending(session.AgentType)
		if actErr != nil {
			return nil, fmt.Errorf("激活会话-创建 ACP 会话: %w", actErr)
		}

		// 写入 agent_session_id，不改稳定 session_id
		if actErr = s.sessions.UpdateAgentSessionID(session.ID, newAgentSID); actErr != nil {
			_ = conn.CloseSessionByID(ctx, newAgentSID)
			return nil, fmt.Errorf("激活会话-更新 agent_session_id: %w", actErr)
		}
		if actErr = s.sessions.UpdateStatus(session.ID, models.SessionStatusActive, nil); actErr != nil {
			_ = conn.CloseSessionByID(ctx, newAgentSID)
			return nil, fmt.Errorf("激活会话-更新状态: %w", actErr)
		}

		// 内存路由以稳定 session_id 为键
		poolKey := connectionKey(session.AgentType, cwd)
		s.mu.Lock()
		s.sessionPoolKey[sessionID] = poolKey
		if len(configOptions) > 0 {
			s.configs[sessionID] = configOptions
		}
		if len(modes) > 0 {
			s.modes[sessionID] = modes
			s.agentModes[session.AgentType] = modes
		}
		s.mu.Unlock()
		s.debugRegister(newAgentSID, session.ID)
		s.debugLog(session.ID, "new_session", newAgentSID, map[string]any{
			"agent": session.AgentType, "cwd": cwd,
		})
		slog.Info("agent 会话已激活", "agent", session.AgentType, "session", sessionID, "agent_session", newAgentSID, "cwd", cwd)

		session.AgentSessionID = newAgentSID
		session.Status = models.SessionStatusActive
		s.rebindACPSessionYolo("", newAgentSID, session.Yolo)

		// 应用用户在创建会话时选择的模型（configOptions 在激活时才可用，故延迟到此设置）
		if modelValue := strings.TrimSpace(session.ModelValue); modelValue != "" {
			if err := s.applyModelValue(ctx, sessionID, newAgentSID, configOptions, modelValue); err != nil {
				slog.Warn("激活会话-应用用户模型失败", "agent", session.AgentType, "session", sessionID, "model", modelValue, "err", err)
			}
		}
	}

	conn, ok := s.connForSession(sessionID)
	if !ok {
		// 内存路由丢失（服务重启 / 连接断开），但 DB 状态仍为 active：
		// 必须走完整 ResumeSession 重建 ACP 会话并刷新 agent_session_id，
		// 否则会用旧进程遗留的 acpSID 向新连接发 prompt，导致失败。
		slog.Warn("会话连接丢失，走完整 ResumeSession 恢复", "session", sessionID, "agent", session.AgentType)
		if _, recErr := s.ResumeSession(ctx, sessionID); recErr != nil {
			slog.Error("自动恢复会话失败", "session", sessionID, "err", recErr)
			return nil, ErrSessionNotActive
		}
		slog.Info("会话连接丢失-重连成功", "session", sessionID, "agent", session.AgentType)
		reconnected = true
		// ResumeSession 已更新 agent_session_id 与 sessionPoolKey，重新读取
		session, err = s.GetSession(sessionID)
		if err != nil {
			return nil, err
		}
		conn, ok = s.connForSession(sessionID)
		if !ok {
			slog.Error("ResumeSession 后仍无法找到连接", "session", sessionID)
			return nil, ErrSessionNotActive
		}
	}

	acpSID := agentSessionID(session)
	agentPrompt := s.expandPrompt(sessionID, session, promptForAgent)

	// 注入工作区附加目录上下文，让 AI 知晓可访问的额外目录
	if dirCtx := s.workspaceDirContext(session); dirCtx != "" {
		agentPrompt = dirCtx + "\n" + agentPrompt
	}
	slog.Debug("发送 agent prompt",
		"session", sessionID,
		"agent_session", acpSID,
		"agent", session.AgentType,
		"prompt_chars", len(prompt),
		"expanded_chars", len(agentPrompt),
		"preview", logging.Preview(prompt, 120),
	)
	s.debugLog(session.ID, "prompt", acpSID, map[string]any{
		"chars": len(prompt), "preview": logging.Preview(prompt, 80),
	})
	// prompt 用独立 ctx：生命周期脱离发起它的 HTTP/SSE 请求。
	// SSE 断开（c.Request.Context cancel）不再误杀 agent 本轮；agent 继续，
	// 用户经断点续传（subscribeStream）重连接上。超时兜底防 agent 卡死致 goroutine 泄漏。
	// 注意：promptCancel 由消费 goroutine 的 defer 调用——PromptWithExecution 在启动 goroutine
	// 后立即返回（返回 out channel），若在此处 defer cancel 会在返回瞬间即取消 prompt。
	promptCtx, promptCancel := context.WithTimeout(context.Background(), s.effectivePromptMaxDuration())
	updates, err := conn.Prompt(promptCtx, acpSID, agentPrompt)
	if err != nil {
		promptCancel()
		return nil, err
	}

	_ = s.sessions.UpdateLastPrompt(session.ID, prompt)
	// 刷新心跳表活动时间：让 watchdog 知道该连接刚被使用，不判为空闲
	s.recordConnectionActivity(connectionKey(session.AgentType, sessionCwd(session, s.workspaces)))

	// 首次对话时从 prompt 提取标题（仅当 title 为空时设置）
	if session.Title == "" {
		title := extractTitle(prompt)
		if title != "" {
			_ = s.sessions.UpdateTitle(session.ID, title)
		}
	}

	// 首次对话时异步触发任务自动打标签 / AI 标题生成（fire-and-forget，不阻塞主对话流）。
	// 仅在首次对话触发，避免每次追问都重复分类。
	if s.taskMetaTrigger != nil && session.Title == "" {
		uid := session.UserID
		dbID := session.ID
		p := prompt
		go s.taskMetaTrigger.ProcessTask(uid, dbID, p)
	}

	// 创建广播器：当前 prompt 的所有消息经广播器分发，支持多客户端订阅（断点续传重连）。
	startSeq := s.getNextSequence(session.SessionID)
	bc := newMsgBroadcaster(startSeq)
	s.registerBroadcaster(sessionID, bc)

	// 创建 running_task 记录，用于服务重启后的中断恢复。
	task := &models.RunningTask{
		DBSessionID: session.ID,
		UserID:      session.UserID,
		Prompt:      prompt,
		Status:      models.RunningTaskStatusRunning,
		LastSeq:     startSeq,
		ExecutionID: executionID,
		StartedAt:   time.Now(),
	}
	if err := s.runningTasks.Create(task); err != nil {
		slog.Warn("创建 running_task 记录失败", "session", sessionID, "err", err)
	}
	finishTask := func(status string) {
		if task.ID == 0 {
			return
		}
		now := time.Now()
		if err := s.runningTasks.UpdateStatus(task.ID, status, &now); err != nil {
			slog.Warn("更新 running_task 状态失败", "task", task.ID, "status", status, "err", err)
		}
	}

	// 主订阅者：发起 prompt 的原始请求消费此 channel。
	// prompt 前快照工作目录，用于结束后对比文件改动。
	snapshotCwd := sessionCwd(session, s.workspaces)
	snapshotBefore := takeSnapshot(snapshotCwd)
	out := make(chan models.Message, 256)
	go func() {
		seq := startSeq
		// finalStatus 控制 defer 收尾：正常完成=done，进程崩溃且不可恢复=interrupted
		finalStatus := models.RunningTaskStatusDone

		defer func() {
			// prompt 超时兜底：若因 promptCtx 超时退出（agent 卡死），标记 interrupted 而非 done，
			// 让前端提示"任务超时，可重发"。正常完成时 promptCtx 尚未到期，Err()==nil。
			if finalStatus == models.RunningTaskStatusDone && promptCtx.Err() == context.DeadlineExceeded {
				slog.Warn("prompt 超时，标记为 interrupted", "session", sessionID, "timeout", s.effectivePromptMaxDuration())
				finalStatus = models.RunningTaskStatusInterrupted
			}
			// prompt 结束后快照对比，生成文件改动摘要消息
			snapshotAfter := takeSnapshot(snapshotCwd)
			diffs := compareSnapshots(snapshotBefore, snapshotAfter)
			if len(diffs) > 0 {
				seq++
				fileMsg := MapFileWriteBatch(sessionID, session.ID, seq, diffs)
				fileMsg.ExecutionID = executionID
				if err := s.messages.Create(&fileMsg); err != nil {
					slog.Error("持久化文件改动摘要失败", "session", sessionID, "sequence", fileMsg.Sequence, "err", err)
				} else {
					bc.broadcast(fileMsg)
					out <- fileMsg
					if task.ID != 0 {
						_ = s.runningTasks.UpdateLastSeq(task.ID, fileMsg.Sequence)
					}
				}
			}
			close(out)
			bc.close()
			s.unregisterBroadcaster(sessionID)
			finishTask(finalStatus)
			// 通知任务管理同步 tasks.json 中会话登记任务的状态（否则登记条目永远显示运行中）
			if s.promptFinished != nil {
				s.promptFinished.PromptFinished(session.ID, finalStatus)
			}
			// goal 循环：本轮正常结束后评估是否达成、决定是否自动续轮（无 goal 时立即返回）。
			// 独立 goroutine：评估走临时会话可能耗时，不阻塞本 prompt 收尾。
			if finalStatus == models.RunningTaskStatusDone {
				go s.goalOnTurnEnd(sessionID)
			}
			// 最后释放 prompt ctx（goroutine 退出即本 prompt 终结）。放在末尾确保上面
			// 的 promptCtx.Err() 超时检查已完成。
			promptCancel()
		}()

		// persistMsg 持久化消息并广播，同步更新 running_task 的 LastSeq。
		persistMsg := func(msg models.Message) {
			if err := s.messages.Create(&msg); err != nil {
				slog.Error("持久化消息失败", "session", sessionID, "sequence", msg.Sequence, "err", err)
			}
			bc.broadcast(msg)
			out <- msg
			if task.ID != 0 {
				_ = s.runningTasks.UpdateLastSeq(task.ID, msg.Sequence)
			}
		}

		// 若本次发送触发了会话重连（非活跃/连接丢失），先推送一条临时状态帧
		// 提示前端进入“重新连接中”。该帧不落库、sequence=0（不占用正式序号，
		// 不影响断点续传游标），前端拦截后设 convState('reconnecting') 并过滤不入队。
		if reconnected {
			reconnMsg := models.Message{
				SessionID:   sessionID,
				DBSessionID: session.ID,
				Role:        models.MessageRoleAssistant,
				Kind:        models.MessageKindReconnecting,
				Content:     "检测到会话断开，正在重新连接...",
				Sequence:    0,
				ExecutionID: executionID,
				CreatedAt:   time.Now(),
			}
			bc.broadcast(reconnMsg)
			out <- reconnMsg
		}

		// 持久化用户发送的 prompt 作为 user_message_chunk
		seq++
		userUpdate := acp.SessionUpdate{
			UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{Text: prompt, Type: "text"},
				},
				SessionUpdate: "user_message_chunk",
			},
		}
		userMsg := MapUpdate(sessionID, session.ID, seq, userUpdate)
		userMsg.ExecutionID = executionID
		persistMsg(userMsg)

		// consumeStream 消费单次 prompt 的 update 流与权限/文件事件，直到流关闭。
		// 返回 true 表示 agent 进程在该轮运行中崩溃（conn.Done() 已关闭），调用方据此决定是否重连。
		consumeStream := func(conn *Connection, acpSID string, updates <-chan acp.SessionUpdate) bool {
			sid := acp.SessionId(acpSID)
			permCh := conn.Client().RegisterPermissionWaiter(sid)
			defer conn.Client().UnregisterPermissionWaiter(sid)
			fileCh := conn.Client().RegisterFileWaiter(sid)
			defer conn.Client().UnregisterFileWaiter(sid)

			// thought_chunk 攒批降频：agent 思考过程是高频 token delta，
			// 逐条同步落库会拖慢消费循环导致订阅者 buffer 满丢消息。
			// 思考片段照常实时广播/流出（保证 token-by-token 流式 UX），
			// 仅 DB 写入合并为一行：拼接相邻 delta 文本，RawJSON 用 \n 连接
			// （与前端 groupMessages 合并格式一致）。前端本就合并相邻 thought，
			// 故落库粒度变粗对展示零影响；也无任何代码按 thought 单条回查。
			var thoughtBatch []models.Message
			flushThoughts := func() {
				if len(thoughtBatch) == 0 {
					return
				}
				// 取批末尾作为合并记录：断线续传（sequence > lastSeq）即便落在批次中间，
				// 也能取回这批的完整合并文本（重复优于缺口，thought 可折叠且重复无副作用）。
				merged := thoughtBatch[len(thoughtBatch)-1]
				var sb strings.Builder
				raws := make([]string, 0, len(thoughtBatch))
				for _, m := range thoughtBatch {
					sb.WriteString(m.Content)
					if m.RawJSON != "" {
						raws = append(raws, m.RawJSON)
					}
				}
				merged.Content = sb.String()
				merged.RawJSON = strings.Join(raws, "\n")
				// 仅落库（广播/流出已在收到时实时完成，这里不重复）
				if err := s.messages.Create(&merged); err != nil {
					slog.Error("持久化合并 thought 失败", "session", sessionID, "sequence", merged.Sequence, "err", err)
				}
				if task.ID != 0 {
					_ = s.runningTasks.UpdateLastSeq(task.ID, merged.Sequence)
				}
				thoughtBatch = nil
			}

			// tool_call_update 同样高频（shell/read 输出流式），逐条 Create 会拖死
			// ACP 订阅 buffer。实时推前端；按 toolCallId 只保留最新一条延迟落库
			//（前端 parseToolCalls 本就按 id 合并，历史只需终态）。
			pendingToolUpdates := map[string]models.Message{}
			var toolUpdateOrder []string
			// pendingToolMeta 同窗口攒批工具调用记录的增量（状态/退出码等），随 flush 一并落库
			pendingToolMeta := map[string]*toolCallMeta{}
			flushToolUpdates := func() {
				if len(toolUpdateOrder) == 0 {
					return
				}
				for _, id := range toolUpdateOrder {
					m := pendingToolUpdates[id]
					if err := s.messages.Create(&m); err != nil {
						slog.Error("持久化 tool_call_update 失败", "session", sessionID, "sequence", m.Sequence, "err", err)
					}
					if task.ID != 0 {
						_ = s.runningTasks.UpdateLastSeq(task.ID, m.Sequence)
					}
					s.applyToolCallMeta(session.ID, id, pendingToolMeta[id])
				}
				pendingToolUpdates = map[string]models.Message{}
				toolUpdateOrder = nil
				pendingToolMeta = map[string]*toolCallMeta{}
			}
			flushPending := func() {
				flushThoughts()
				flushToolUpdates()
			}

			for {
				select {
				case u, ok := <-updates:
					if !ok {
						// 流关闭：先 flush 攒批，再判断是正常结束还是进程崩溃
						flushPending()
						select {
						case <-conn.Done():
							return true // 进程崩溃
						default:
							return false // 正常结束
						}
					}
					s.captureCommands(sessionID, u)
					seq++
					msg := MapUpdate(sessionID, session.ID, seq, u)
					msg.ExecutionID = executionID
					if msg.Kind == models.MessageKindAgentThoughtChunk {
						// 实时流式（内存，快），攒批延迟落库（降频）
						flushToolUpdates()
						bc.broadcast(msg)
						out <- msg
						thoughtBatch = append(thoughtBatch, msg)
					} else if msg.Kind == models.MessageKindToolCallUpdate {
						flushThoughts()
						bc.broadcast(msg)
						select {
						case out <- msg:
						default:
						}
						id := ""
						if u.ToolCallUpdate != nil {
							id = string(u.ToolCallUpdate.ToolCallId)
						}
						if id == "" {
							id = fmt.Sprintf("seq-%d", msg.Sequence)
						}
						if _, exists := pendingToolUpdates[id]; !exists {
							toolUpdateOrder = append(toolUpdateOrder, id)
						}
						pendingToolUpdates[id] = msg
						if u.ToolCallUpdate != nil && s.toolCallRecords != nil {
							// 合并本条增量到记录攒批；内嵌 terminal content 时立即写关联，
							// 保证终端退出回调能按 terminal_id 命中记录（每终端仅一次，低频）
							if pendingToolMeta[id] == nil {
								pendingToolMeta[id] = &toolCallMeta{}
							}
							mergeToolCallDelta(pendingToolMeta[id], u.ToolCallUpdate)
							s.linkToolCallTerminal(session.ID, u.ToolCallUpdate)
						}
					} else if msg.Kind == models.MessageKindUsageUpdate || msg.Kind == models.MessageKindSessionInfoUpdate {
						// 用量/会话信息是高频心跳：只推前端，不落库、不阻塞 out。
						// 逐条 Create + 阻塞 out 会拖慢本循环，导致 ACP 订阅 buffer 满并丢弃
						// agent_message_chunk（用户看到输出到一半突然没了）。
						bc.broadcast(msg)
						select {
						case out <- msg:
						default:
						}
					} else {
						// 非高频类型：先 flush 攒批，再同步落库本条
						flushPending()
						persistMsg(msg)
						if u.ToolCall != nil {
							// 工具调用历史：创建记录（shell 类解析 rawInput 命令/目录）
							s.recordToolCallStart(session, snapshotCwd, u.ToolCall)
						}
					}
				case pn, ok := <-permCh:
					if !ok {
						continue
					}
					slog.Debug("agent 权限请求", "session", sessionID, "request_id", pn.RequestID)
					flushPending()
					seq++
					msg := MapPermissionRequest(sessionID, session.ID, seq, pn)
					msg.ExecutionID = executionID
					persistMsg(msg)
				case fw, ok := <-fileCh:
					if !ok {
						continue
					}
					flushPending()
					seq++
					fileMsg := MapFileWrite(sessionID, session.ID, seq, fw)
					fileMsg.ExecutionID = executionID
					persistMsg(fileMsg)
				}
			}
		}

		// 消费首轮 prompt 流；返回 true 表示 agent 进程运行中崩溃
		if !consumeStream(conn, acpSID, updates) {
			return // 正常结束
		}

		// 运行中崩溃：按配置决定是否单次自动重连重发（避免无限循环烧 token）。
		if !s.failedTaskAutoRetryOnce {
			slog.Warn("agent 进程运行中崩溃，未开启自动重试，标记为 interrupted",
				"session", sessionID, "agent", session.AgentType)
			finalStatus = models.RunningTaskStatusInterrupted
			return
		}
		slog.Warn("agent 进程运行中崩溃，自动重连重发", "session", sessionID, "agent", session.AgentType)
		if _, err := s.ResumeSession(promptCtx, sessionID); err != nil {
			slog.Error("自动重连-恢复会话失败，标记为 interrupted", "session", sessionID, "err", err)
			finalStatus = models.RunningTaskStatusInterrupted
			return
		}
		// ResumeSession 已更新 agent_session_id 与连接池路由，重新解析连接与会话
		newConn, ok := s.connForSession(sessionID)
		if !ok {
			slog.Error("自动重连-重连后仍找不到连接，标记为 interrupted", "session", sessionID)
			finalStatus = models.RunningTaskStatusInterrupted
			return
		}
		refreshed, err := s.GetSession(sessionID)
		if err != nil {
			slog.Error("自动重连-重读会话失败，标记为 interrupted", "session", sessionID, "err", err)
			finalStatus = models.RunningTaskStatusInterrupted
			return
		}
		newAcpSID := agentSessionID(refreshed)
		newUpdates, err := newConn.Prompt(promptCtx, newAcpSID, agentPrompt)
		if err != nil {
			slog.Error("自动重连-重发 prompt 失败，标记为 interrupted", "session", sessionID, "err", err)
			finalStatus = models.RunningTaskStatusInterrupted
			return
		}
		// 消费重连后的流；二次崩溃则标记 interrupted
		if consumeStream(newConn, newAcpSID, newUpdates) {
			slog.Warn("agent 进程重连后再次崩溃，标记为 interrupted", "session", sessionID)
			finalStatus = models.RunningTaskStatusInterrupted
		}
	}()

	return out, nil
}

// CancelSession 取消正在进行的 prompt。
func (s *Service) CancelSession(ctx context.Context, sessionID string) error {
	// 用户主动取消视为放弃 goal：否则取消后流正常关闭（finalStatus=done）会误触发自动续轮。
	if s.clearGoal(sessionID) {
		slog.Info("会话取消，goal 已清除", "session", sessionID)
	}
	conn, ok := s.connForSession(sessionID)
	if !ok {
		return ErrSessionNotFound
	}
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	acpSID := agentSessionID(sess)
	s.debugLog(sess.ID, "cancel", acpSID, nil)
	conn.Client().CancelPermissions(acp.SessionId(acpSID))
	return conn.Cancel(ctx, acpSID)
}

// registerBroadcaster 注册指定会话的活跃 prompt 广播器。
func (s *Service) registerBroadcaster(sessionID string, bc *msgBroadcaster) {
	s.mu.Lock()
	s.activePrompts[sessionID] = bc
	s.mu.Unlock()
}

// unregisterBroadcaster 移除指定会话的活跃 prompt 广播器。
func (s *Service) unregisterBroadcaster(sessionID string) {
	s.mu.Lock()
	delete(s.activePrompts, sessionID)
	s.mu.Unlock()
}

// SubscribeSession 订阅指定会话当前进行中的 prompt 流，用于断点续传。
// lastSeq 为客户端最后收到的 message sequence；返回值：
//   - missed: DB 中 sequence > lastSeq 的遗漏消息（需先补发给客户端）
//   - ch: 实时消息 channel（若无进行中的 prompt 则为 nil）
//   - 若会话当前无活跃 prompt，返回 missed（补齐尾部）+ nil channel
func (s *Service) SubscribeSession(sessionID string, lastSeq int) (missed []models.Message, ch <-chan models.Message, err error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, nil, err
	}

	// 先从文件补齐 lastSeq 之后的遗漏消息
	missed, dbErr := s.messages.FindBySessionIDAfter(session.SessionID, lastSeq)
	if dbErr != nil {
		return nil, nil, dbErr
	}

	// 检查是否有活跃 prompt 广播器
	s.mu.RLock()
	bc, ok := s.activePrompts[sessionID]
	s.mu.RUnlock()

	if !ok || bc == nil {
		// 无活跃 prompt：仅返回 DB 补齐的消息，channel 为 nil
		return missed, nil, nil
	}

	// 订阅广播器，获取订阅时刻的 currentSeq
	subCh, curSeq := bc.subscribe(256)

	// 从订阅时刻的 currentSeq 之后去重 missed，避免与广播器即将推送的消息重复
	// （广播器 currentSeq 之后的实时消息会经 channel 推送，missed 只取到 currentSeq）
	if len(missed) > 0 && curSeq > lastSeq {
		filtered := missed[:0]
		for _, m := range missed {
			if m.Sequence <= curSeq {
				filtered = append(filtered, m)
			}
		}
		missed = filtered
	}

	return missed, subCh, nil
}

// HasActivePrompt 判断指定会话是否有进行中的 prompt。
func (s *Service) HasActivePrompt(sessionID string) bool {
	s.mu.RLock()
	_, ok := s.activePrompts[sessionID]
	s.mu.RUnlock()
	return ok
}

func (s *Service) detachSession(sessionID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	poolKey, ok := s.sessionPoolKey[sessionID]
	if ok {
		delete(s.sessionPoolKey, sessionID)
	}
	delete(s.commands, sessionID)
	delete(s.configs, sessionID)
	delete(s.modes, sessionID)
	return poolKey, ok
}

// hasActiveSessionForPoolKey 判断指定连接池键是否还有活跃 session 路由。
func (s *Service) hasActiveSessionForPoolKey(poolKey string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range s.sessionPoolKey {
		if key == poolKey {
			return true
		}
	}
	return false
}

// DeleteSession 彻底删除会话：释放连接、删除消息/记录，并清理 debug 日志与孤儿 temporary 工作区。
func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	wsID := session.WorkspaceID

	s.debugUnregister(agentSessionID(session))
	s.bindACPSessionYolo(agentSessionID(session), false)
	s.detachAndReleaseConn(ctx, session)
	// 释放该会话残留的 agent 终端（若有），防进程泄漏
	s.terminalBridge.ReleaseSession(session.ID)

	// 先删消息再删会话，避免孤儿消息
	if err := s.messages.DeleteBySessionID(session.SessionID); err != nil {
		return fmt.Errorf("删除会话消息: %w", err)
	}
	if err := s.sessions.Delete(session.ID); err != nil {
		return fmt.Errorf("删除会话记录: %w", err)
	}
	s.debugCleanup(session.ID)
	s.maybeCleanupOrphanTemporaryWorkspace(wsID)
	return nil
}

// detachAndReleaseConn 是 DeleteSession / DeleteSessionWithMessages 共享的清理逻辑。
// 1) detach 会话从 Service 的 sessionPoolKey/commands/configs/modes map 中移除（否则永久残留）。
// 2) 若该会话所属连接池已无其他活跃会话，关闭并释放共享 Connection（含子进程）。
// 3) 关闭并清理该会话可能残留的 activePrompts 广播器，防止 prompt goroutine 卡死时永久泄漏。
// 所有 s.pool 读写均在 s.mu 保护下进行，消除原 DeleteSession 中未持锁读 pool 的数据竞争。
func (s *Service) detachAndReleaseConn(ctx context.Context, session *models.Session) {
	poolKey, hadConn := s.detachSession(session.SessionID)

	// 清理可能残留的 activePrompts 广播器（prompt goroutine 卡死时会永久残留）
	s.mu.Lock()
	if bc, ok := s.activePrompts[session.SessionID]; ok {
		delete(s.activePrompts, session.SessionID)
		s.mu.Unlock()
		bc.close()
	} else {
		s.mu.Unlock()
	}

	if !hadConn {
		return
	}
	// 读 pool 必须持锁（原 DeleteSession 这里漏锁，触发与 ensureConnection 的数据竞争）
	s.mu.RLock()
	conn, connOK := s.pool[poolKey]
	s.mu.RUnlock()
	if !connOK {
		return
	}
	_ = conn.CloseSessionByID(ctx, agentSessionID(session))
	if !s.hasActiveSessionForPoolKey(poolKey) {
		s.mu.Lock()
		delete(s.pool, poolKey)
		delete(s.states, poolKey)
		s.mu.Unlock()
		_ = conn.Close()
	}
}

// maybeCleanupOrphanTemporaryWorkspace 在 temporary 工作区已无会话时删除目录与记录。
func (s *Service) maybeCleanupOrphanTemporaryWorkspace(wsID *uint) {
	if wsID == nil || *wsID == 0 {
		return
	}
	ws, err := s.workspaces.FindByID(*wsID)
	if err != nil || ws == nil || ws.Mode != models.WorkspaceModeTemporary {
		return
	}
	count, err := s.workspaces.SessionCount(*wsID)
	if err != nil || count > 0 {
		return
	}
	_ = s.workspaces.Delete(*wsID)
	if err := (&Workspace{Mode: ws.Mode, Cwd: ws.Cwd, TempDir: ws.TempDir}).Cleanup(); err != nil {
		slog.Warn("清理 temporary 工作区失败", "workspaceID", *wsID, "err", err)
	}
}

// ListSessions 列出指定用户的会话。
func (s *Service) ListSessions(userID uint) ([]models.Session, error) {
	return s.sessions.FindByUserID(userID)
}

// ListSessionsBySource 列出指定用户指定来源的会话。source 为空时返回全部。
func (s *Service) ListSessionsBySource(userID uint, source string) ([]models.Session, error) {
	return s.sessions.FindByUserIDAndSource(userID, source)
}

// ListExecutions 返回指定会话的定时执行块聚合（按 started_at 降序）。
func (s *Service) ListExecutions(sessionID string) ([]repository.ExecutionAggregate, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return s.messages.AggregateExecutions(session.SessionID)
}

// NextExecutionID 返回指定会话下一个可用的 execution_id（当前最大值 + 1）。
func (s *Service) NextExecutionID(sessionID string) (uint, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return 0, err
	}
	max, err := s.messages.MaxExecutionID(session.SessionID)
	if err != nil {
		return 0, err
	}
	return max + 1, nil
}

// GetSession 查询会话。
func (s *Service) GetSession(sessionID string) (*models.Session, error) {
	sess, err := s.sessions.FindBySessionID(sessionID)
	if err != nil {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

// UpdateTitle 更新会话标题。
func (s *Service) UpdateTitle(dbSessionID uint, title string) error {
	return s.sessions.UpdateTitle(dbSessionID, title)
}

// SetSessionYolo 设置会话级 YOLO，并同步到权限 broker 运行态。
func (s *Service) SetSessionYolo(dbSessionID uint, yolo bool) (*models.Session, error) {
	sess, err := s.sessions.FindByID(dbSessionID)
	if err != nil {
		return nil, ErrSessionNotFound
	}
	if err := s.sessions.UpdateYolo(dbSessionID, yolo); err != nil {
		return nil, err
	}
	sess.Yolo = yolo
	s.bindACPSessionYolo(agentSessionID(sess), yolo)
	return sess, nil
}

// RecoverActiveSessions 在服务启动时调用：
//  1. 将所有 running 状态的 running_task 标记为 interrupted（服务重启导致 prompt 中断）。
//  2. 仅将这些被中断任务对应的会话标记为 error（agent 进程已随重启终止，内存态丢失）。
//     不再批量标记所有 active 会话——已正常完成的空闲会话应保持 active。
//
// 用户可通过 ListInterruptedTasks 查看中断任务并手动重发。
func (s *Service) RecoverActiveSessions() {
	if err := s.runningTasks.MarkRunningAsInterrupted(); err != nil {
		slog.Warn("标记中断任务失败", "err", err)
	}
	ids, err := s.runningTasks.FindInterruptedDBSessionIDs()
	if err != nil {
		slog.Warn("查询中断任务会话失败", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	if err := s.sessions.MarkSessionsErrorByIDs(ids); err != nil {
		slog.Warn("标记中断会话为 error 失败", "err", err)
	}
}

// ListInterruptedTasks 返回指定会话下所有 interrupted 状态的任务。
func (s *Service) ListInterruptedTasks(dbSessionID uint) ([]models.RunningTask, error) {
	return s.runningTasks.FindInterruptedByDBSessionID(dbSessionID)
}

// ListRunningDBSessionIDs 返回指定用户下所有 status=running 的 db_session_id，
// 供侧边栏展示「哪些会话正在运行」。
func (s *Service) ListRunningDBSessionIDs(userID uint) ([]uint, error) {
	return s.runningTasks.FindRunningDBSessionIDsByUser(userID)
}

// ResumeInterruptedTask 恢复中断的任务：ResumeSession 后重新发送原 prompt。
// 返回的消息流与普通 Prompt 一致（含 id: sequence，经广播器分发）。
func (s *Service) ResumeInterruptedTask(ctx context.Context, taskID uint) (<-chan models.Message, error) {
	task, err := s.runningTasks.FindByID(taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != models.RunningTaskStatusInterrupted {
		return nil, fmt.Errorf("任务状态不是 interrupted（当前: %s），无法恢复", task.Status)
	}

	session, err := s.sessions.FindByID(task.DBSessionID)
	if err != nil {
		return nil, err
	}

	// 恢复会话（error/closed 状态会重开 ACP session）
	if _, err := s.ResumeSession(ctx, session.SessionID); err != nil {
		return nil, fmt.Errorf("恢复会话失败: %w", err)
	}

	// 重新发送原 prompt（使用重开后的 sessionID）
	ch, err := s.PromptWithExecution(ctx, session.SessionID, task.Prompt, task.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("重发 prompt 失败: %w", err)
	}

	// 标记任务已完成（重发后会创建新的 running_task 记录）
	now := time.Now()
	_ = s.runningTasks.UpdateStatus(taskID, models.RunningTaskStatusDone, &now)

	return ch, nil
}

// getNextSequence 获取指定会话当前最大 sequence 值（无消息时返回 0）。
func (s *Service) getNextSequence(sessionID string) int {
	max, err := s.messages.MaxSequence(sessionID)
	if err != nil {
		return 0
	}
	return max
}

// ListMessages 查询会话消息历史，按 sequence 升序返回。
// 默认最多返回 defaultMessagePageSize 条（最近 N 条），避免长会话一次性加载全部 raw_json。
// 如需更早的消息，调用方应使用 ListMessagesPaged 显式分页。
// 注意：不可用 Paged(limit, 0)——那是按 sequence 升序的「最早 N 条」，长会话刷新会把
// 前端刚流式展示的近期对话整页冲掉（表现为输出突然消失、自己发的消息不见了）。
func (s *Service) ListMessages(sessionID string) ([]models.Message, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return s.messages.FindBySessionIDLastN(session.SessionID, defaultMessagePageSize)
}

// defaultMessagePageSize 是 ListMessages 默认返回的消息上限。
// 选 500：覆盖绝大多数会话的完整历史，同时为超长会话设置硬上限避免内存爆炸。
const defaultMessagePageSize = 500

// maxMessagePageSize 是 ListMessagesPaged 允许的最大 limit，防止滥用。
const maxMessagePageSize = 1000

// ListMessagesPaged 分页查询会话消息，按 sequence 升序返回。
// limit<=0 时使用默认页大小；limit>maxMessagePageSize 时截断为最大值。
// offset 从 0 开始（最早一条起算）；要取「最近 N 条」请用 ListMessages。
func (s *Service) ListMessagesPaged(sessionID string, limit, offset int) ([]models.Message, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultMessagePageSize
	}
	if limit > maxMessagePageSize {
		limit = maxMessagePageSize
	}
	return s.messages.FindBySessionIDPaged(session.SessionID, limit, offset)
}

// ListMessagesByKind 仅查询指定 kind 的消息，按 sequence 升序返回。
// 用于文件变更等只关心特定 kind 的场景，避免加载无关消息的 raw_json。
func (s *Service) ListMessagesByKind(sessionID string, kind string) ([]models.Message, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return s.messages.FindByKind(session.SessionID, kind)
}

// FindMessageByID 按消息主键查询单条消息（用于撤销等按消息定位的场景）。
func (s *Service) FindMessageByID(messageID uint) (*models.Message, error) {
	return s.messages.FindByID(messageID)
}

// ListMessagesRecent 返回最近的若干条消息 + 是否还有更早的消息（供前端「加载更多」）。
// beforeSeq>0 时仅返回 sequence<beforeSeq 的消息（向前翻页游标）；<=0 时不限制。
// limit<=0 时使用默认页大小；limit>maxMessagePageSize 时截断为最大值。
// hasMore 表示当前返回范围之外是否还有更早的消息——前端据此决定是否显示「加载更多」。
// 实现上多取 1 条用于判断 hasMore，避免单独的 Count 调用。
func (s *Service) ListMessagesRecent(sessionID string, beforeSeq int, limit int) (msgs []models.Message, hasMore bool, err error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		limit = defaultMessagePageSize
	}
	if limit > maxMessagePageSize {
		limit = maxMessagePageSize
	}
	// 多取 1 条用于判断 hasMore：返回条数 > limit 即说明还有更早的消息。
	// fetched 为升序，末尾是最近的消息；截断时丢弃最早的那条多余条，保留最近的 limit 条。
	fetched, err := s.messages.FindBySessionIDBeforeLastN(session.SessionID, beforeSeq, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(fetched) > limit {
		return fetched[1:], true, nil
	}
	return fetched, false, nil
}

// DeleteMessagesFromSequence 删除指定会话中 sequence 大于等于 fromSeq 的消息（会话回滚，含目标）。
func (s *Service) DeleteMessagesFromSequence(sessionID string, fromSeq int) (int64, error) {
	return s.messages.DeleteFromSequence(sessionID, fromSeq)
}

// captureCommands 从 SessionUpdate 中提取 AvailableCommandsUpdate、ConfigOptionUpdate 和 CurrentModeUpdate 并缓存到会话。
func (s *Service) captureCommands(sessionID string, u acp.SessionUpdate) {
	if u.AvailableCommandsUpdate != nil {
		cmds := u.AvailableCommandsUpdate.AvailableCommands
		s.mu.Lock()
		s.commands[sessionID] = cmds
		if agentType, ok := s.agentTypeForSession(sessionID); ok && len(cmds) > 0 {
			s.agentCommands[agentType] = cmds
		}
		s.mu.Unlock()
	}
	if u.ConfigOptionUpdate != nil {
		opts := u.ConfigOptionUpdate.ConfigOptions
		s.mu.Lock()
		s.configs[sessionID] = opts
		s.mu.Unlock()
	}
	// CurrentModeUpdate 仅更新当前选中的 mode ID，可用 mode 列表不变，无需重新缓存
}

// ListCommands 返回会话可用的 slash command（Agent 原生 + 配置的 Claude Code commands）。
// 若会话级缓存为空（如服务重启后），回退到 agent 级缓存 agentCommands。
func (s *Service) ListCommands(sessionID string) ([]acp.AvailableCommand, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	cwd := sessionCwd(session, s.workspaces)
	s.mu.RLock()
	agentCmds := s.commands[sessionID]
	if len(agentCmds) == 0 {
		// 服务重启后会话级缓存丢失，回退到 agent 级缓存
		agentCmds = s.agentCommands[session.AgentType]
	}
	s.mu.RUnlock()
	merged := s.mergeCommands(agentCmds, cwd)
	return appendBuiltinCommands(merged), nil
}

// appendBuiltinCommands 追加内置 -opennexus 命令（goal 循环 / 会话 YOLO），供 "/" 弹窗展示。
// 命令名带后缀不与原生命令冲突；已存在同名命令时跳过。
func appendBuiltinCommands(cmds []acp.AvailableCommand) []acp.AvailableCommand {
	for _, builtin := range append([]acp.AvailableCommand{builtinGoalCommand()}, builtinYoloCommands()...) {
		exists := false
		for _, c := range cmds {
			if c.Name == builtin.Name {
				exists = true
				break
			}
		}
		if !exists {
			cmds = append(cmds, builtin)
		}
	}
	return cmds
}

// sessionCwd 返回会话的工作目录。
//
// 会话若被固定到自定义目录（创建时用户选择了 worktree/目录，或任务执行时指定了 worktree，
// 使 session.Cwd 与工作区 cwd 不同），优先使用 session.Cwd；否则 cwd 跟随工作区：
// 若工作区 cwd 被修改，已有会话应感知到新 cwd，因此这类会话实时从工作区重新读取
// （保留历史行为，避免回归）。
func sessionCwd(session *models.Session, workspaces *repository.WorkspaceRepository) string {
	if session.WorkspaceID != nil {
		if ws, err := workspaces.FindByID(*session.WorkspaceID); err == nil {
			// 会话被固定到自定义目录（cwd 与工作区 cwd 不同）时优先使用会话自身 cwd。
			if c := strings.TrimSpace(session.Cwd); c != "" && c != ws.Cwd {
				return session.Cwd
			}
			return ws.Cwd
		}
	}
	return session.Cwd
}

// sessionAdditionalDirs 返回 ACP 会话的 additionalDirectories：
// 工作区附加目录（次级）+ skills/commands/rules 目录 + 上传文件目录。
func (s *Service) sessionAdditionalDirs(session *models.Session, cwd string) []string {
	var wsDirs []string
	if session.WorkspaceID != nil {
		if ws, err := s.workspaces.FindByID(*session.WorkspaceID); err == nil {
			wsDirs = ws.Directories
			slog.Info("工作区附加目录", "workspaceID", *session.WorkspaceID, "directories", wsDirs)
		} else {
			slog.Warn("查找工作区附加目录失败", "workspaceID", *session.WorkspaceID, "err", err)
		}
	} else {
		slog.Warn("会话无 WorkspaceID，无法获取附加目录", "sessionID", session.SessionID)
	}
	// 上传文件已移出 cwd（落在工作区管理数据目录），存在时一并授权，
	// 使 agent 能读取 @<绝对路径> 引用的上传文件。
	var uploadDirs []string
	if up := workspacemeta.UploadsDirFor(cwd); up != "" && !strings.HasPrefix(up, cwd+string(filepath.Separator)) {
		if info, err := os.Stat(up); err == nil && info.IsDir() {
			uploadDirs = append(uploadDirs, up)
		}
	}
	result := MergeAdditionalDirectories(
		wsDirs,
		s.skillAdditionalDirs(cwd),
		uploadDirs,
	)
	slog.Info("会话 AdditionalDirectories", "sessionID", session.SessionID, "total", len(result), "dirs", result)
	return result
}

// workspaceDirContext 返回工作区附加目录的 prompt 上下文文本。
// 仅包含用户配置的工作区次级目录（不含 skills/commands/rules），以便 AI 知晓可访问的额外文件目录。
func (s *Service) workspaceDirContext(session *models.Session) string {
	if session.WorkspaceID == nil {
		return ""
	}
	ws, err := s.workspaces.FindByID(*session.WorkspaceID)
	if err != nil || len(ws.Directories) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<workspace_directories>\n以下目录在当前工作区中可访问：\n")
	for _, d := range ws.Directories {
		sb.WriteString(fmt.Sprintf("- %s\n", d))
	}
	sb.WriteString("你可以读取和操作这些目录中的文件。\n")
	sb.WriteString("</workspace_directories>")
	return sb.String()
}

func (s *Service) expandPrompt(sessionID string, session *models.Session, prompt string) string {
	cwd := sessionCwd(session, s.workspaces)
	s.mu.RLock()
	agentCmds := s.commands[sessionID]
	modes := s.modes[sessionID]
	s.mu.RUnlock()
	_, expanded := ExpandPrompt(ExpandPromptInput{
		Prompt:             prompt,
		Cwd:                cwd,
		SkillUserDirs:      s.skillUserDirs,
		SkillProjectDirs:   s.skillProjectDirs,
		CommandUserDirs:    s.commandUserDirs,
		CommandProjectDirs: s.commandProjectDirs,
		AgentCommands:      agentCmds,
		Modes:              modes,
	})
	if expanded != prompt {
		slog.Info("slash 调用已展开", "session", sessionID, "chars", len(expanded))
	}
	return expanded
}

// rulesSystemPrompt 汇总 alwaysApply 规则，注入 session/new 的 _meta.systemPrompt。
func (s *Service) rulesSystemPrompt(cwd string) string {
	return AlwaysApplySystemPrompt(cwd, s.ruleUserDirs, s.ruleProjectDirs)
}

func (s *Service) mergeCommands(agentCmds []acp.AvailableCommand, cwd string) []acp.AvailableCommand {
	configured := SlashCommandsToAvailable(ScanSlashCommands(cwd, s.commandUserDirs, s.commandProjectDirs))
	return MergeAvailableCommands(agentCmds, configured)
}

// ListConfiguredCommands 扫描配置的 slash command（Claude Code commands 目录）。
func (s *Service) ListConfiguredCommands(cwd string) []SlashCommand {
	return ScanSlashCommands(cwd, s.commandUserDirs, s.commandProjectDirs)
}

// ListConfiguredCommandsForSession 扫描会话工作区下的配置 slash command。
func (s *Service) ListConfiguredCommandsForSession(sessionID string) ([]SlashCommand, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	cwd := sessionCwd(session, s.workspaces)
	return ScanSlashCommands(cwd, s.commandUserDirs, s.commandProjectDirs), nil
}

// ListConfigOptions 返回会话缓存的 config option 列表（含模型选择等）。
// 若会话级缓存为空（如服务重启后），回退到 agent 级探测缓存 probeCache。
func (s *Service) ListConfigOptions(sessionID string) ([]acp.SessionConfigOption, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	opts := s.configs[sessionID]
	if len(opts) == 0 {
		// 服务重启后会话级缓存丢失，回退到 agent 级缓存
		opts = s.probeCache[session.AgentType]
	}
	out := make([]acp.SessionConfigOption, len(opts))
	copy(out, opts)
	return out, nil
}

// CachedModelOptions 返回指定 agent 类型的可用模型 config option（从已有会话缓存获取）。
// 仅返回 category=model 的 select 类型 option。若该 agent 类型尚无会话缓存则返回 nil。
func (s *Service) CachedModelOptions(agentType string) []acp.SessionConfigOption {
	s.mu.RLock()
	sessionIDs := make([]string, 0, len(s.configs))
	for sid := range s.configs {
		sessionIDs = append(sessionIDs, sid)
	}
	s.mu.RUnlock()

	for _, sid := range sessionIDs {
		sess, err := s.GetSession(sid)
		if err != nil || sess.AgentType != agentType {
			continue
		}
		s.mu.RLock()
		opts := s.configs[sid]
		s.mu.RUnlock()
		for _, opt := range opts {
			if opt.Select == nil || opt.Select.Category == nil {
				continue
			}
			if string(*opt.Select.Category) == "model" {
				return []acp.SessionConfigOption{opt}
			}
		}
	}
	return nil
}

// CachedCommands 返回指定 agent 类型的 slash command（Agent 原生 + 配置的 commands + 内置命令）。
// cwd 非空时一并扫描项目级 commands 目录。新建任务页 /agents/:type/commands 使用。
func (s *Service) CachedCommands(agentType string, cwd string) []acp.AvailableCommand {
	s.mu.RLock()
	agentCmds := s.agentCommands[agentType]
	s.mu.RUnlock()
	return appendBuiltinCommands(s.mergeCommands(agentCmds, cwd))
}

// CachedModes 返回指定 agent 类型缓存的 session mode（来自探测或已有会话）。
func (s *Service) CachedModes(agentType string) []acp.SessionMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	modes := s.agentModes[agentType]
	if len(modes) == 0 {
		return nil
	}
	out := make([]acp.SessionMode, len(modes))
	copy(out, modes)
	return out
}

// ProbeConfigOptions 返回指定 agent 类型的 config options。
// 结果在 agent 首次连接时预探测并缓存在内存中；缓存未命中时轻量探测（仅 NewSession，不发送 prompt）。
func (s *Service) ProbeConfigOptions(ctx context.Context, agentType string, userID uint) ([]acp.SessionConfigOption, error) {
	_ = userID
	return s.fetchProbeConfig(ctx, agentType)
}

func (s *Service) prefetchProbeConfig(ctx context.Context, agentType string) {
	if _, err := s.fetchProbeConfig(ctx, agentType); err != nil {
		slog.Warn("预探测 agent 配置失败", "agent", agentType, "err", err)
	}
}

func (s *Service) fetchProbeConfig(ctx context.Context, agentType string) ([]acp.SessionConfigOption, error) {
	s.mu.RLock()
	if cached, ok := s.probeCache[agentType]; ok {
		out := make([]acp.SessionConfigOption, len(cached))
		copy(out, cached)
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()

	s.probeLock.Lock()
	defer s.probeLock.Unlock()

	s.mu.RLock()
	if cached, ok := s.probeCache[agentType]; ok {
		out := make([]acp.SessionConfigOption, len(cached))
		copy(out, cached)
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()

	opts, err := s.probeConfigViaSession(ctx, agentType)
	if err != nil {
		return nil, err
	}
	out := make([]acp.SessionConfigOption, len(opts))
	copy(out, opts)
	return out, nil
}

func (s *Service) probeCwd() string {
	if s.wsConfig.SessionDir != "" {
		return s.wsConfig.SessionDir
	}
	return "/tmp"
}

// probeConfigViaSession 创建临时 ACP session 读取 ConfigOptions，不写入数据库、不发送 prompt。
func (s *Service) probeConfigViaSession(ctx context.Context, agentType string) ([]acp.SessionConfigOption, error) {
	probeCwd := s.probeCwd()
	conn, err := s.ensureConnection(ctx, agentType, probeCwd)
	if err != nil {
		return nil, err
	}
	sessionID, configOptions, modes, err := conn.NewSession(ctx, probeCwd, s.skillAdditionalDirs(probeCwd), nil, "")
	if err != nil {
		return nil, fmt.Errorf("探测 NewSession: %w", err)
	}
	defer func() { _ = conn.CloseSessionByID(ctx, sessionID) }()

	cmds := s.collectProbeCommands(ctx, conn, sessionID)

	out := make([]acp.SessionConfigOption, len(configOptions))
	copy(out, configOptions)

	s.mu.Lock()
	s.probeCache[agentType] = out
	if len(modes) > 0 {
		s.agentModes[agentType] = modes
	}
	if len(cmds) > 0 {
		s.agentCommands[agentType] = cmds
	}
	s.mu.Unlock()
	slog.Info("探测配置已缓存", "agent", agentType, "config_count", len(out), "modes", len(modes), "commands", len(cmds))
	return out, nil
}

// collectProbeCommands 在探测 session 上短暂等待 AvailableCommandsUpdate。
func (s *Service) collectProbeCommands(ctx context.Context, conn *Connection, sessionID string) []acp.AvailableCommand {
	sid := acp.SessionId(sessionID)
	ch := conn.Client().RegisterStream(sid, 8)
	defer conn.Client().UnregisterStream(sid)

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case u, ok := <-ch:
			if !ok {
				return nil
			}
			if u.AvailableCommandsUpdate != nil {
				return u.AvailableCommandsUpdate.AvailableCommands
			}
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// ListModes 返回会话可用的 mode 列表（agent skill/模式，如 plan/act）。
// 若会话级缓存为空（如服务重启后），回退到 agent 级缓存 agentModes。
func (s *Service) ListModes(sessionID string) ([]acp.SessionMode, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	modes := s.modes[sessionID]
	if len(modes) == 0 {
		// 服务重启后会话级缓存丢失，回退到 agent 级缓存
		modes = s.agentModes[session.AgentType]
	}
	out := make([]acp.SessionMode, len(modes))
	copy(out, modes)
	return out, nil
}

// skillAdditionalDirs 返回当前会话 cwd 应对 Agent 暴露的 skills/commands/rules 根目录。
func (s *Service) skillAdditionalDirs(cwd string) []string {
	return MergeAdditionalDirectories(
		SkillAdditionalDirectories(cwd, s.skillUserDirs, s.skillProjectDirs),
		SkillAdditionalDirectories(cwd, s.commandUserDirs, s.commandProjectDirs),
		RuleAdditionalDirectories(cwd, s.ruleUserDirs, s.ruleProjectDirs),
	)
}

// ListSkills 扫描会话工作目录和用户主目录下的 Agent Skills（agentskills.io 规范）。
func (s *Service) ListSkills(sessionID string) ([]Skill, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	cwd := sessionCwd(session, s.workspaces)
	return ScanSkills(cwd, s.skillUserDirs, s.skillProjectDirs), nil
}

// ListSubAgents 扫描 subagent 定义文件（~/.agents/agents 等）。
// 使用 probeCwd 作为项目级扫描根（subagent MCP server 无会话上下文）。
func (s *Service) ListSubAgents() []SubAgentDef {
	s.mu.RLock()
	userDirs := append([]string(nil), s.subAgentUserDirs...)
	projectDirs := append([]string(nil), s.subAgentProjectDirs...)
	s.mu.RUnlock()
	return ScanSubAgents(s.probeCwd(), userDirs, projectDirs)
}

// ResolveSubAgent 按 name 查找单个 subagent 定义。未找到返回 nil。
func (s *Service) ResolveSubAgent(name string) *SubAgentDef {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	defs := s.ListSubAgents()
	for i := range defs {
		if defs[i].Name == name {
			return &defs[i]
		}
	}
	return nil
}

// SetConfigOption 设置会话的 config option 值（如切换模型）。
// 连接丢失时自动恢复会话，避免服务重启后切换模型直接报「会话不在活跃状态」。
func (s *Service) SetConfigOption(ctx context.Context, sessionID, configID, value string) error {
	conn, err := s.connForSessionOrResume(ctx, sessionID)
	if err != nil {
		return err
	}
	// 恢复后 agent_session_id 可能已刷新，须在拿到连接之后再读取会话。
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	acpSID := agentSessionID(sess)
	s.debugLog(sess.ID, "set_config", acpSID, map[string]any{"config_id": configID, "value": value})
	if err := conn.SetConfigOption(ctx, acpSID, configID, value); err != nil {
		return err
	}
	// 若切换的是模型（category=model），持久化到会话记录，
	// 使配置项回显始终为「实际使用/发送时选择的模型」，避免刷新后回落到 agent 默认模型。
	if s.isModelConfigOption(sessionID, configID) {
		if err := s.sessions.UpdateModelValue(sess.ID, value); err != nil {
			slog.Warn("持久化会话模型失败", "session_id", sessionID, "value", value, "error", err)
		}
	}
	return nil
}

// isModelConfigOption 判断给定 configID 在会话缓存的配置项中是否属于 category=model。
func (s *Service) isModelConfigOption(sessionID, configID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, opt := range s.configs[sessionID] {
		if opt.Select == nil || opt.Select.Category == nil {
			continue
		}
		if string(opt.Select.Id) == configID {
			return string(*opt.Select.Category) == "model"
		}
	}
	return false
}

// SetSessionMode 切换会话模式（如 ask / agent / edit）。
// 连接丢失时自动恢复会话，避免服务重启后切换模式直接报「会话不在活跃状态」。
func (s *Service) SetSessionMode(ctx context.Context, sessionID, modeID string) error {
	conn, err := s.connForSessionOrResume(ctx, sessionID)
	if err != nil {
		return err
	}
	// 恢复后 agent_session_id 可能已刷新，须在拿到连接之后再读取会话。
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	acpSID := agentSessionID(sess)
	s.debugLog(sess.ID, "set_mode", acpSID, map[string]any{"mode_id": modeID})
	return conn.SetSessionMode(ctx, acpSID, modeID)
}

// RespondPermission 提交用户对权限请求的响应。
func (s *Service) RespondPermission(sessionID, requestID, optionID string, cancelled bool) error {
	conn, ok := s.connForSession(sessionID)
	if !ok {
		return ErrSessionNotActive
	}
	return conn.Client().RespondPermission(requestID, optionID, cancelled)
}

// GetSessionByDBID 按数据库主键查询会话。
func (s *Service) GetSessionByDBID(id uint) (*models.Session, error) {
	sess, err := s.sessions.FindByID(id)
	if err != nil {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

// ResumeSession 恢复或重开会话：在共享连接上新建 ACP session、注入历史上下文、更新 agent_session_id。
// active 且连接存在的会话直接返回；error 与 closed 状态均会尝试恢复。
func (s *Service) ResumeSession(ctx context.Context, sessionID string) (*models.Session, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}

	// active 且连接存在 → 直接返回
	if session.Status == models.SessionStatusActive {
		if _, ok := s.connForSession(sessionID); ok {
			return session, nil
		}
	}

	// 会话若被固定到自定义目录，优先使用 session.Cwd；否则跟随工作区 cwd
	cwd := sessionCwd(session, s.workspaces)
	wsMode := ""
	if session.WorkspaceID != nil {
		if ws, wsErr := s.workspaces.FindByID(*session.WorkspaceID); wsErr == nil {
			wsMode = ws.Mode
		}
	}
	if cwd == "" {
		return nil, errors.New("恢复会话需要工作目录，请提供有效的 workspace")
	}
	if err := EnsureWorkspaceDir(wsMode, cwd); err != nil {
		return nil, err
	}

	// 复用共享连接（不存在则自动建立）
	conn, err := s.ensureConnection(ctx, session.AgentType, cwd)
	if err != nil {
		return nil, fmt.Errorf("恢复会话-建立连接: %w", err)
	}

	oldAgentSID := session.AgentSessionID
	// 重开会话前清掉旧 agent 会话的挂起权限：旧 agent 进程的权限请求 requestID 已失效，
	// 新会话的权限是新 requestID。不清则残留死权限（respond 永远无意义）。
	if oldAgentSID != "" {
		conn.Client().CancelPermissions(acp.SessionId(oldAgentSID))
	}
	s.debugBindPending(session.AgentType, session.ID)
	newAgentSID, configOptions, modes, err := conn.NewSession(ctx, cwd, s.sessionAdditionalDirs(session, cwd), s.sessionMCPServers(session.UserID, conn.McpCapabilities()), s.rulesSystemPrompt(cwd))
	s.debugClearPending(session.AgentType)
	if err != nil {
		return nil, fmt.Errorf("恢复会话-创建 ACP 会话: %w", err)
	}

	// 查询历史消息并注入上下文（只取最近 100 条，避免长会话全量加载 raw_json 撑爆内存）
	history, _ := s.messages.FindBySessionIDLastN(session.SessionID, 100)
	contextText := formatHistory(history)
	if contextText != "" {
		// 异步注入历史上下文，不等结果
		go func() {
			_, _ = conn.Prompt(ctx, newAgentSID, contextText)
		}()
	}

	// 更新 agent_session_id 和状态（closed_at 置空）；稳定 session_id 不变
	if err := s.sessions.UpdateAgentSessionID(session.ID, newAgentSID); err != nil {
		_ = conn.CloseSessionByID(ctx, newAgentSID)
		return nil, fmt.Errorf("恢复会话-更新 agent_session_id: %w", err)
	}
	if err := s.sessions.UpdateStatus(session.ID, models.SessionStatusActive, nil); err != nil {
		_ = conn.CloseSessionByID(ctx, newAgentSID)
		return nil, fmt.Errorf("恢复会话-更新状态: %w", err)
	}

	// 稳定 session_id 为键，更新路由与缓存
	poolKey := connectionKey(session.AgentType, cwd)
	s.mu.Lock()
	s.sessionPoolKey[sessionID] = poolKey
	if len(configOptions) > 0 {
		s.configs[sessionID] = configOptions
	}
	if len(modes) > 0 {
		s.modes[sessionID] = modes
	}
	s.mu.Unlock()
	if oldAgentSID != "" {
		s.debugUnregister(oldAgentSID)
	}
	s.debugRegister(newAgentSID, session.ID)
	s.debugLog(session.ID, "resume_session", newAgentSID, map[string]any{
		"agent": session.AgentType, "cwd": cwd,
	})
	s.rebindACPSessionYolo(oldAgentSID, newAgentSID, session.Yolo)

	// 返回更新后的 session
	return s.sessions.FindByID(session.ID)
}

// ClearContext 清理会话上下文：新建一条全新的 ACP 会话替换旧会话，
// 但不注入历史消息，使模型上下文（token 占用）归零。数据库会话与历史消息展示保留。
// 与 ResumeSession 的区别：始终重建 ACP 会话、不注入历史，并追加一条 used=0 的 usage_update。
func (s *Service) ClearContext(ctx context.Context, sessionID string) (*models.Session, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}

	// 进行中的 prompt 不允许清理上下文，避免与实时流状态竞态。
	if s.HasActivePrompt(sessionID) {
		return nil, errors.New("会话有进行中的任务，无法清理上下文")
	}

	// 会话若被固定到自定义目录，优先使用 session.Cwd；否则跟随工作区 cwd
	cwd := sessionCwd(session, s.workspaces)
	wsMode := ""
	if session.WorkspaceID != nil {
		if ws, wsErr := s.workspaces.FindByID(*session.WorkspaceID); wsErr == nil {
			wsMode = ws.Mode
		}
	}
	if cwd == "" {
		return nil, errors.New("清理上下文需要工作目录，请提供有效的 workspace")
	}
	if err := EnsureWorkspaceDir(wsMode, cwd); err != nil {
		return nil, err
	}

	// 复用共享连接（不存在则自动建立）
	conn, err := s.ensureConnection(ctx, session.AgentType, cwd)
	if err != nil {
		return nil, fmt.Errorf("清理上下文-建立连接: %w", err)
	}

	oldAgentSID := session.AgentSessionID
	s.debugBindPending(session.AgentType, session.ID)
	newAgentSID, configOptions, modes, err := conn.NewSession(ctx, cwd, s.sessionAdditionalDirs(session, cwd), s.sessionMCPServers(session.UserID, conn.McpCapabilities()), s.rulesSystemPrompt(cwd))
	s.debugClearPending(session.AgentType)
	if err != nil {
		return nil, fmt.Errorf("清理上下文-创建 ACP 会话: %w", err)
	}

	// 关闭旧 ACP 会话，释放其上下文（best-effort）。
	if oldAgentSID != "" && oldAgentSID != newAgentSID {
		_ = conn.CloseSessionByID(ctx, oldAgentSID)
	}

	// 更新 agent_session_id 和状态（closed_at 置空）；稳定 session_id 不变
	if err := s.sessions.UpdateAgentSessionID(session.ID, newAgentSID); err != nil {
		_ = conn.CloseSessionByID(ctx, newAgentSID)
		return nil, fmt.Errorf("清理上下文-更新 agent_session_id: %w", err)
	}
	if err := s.sessions.UpdateStatus(session.ID, models.SessionStatusActive, nil); err != nil {
		_ = conn.CloseSessionByID(ctx, newAgentSID)
		return nil, fmt.Errorf("清理上下文-更新状态: %w", err)
	}

	// 稳定 session_id 为键，更新路由与缓存
	poolKey := connectionKey(session.AgentType, cwd)
	s.mu.Lock()
	s.sessionPoolKey[sessionID] = poolKey
	if len(configOptions) > 0 {
		s.configs[sessionID] = configOptions
	}
	if len(modes) > 0 {
		s.modes[sessionID] = modes
	}
	s.mu.Unlock()
	if oldAgentSID != "" {
		s.debugUnregister(oldAgentSID)
	}
	s.debugRegister(newAgentSID, session.ID)
	s.debugLog(session.ID, "clear_context", newAgentSID, map[string]any{
		"agent": session.AgentType, "cwd": cwd,
	})
	s.rebindACPSessionYolo(oldAgentSID, newAgentSID, session.Yolo)

	// 追加一条 used=0 的 usage_update，使前端上下文占用立即归零（保留原窗口大小以维持展示）。
	// 仅需最近一次 usage_update 的 size，无需全量加载历史。
	lastUsage, _ := s.messages.FindLastByKind(session.SessionID, models.MessageKindUsageUpdate)
	seq := s.getNextSequence(session.SessionID) + 1
	resetUpdate := acp.SessionUpdate{
		UsageUpdate: &acp.SessionUsageUpdate{
			SessionUpdate: "usage_update",
			Size:          contextSizeFromMessage(lastUsage),
			Used:          0,
		},
	}
	resetMsg := MapUpdate(sessionID, session.ID, seq, resetUpdate)
	if err := s.messages.Create(&resetMsg); err != nil {
		slog.Warn("清理上下文-写入 usage_update 失败", "session", sessionID, "err", err)
	}

	// 返回更新后的 session
	return s.sessions.FindByID(session.ID)
}

// contextSizeFromMessage 从单条 usage_update 消息中解析上下文窗口大小（size），无则返回 0。
// 替代旧的 lastContextSize（曾遍历整个历史），现在配合 FindLastByKind 只加载最新一条。
func contextSizeFromMessage(m *models.Message) int {
	if m == nil || m.RawJSON == "" {
		return 0
	}
	var u struct {
		Size int `json:"size"`
	}
	if err := json.Unmarshal([]byte(m.RawJSON), &u); err == nil && u.Size > 0 {
		return u.Size
	}
	return 0
}

// formatHistory 将历史消息格式化为对话上下文文本，最多取最近 50 条。
func formatHistory(messages []models.Message) string {
	if len(messages) == 0 {
		return ""
	}

	// 最多取最近 50 条
	const limit = 50
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}

	var sb strings.Builder
	sb.WriteString("以下是之前对话的历史记录，请基于这些上下文继续对话：\n\n")
	for _, m := range messages {
		switch m.Role {
		case models.MessageRoleUser:
			sb.WriteString("[User]: " + m.Content + "\n")
		case models.MessageRoleAssistant:
			sb.WriteString("[Assistant]: " + m.Content + "\n")
		case models.MessageRoleTool:
			sb.WriteString("[Tool]: " + m.Content + "\n")
		}
	}
	return sb.String()
}

// extractTitle 从用户 prompt 中提取会话标题。
// 取首行非空文本的前 30 个字符（rune），去除首尾空白和命令前缀。
const maxTitleLen = 30

func extractTitle(prompt string) string {
	// 去除首尾空白
	s := strings.TrimSpace(prompt)
	if s == "" {
		return ""
	}
	// 取首行（多行 prompt 只用第一行）
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	// 去除常见的 slash 命令前缀（如 /help、/plan 等）
	if strings.HasPrefix(s, "/") {
		// 跳过命令部分，取命令后的文本
		if idx := strings.IndexByte(s, ' '); idx >= 0 {
			s = strings.TrimSpace(s[idx+1:])
		}
	}
	if s == "" {
		return ""
	}
	// 按 rune 截断，避免截断多字节字符
	runes := []rune(s)
	if len(runes) > maxTitleLen {
		runes = runes[:maxTitleLen]
		return string(runes) + "..."
	}
	return string(runes)
}

// AgentStatus 描述单个 agent 类型的连接状态，供前端侧边栏展示。
type AgentStatus struct {
	AgentType   string `json:"agent_type"`
	Status      string `json:"status"` // "connected" | "connecting" | "disconnected"
	ActiveCount int    `json:"active_count"`
}

// AgentACPInfo 描述某 agent 类型最近一次 ACP 握手的能力信息，供设置页展示。
type AgentACPInfo struct {
	// InitResp 是 agent 在 initialize 握手中返回的完整响应。
	InitResp acp.InitializeResponse
	// ClientCaps 是本服务握手时向 agent 声明的 client 能力。
	ClientCaps acp.ClientCapabilities
}

// AgentACPInfo 返回指定 agent 类型最近一次握手的能力信息。
// 若该 agent 类型从未完成过握手则返回 ok=false。
func (s *Service) AgentACPInfo(agentType string) (AgentACPInfo, bool) {
	s.mu.RLock()
	initResp, ok := s.agentInitInfo[agentType]
	s.mu.RUnlock()
	if !ok {
		return AgentACPInfo{}, false
	}
	return AgentACPInfo{
		InitResp:   initResp,
		ClientCaps: clientCapabilities(s.terminalEnabled),
	}, true
}

// AuthTerminalSpec 描述 terminal 类型认证方式对应的交互式登录进程启动参数：
// 用 agent 二进制 + 认证方式声明的 args/env 拉起登录 TUI（如 OAuth 流程）。
type AuthTerminalSpec struct {
	Command    string
	Args       []string
	Env        []string
	MethodName string
}

// AgentAuthTerminalSpec 根据最近一次握手声明的 terminal 类型认证方式，
// 构造交互式登录进程的启动参数。methodID 为空时取第一个 terminal 类型认证方式。
// 按 ACP unstable 约定，登录进程 = agent 二进制 + 认证方式声明的 args（不包含 ACP 服务参数）。
func (s *Service) AgentAuthTerminalSpec(agentType, methodID string) (AuthTerminalSpec, error) {
	b, err := s.GetBackend(agentType)
	if err != nil {
		return AuthTerminalSpec{}, err
	}
	s.mu.RLock()
	initResp, ok := s.agentInitInfo[agentType]
	s.mu.RUnlock()
	if !ok {
		return AuthTerminalSpec{}, fmt.Errorf("agent %s 尚未完成 ACP 握手，无法获取认证方式", agentType)
	}
	for _, m := range initResp.AuthMethods {
		term := m.Terminal
		if term == nil || (methodID != "" && term.Id != methodID) {
			continue
		}
		// 环境变量：继承服务进程环境 + 后端声明（API key 等）+ 认证方式额外声明
		env := append(os.Environ(), b.Env()...)
		env = append(env, "TERM=xterm-256color")
		for k, v := range term.Env {
			env = append(env, fmt.Sprintf("%s=%v", k, v))
		}
		return AuthTerminalSpec{
			Command:    b.Command(),
			Args:       append([]string{}, term.Args...),
			Env:        env,
			MethodName: term.Name,
		}, nil
	}
	return AuthTerminalSpec{}, fmt.Errorf("agent %s 未声明 terminal 类型认证方式", agentType)
}

// ListAgentStatus 返回所有已注册后端的连接状态与活跃会话数。
// status=connected 表示该 agent 类型至少有一条 ACP 连接已建立且进程存活。
func (s *Service) ListAgentStatus() []AgentStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	counts := make(map[string]int, len(s.backends))
	for _, poolKey := range s.sessionPoolKey {
		agentType, _ := splitConnectionKey(poolKey)
		counts[agentType]++
	}

	agentState := make(map[string]string, len(s.backends))
	for poolKey, state := range s.states {
		agentType, _ := splitConnectionKey(poolKey)
		if state == connStateConnected {
			if conn, ok := s.pool[poolKey]; ok {
				select {
				case <-conn.Done():
					continue
				default:
					agentState[agentType] = connStateConnected
				}
			}
			continue
		}
		if state == connStateConnecting && agentState[agentType] != connStateConnected {
			agentState[agentType] = connStateConnecting
		}
	}

	out := make([]AgentStatus, 0, len(s.backends))
	// s.backends 是 map，遍历顺序随机。按 agent_type 排序保证返回顺序稳定，
	// 避免前端侧边栏的 agent 状态列表顺序每次刷新都变化。
	names := make([]string, 0, len(s.backends))
	for name := range s.backends {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		state := agentState[name]
		if state == "" {
			state = connStateDisconnected
		}
		out = append(out, AgentStatus{
			AgentType:   name,
			Status:      state,
			ActiveCount: counts[name],
		})
	}
	return out
}

// applyModelValue 在指定 configOptions 中查找 category=model 的 select option，
// 若找到且 modelValue 在可选项中，则通过连接设置该值。
// sessionID 为稳定内部 ID（查连接路由），agentSID 为 ACP sessionId。
func (s *Service) applyModelValue(ctx context.Context, sessionID, agentSID string, opts []acp.SessionConfigOption, modelValue string) error {
	for _, opt := range opts {
		if opt.Select == nil || opt.Select.Category == nil {
			continue
		}
		if string(*opt.Select.Category) != "model" {
			continue
		}
		// 校验 modelValue 是否在可选项中
		valid := false
		if opt.Select.Options.Ungrouped != nil {
			for _, o := range *opt.Select.Options.Ungrouped {
				if string(o.Value) == modelValue {
					valid = true
					break
				}
			}
		}
		if !valid && opt.Select.Options.Grouped != nil {
			for _, g := range *opt.Select.Options.Grouped {
				for _, o := range g.Options {
					if string(o.Value) == modelValue {
						valid = true
						break
					}
				}
				if valid {
					break
				}
			}
		}
		if !valid {
			return fmt.Errorf("模型值 %s 不在可用列表中", modelValue)
		}
		conn, ok := s.connForSession(sessionID)
		if !ok {
			return ErrSessionNotActive
		}
		return conn.SetConfigOption(ctx, agentSID, string(opt.Select.Id), modelValue)
	}
	return nil // 该 agent 无 model config option，静默跳过
}

// logWarn 统一警告日志输出。
func (s *Service) logWarn(msg, agent string) {
	slog.Warn(msg, "agent", agent)
}

// backendCommandSafe 返回指定 agent 后端的命令字符串，仅用于日志展示。
// 后端不存在或读取失败时返回空串，绝不返回 error，避免污染调用方日志。
func (s *Service) backendCommandSafe(agentType string) string {
	b, err := s.GetBackend(agentType)
	if err != nil {
		return ""
	}
	return b.Command()
}

// Workspace delegation methods

func (s *Service) GetWorkspaceCwd(workspaceID uint) (string, error) {
	ws, err := s.workspaces.FindByID(workspaceID)
	if err != nil {
		return "", err
	}
	return ws.Cwd, nil
}

func (s *Service) CreateWorkspace(ws *models.Workspace) error {
	return s.workspaces.Create(ws)
}

func (s *Service) FindWorkspaceByID(id uint) (*models.Workspace, error) {
	return s.workspaces.FindByID(id)
}

func (s *Service) FindWorkspacesByUserID(userID uint) ([]models.Workspace, error) {
	return s.workspaces.FindByUserID(userID)
}

func (s *Service) FindWorkspaceByUserIDAndCwd(userID uint, cwd string) (*models.Workspace, error) {
	return s.workspaces.FindByUserIDAndCwd(userID, cwd)
}

func (s *Service) FindDefaultWorkspaceByUserID(userID uint) (*models.Workspace, error) {
	return s.workspaces.FindDefaultByUserID(userID)
}

func (s *Service) UpdateWorkspace(id uint, updates map[string]interface{}) error {
	return s.workspaces.Update(id, updates)
}

func (s *Service) DeleteWorkspace(id uint) error {
	ws, err := s.workspaces.FindByID(id)
	if err != nil {
		return err
	}
	if err := s.workspaces.Delete(id); err != nil {
		return err
	}
	return (&Workspace{Mode: ws.Mode, Cwd: ws.Cwd, TempDir: ws.TempDir}).Cleanup()
}

func (s *Service) WorkspaceSessionCount(workspaceID uint) (int64, error) {
	return s.workspaces.SessionCount(workspaceID)
}

func (s *Service) FindSessionsByWorkspaceID(workspaceID uint) ([]models.Session, error) {
	return s.sessions.FindByWorkspaceID(workspaceID)
}

// DeleteSessionWithMessages 用于工作区删除等批量场景：删除会话消息与记录。
// 必须复用 DeleteSession 的连接/map 清理逻辑，否则每次调用会泄漏：
//   - sessionPoolKey/commands/configs/modes 各 1 条 map 条目
//   - 该会话占用的共享 Connection（若为最后一个会话则子进程永久残留）
//   - 残留的 activePrompts 广播器
//
// 调用方（workspace_handler 删除循环）无 ctx，故使用 context.Background()。
func (s *Service) DeleteSessionWithMessages(session *models.Session) error {
	s.debugUnregister(agentSessionID(session))
	s.bindACPSessionYolo(agentSessionID(session), false)
	s.detachAndReleaseConn(context.Background(), session)
	if err := s.messages.DeleteBySessionID(session.SessionID); err != nil {
		return err
	}
	if err := s.sessions.Delete(session.ID); err != nil {
		return err
	}
	s.debugCleanup(session.ID)
	s.maybeCleanupOrphanTemporaryWorkspace(session.WorkspaceID)
	return nil
}
