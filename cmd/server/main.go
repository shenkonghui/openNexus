package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/acp"
	"opennexus/internal/agent"
	"opennexus/internal/config"
	"opennexus/internal/database"
	"opennexus/internal/handlers"
	"opennexus/internal/logging"
	gatewaymcp "opennexus/internal/mcp/gateway"
	notesmcp "opennexus/internal/mcp/notes"
	orchestrationmcp "opennexus/internal/mcp/orchestration"
	"opennexus/internal/models"
	"opennexus/internal/repository"
	"opennexus/internal/router"
	"opennexus/internal/services"
	"opennexus/internal/sysutil"
)

// ldflags 注入
var version = "dev"

func stopWithTimeout(name string, timeout time.Duration, stop func()) {
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("%s 退出超时，继续关闭", name)
	}
}

// ensureBuiltinMCPToken 保证存在一个全局 MCP token：内置 MCP server（notes/subagent/
// orchestration）依赖它生成 Authorization 头并写入 ~/.agents/mcp.json。启动同步
// （SyncAllNotesMCP / SyncAllSubagentMCP）采用全局共享 token 策略，若一个 token 都没有
// 则直接跳过写入，导致新安装时内置 MCP 不会出现在 mcp.json。此处在首次启动
// （尚无任何 token）时为 admin 用户自动生成一个，使内置 MCP 开箱即用。
func ensureBuiltinMCPToken(userRepo *repository.UserRepository, settingsRepo *repository.NoteSettingsRepository) {
	list, err := settingsRepo.FindAllWithMcpToken()
	if err != nil {
		log.Printf("检查 MCP token 失败（跳过自动生成）: %v", err)
		return
	}
	if len(list) > 0 {
		return // 已有 token，复用全局共享 token
	}
	admin, err := userRepo.FindByUsername("admin")
	if err != nil || admin == nil {
		log.Printf("未找到 admin 用户，跳过 MCP token 自动生成: %v", err)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Printf("生成 MCP token 失败: %v", err)
		return
	}
	if err := settingsRepo.SetMCPTokenOnce(admin.ID, hex.EncodeToString(b)); err != nil {
		log.Printf("保存自动生成的 MCP token 失败: %v", err)
		return
	}
	log.Printf("已为内置 MCP server 自动生成全局 MCP token（admin 用户）")
}

func main() {
	// 子命令分发：`opennexus watchdog ...` 作为独立守护进程运行（与主 server 解耦）。
	// watchdog 由主 server 用 exec.Command + Setsid 拉起，负责在主程序退出/崩溃后清理 acp 进程，
	// 以及回收空闲超过阈值的 acp 连接。必须在 flag.Parse 之前拦截，避免与 server flag 冲突。
	if len(os.Args) > 1 && os.Args[1] == "watchdog" {
		runWatchdog()
		return
	}

	// `opennexus mcp-bridge`：stdio MCP 桥子进程，把聚合网关桥接给
	// 不支持 http mcpCapabilities 的 ACP agent（如 devin）。同样须在 flag.Parse 前拦截。
	if len(os.Args) > 1 && os.Args[1] == "mcp-bridge" {
		runMCPBridge()
		return
	}

	// 启动早期扩充 PATH：GUI/launchd 启动的进程 PATH 不含 nvm、Homebrew 等目录，
	// 会导致通过 npm exec 启动的 agent 子进程找不到 npm/node。
	// 必须早于任何 agent 进程拉起（含下方的 PreconnectAllAsync）。
	sysutil.EnrichPath()

	// 命令行参数（桌面客户端模式）
	openBrowser := flag.Bool("open", false, "启动后自动打开浏览器")
	dataDir := flag.String("data-dir", "", "数据目录（覆盖 config.yaml 中的 database.path 和 workspace）")
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.Parse()

	if *showVersion {
		fmt.Printf("openNexus %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	// 启动时一次性迁移改名前的历史数据目录（~/.nextAgent / ~/.nexusagent → ~/.openNexus）。
	// 必须在 ResolveConfigPath 之前执行：后者会检查 ~/.openNexus/config.yaml 是否存在。
	// 幂等：已迁移过则无操作；失败仅警告不影响启动。SKIP_DATA_MIGRATION=1 可禁用。
	if moved, err := config.MigrateLegacyDataDir(); err != nil {
		log.Printf("数据目录迁移失败（不影响启动）: %v", err)
	} else if moved > 0 {
		log.Printf("已迁移历史数据目录到 ~/.openNexus（%d 项）", moved)
	}

	cfgPath := config.ResolveConfigPath()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("配置校验失败: %v", err)
	}
	logging.Setup(cfg.Logging.Level)
	if cfg.Auth.AutoLogin {
		log.Printf("auth.auto_login 已启用：前端将自动以 admin 身份登录")
	}

	// --data-dir 覆盖数据库路径与会话工作区
	if *dataDir != "" {
		cfg.Database.Path = filepath.Join(*dataDir, "opennexus.db")
		cfg.Agents.Workspace.SessionDir = filepath.Join(*dataDir, "session")
	}

	if cfg.Database.Path != ":memory:" {
		dir := filepath.Dir(cfg.Database.Path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("创建数据库目录失败: %v", err)
		}
	}

	db, err := database.Connect(cfg.Database.Path)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}

	jwtSvc := services.NewJWTService(cfg.JWT.Secret, cfg.JWT.AccessTTL, cfg.JWT.RefreshTTL)
	authSvc := services.NewAuthService(db, jwtSvc, cfg.Password.BcryptCost)
	authSvc.SeedAdminUser()

	// P1: ACP 服务
	messagesDir := filepath.Join(filepath.Dir(cfg.Database.Path), "messages")
	if cfg.Database.Path == ":memory:" {
		messagesDir = filepath.Join(os.TempDir(), "opennexus-messages")
	}
	acpSvc := acp.NewService(db, messagesDir, cfg.Agents.Workspace, cfg.Agents.Skills, cfg.Agents.Commands, cfg.Agents.Rules, cfg.Agents.SubAgents)
	acpSvc.SetDebugConfig(cfg.Debug)
	// 注入 acp_connections 心跳表仓库：主 server 写入连接 PID/活动时间/心跳，
	// 供独立 watchdog 进程读取判定空闲与主程序存活。
	acpConnRepo := repository.NewACPConnectionRepository(db)
	acpSvc.SetACPConnectionRepo(acpConnRepo)
	// 注入单轮 prompt 最大存活时间：prompt 的 ctx 与 HTTP/SSE 请求解耦，
	// 此超时兜底防 agent 卡死导致 goroutine 永久泄漏（超时标记 interrupted）。
	acpSvc.SetPromptMaxDuration(cfg.Agents.PromptMaxDuration)
	// 失败任务自动重试一次：agent 运行中崩溃时 ResumeSession（断连则重连）并重发 prompt。
	acpSvc.SetFailedTaskAutoRetryOnce(cfg.Agents.FailedTaskAutoRetryOnceEnabled())
	// 全局权限规则（yolo/白名单/黑名单）来自 config.yaml 的 permissions 段。
	// 启动时立即下发到 service（须在 PreconnectAllAsync 前，使新连接建连即拿到规则）。
	acpSvc.ApplyPermissions(cfg.Permissions.Mode, cfg.Permissions.Allow, cfg.Permissions.Ask, cfg.Permissions.Deny)

	// P2: Agent 注册表与路由
	agentRegistry := agent.NewRegistry()
	agentCfgRepo := repository.NewAgentConfigRepository(db)
	registrar := agent.NewRegistrar(agentRegistry, acpSvc)

	// 1. 默认仅启用 Claude Code，其余 agent 从 registry 同步但默认禁用
	seedDefaultClaudeCodeAgent(agentCfgRepo, cfg.Agents.ClaudeCode)
	loadRegistryAgents(agentCfgRepo)

	// 恢复 binary agent 的 symlink：读 versions.json，把每个 agent 的稳定路径
	// 重新指向用户上次通过"更新"选定的版本。这是重启不回退的关键——
	// 内存 BinaryRegistry 从内嵌 registry 重填的是旧 version，但 symlink 指向激活版本。
	if restored, err := acp.RestoreBinarySymlinks(); err != nil {
		log.Printf("恢复 binary symlink 失败（不影响启动）: %v", err)
	} else if restored > 0 {
		log.Printf("已恢复 %d 个 binary agent 的 symlink", restored)
	}

	// 2. 从数据库加载用户启用的 agent 配置
	loadDBAgentConfigs(agentCfgRepo, registrar)

	agentRouter := agent.NewRouter(agentRegistry, acpSvc)
	agentCfgH := handlers.NewAgentConfigHandler(agentCfgRepo, registrar)
	registryH := handlers.NewRegistryHandler(agentCfgRepo)

	// 恢复上次服务重启中断的状态：将 running 状态的 running_task 标记为 interrupted，
	// 将 active 状态的会话标记为 error（agent 进程已随上次退出终止）。
	// 用户可查看中断任务并手动重发。
	acpSvc.RecoverActiveSessions()

	// 启动兜底清理：扫杀上次崩溃/异常退出残留的 acp 孤儿进程，并清空心跳表脏行。
	// 正常退出路径（shutdown）已由 KillOrphanACPProcesses 覆盖，此处覆盖崩溃场景：
	// 主 server 被 SIGKILL 或 panic 退出时，shutdown 逻辑不会执行，残留的 agent 进程
	// 由独立进程组存活（Setsid），若 watchdog 也一并死亡则无人清理。此处在新连接建立前扫杀，
	// 既清除孤儿又避免误杀即将建立的新连接（此时 pool 尚空）。
	if n, err := acp.KillOrphanACPProcesses(); err != nil {
		log.Printf("启动清理 acp 孤儿进程失败: %v", err)
	} else if n > 0 {
		log.Printf("启动清理：已扫杀 %d 个残留 acp 孤儿进程", n)
	}
	if err := acpConnRepo.DeleteAll(); err != nil {
		log.Printf("启动清理心跳表脏行失败: %v", err)
	}

	// 启动健康检查与自动重连 goroutine（定期检查连接状态，断开自动重连）。
	// 同时启动心跳续约 goroutine，向 acp_connections 表续约，供 watchdog 判活。
	acpSvc.StartHealthCheck()
	// 异步为所有已注册 agent 预建立共享 ACP 连接（每类 agent 一个常驻进程）。
	// 每个 agent 独立 goroutine 连接，不阻塞服务启动；连接失败由健康检查自动重连。
	acpSvc.PreconnectAllAsync()

	// 拉起独立 watchdog 进程：回收空闲 acp 连接，并在主程序退出/崩溃后清理全部 acp 进程。
	// watchdog 以 Setsid 独立进程组运行，与主 server 解耦——主 server 崩溃后它仍存活完成清理。
	// 若已有 watchdog 在运行（上次启动残留），先杀旧再起新（满足「已运行则重启一次」）。
	spawnWatchdog(cfg.Database.Path)

	// P7: 定时任务调度器
	schedTaskRepo := repository.NewScheduledTaskRepository(db)
	execRepo := repository.NewTaskExecutionRepository(db)
	schedulerSvc := services.NewSchedulerService(schedTaskRepo, execRepo, agentRouter)
	if err := schedulerSvc.Start(); err != nil {
		log.Fatalf("启动定时任务调度器失败: %v", err)
	}
	schedTaskH := handlers.NewScheduledTaskHandler(schedTaskRepo, execRepo, schedulerSvc, agentRouter, agentRouter)

	// 任务编排：基于工作区 cwd 下的 tasks.json 定义任务，按并发上限调度，
	// 每个任务用 git worktree 隔离工作目录，复用 RunSessionTask 创建持久会话执行。
	orchestratorSvc := services.NewOrchestratorService(agentRouter)
	// 服务重启后将各工作区 tasks.json 中残留的 running/queued 标为 interrupt，
	// 否则「全部启动」会因误判仍在运行而跳过这些任务。
	if cwds, err := repository.NewWorkspaceRepository(db).ListCwds(); err != nil {
		log.Printf("编排任务恢复：列举工作区 cwd 失败: %v", err)
	} else {
		orchestratorSvc.RecoverAll(cwds)
	}
	orchH := handlers.NewOrchestrationHandler(orchestratorSvc, agentRouter)

	noteRepo := repository.NewNoteRepository(db)
	noteSettingsRepo := repository.NewNoteSettingsRepository(db)
	noteClassifier := services.NewNoteClassifier(noteSettingsRepo, noteRepo, agentRouter)
	noteClassifyWorker := services.NewNoteClassifyWorker(noteClassifier)
	noteClassifyWorker.Start()
	publicBase := strings.TrimRight(strings.TrimSpace(cfg.Server.PublicBaseURL), "/")
	if publicBase == "" {
		publicBase = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	}
	noteH := handlers.NewNoteHandler(noteRepo, noteSettingsRepo, noteClassifier, cfg.Agents.MCP.ConfigPath, publicBase)
	// 保证存在全局 MCP token：内置 MCP server（notes/subagent/orchestration）依赖它才会
	// 被写入 ~/.agents/mcp.json。首次启动尚无任何 token 时自动生成，使内置 MCP 开箱即用，
	// 无需手动到设置页点"生成 MCP Token"。
	ensureBuiltinMCPToken(repository.NewUserRepository(db), noteSettingsRepo)
	// 启动时把已生成 token 的笔记 MCP 同步到全局 mcp.json（为存量 token 补写配置）。
	noteH.SyncAllNotesMCP()
	acpSvc.SetNotesMCP(noteSettingsRepo, publicBase)
	acpSvc.SetMCPConfigPath(cfg.Agents.MCP.ConfigPath)

	// 任务元数据：自动打标签 + AI 标题生成（异步，fire-and-forget）
	taskSettingsRepo := repository.NewTaskSettingsRepository(db)
	sessionRepo := repository.NewSessionRepository(db)
	taskMetaSvc := services.NewTaskMetaService(taskSettingsRepo, sessionRepo, agentRouter)
	acpSvc.SetTaskMetaTrigger(taskMetaSvc)
	taskSettingsH := handlers.NewTaskSettingsHandler(taskSettingsRepo)
	agentPrefsH := handlers.NewAgentPrefsHandler(repository.NewUserAgentPrefsRepository(db))

	// 全局权限规则（yolo / 白名单 / 黑名单）：配置来自 config.yaml，设置页保存时写回文件并热更新到所有连接
	permSettingsH := handlers.NewPermissionSettingsHandler(cfgPath, agentRouter)

	configH := handlers.NewConfigHandler(cfgPath, acpSvc)

	// MCP 聚合网关：把全局 mcp.json 里的 http/sse 上游汇聚成单一 endpoint。
	// 很多 ACP agent 不实现 session/new 的 mcpServers 参数，只能在其原生配置里手工配置；
	// 有了网关，这种手工配置只需做一次，之后增删 MCP server 不必再改 agent 配置。
	// 默认不主动启用（启用会改变所有会话的 MCP 注入方式），由设置页显式开启；
	// 这里只做启动自愈：条目已存在时刷新其 url 与 token。
	mcpGateway, err := gatewaymcp.New(cfg.Agents.MCP.ConfigPath, gatewaymcp.NewDBAuthenticator(noteSettingsRepo), "")
	if err != nil {
		log.Fatalf("创建 MCP 网关失败: %v", err)
	}
	mcpGateway.SetPublicBaseURL(publicBase)
	mcpGateway.SyncEntry()
	defer mcpGateway.Close()

	// 默认启用网关：把网关 endpoint + 共享 token 注入 acpSvc，
	// 此后所有会话的 MCP 注入自动走网关（http/sse 上游收敛），无需用户手动启用。
	// 尚无任何 MCP Token 时 SharedToken 返回空，acpSvc 退回原行为（不注入网关）。
	if gwToken := mcpGateway.SharedToken(); gwToken != "" {
		acpSvc.SetGatewayEndpoint(mcpGateway.Endpoint(), gwToken)
	}

	mcpH := handlers.NewMCPHandler(cfg.Agents.MCP.ConfigPath, mcpGateway)

	// 日志查看器：复用 logging 包在 Setup 时初始化的日志中心单例，
	// 通过 SSE 把后端 slog 日志实时推送给前端。
	logH := handlers.NewLogHandler(logging.DefaultHub())
	debugH := handlers.NewDebugHandler(agentRouter, acpSvc.Debugger())

	// 内置 MCP（opennexus-task）条目同步自愈（复用笔记 token 体系）。
	agentPrefsRepo := repository.NewUserAgentPrefsRepository(db)
	subAgentH := handlers.NewSubAgentHandler(noteSettingsRepo, cfg.Agents.MCP.ConfigPath, publicBase)
	subAgentH.SyncAllSubagentMCP()

	engine := router.Setup(authSvc, jwtSvc, agentRouter, agentCfgH, registryH, schedTaskH, noteH, taskSettingsH, agentPrefsH, configH, mcpH, logH, debugH, subAgentH, orchH, permSettingsH, orchestratorSvc, cfg.Agents.Skills, cfg.Agents.Commands, cfg.Agents.Rules, cfg.Agents.SubAgents, cfg.Server.Mode, cfg.Server.WebDist, cfg.Auth.AutoLogin)
	engine.Any("/mcp/notes", gin.WrapH(notesmcp.Handler(noteRepo, noteSettingsRepo)))
	engine.Any("/mcp/notes/*path", gin.WrapH(notesmcp.Handler(noteRepo, noteSettingsRepo)))
	// orchestration MCP server：主 agent 通过 MCP 工具管理工作区任务编排（tasks.json）。
	// agentRouter 作为 WorkspaceResolver，orchestratorSvc 执行增删改与启停。
	engine.Any("/mcp/orchestration", gin.WrapH(orchestrationmcp.Handler(noteSettingsRepo, agentPrefsRepo, agentRouter, orchestratorSvc)))
	engine.Any("/mcp/orchestration/*path", gin.WrapH(orchestrationmcp.Handler(noteSettingsRepo, agentPrefsRepo, agentRouter, orchestratorSvc)))
	// 聚合网关的 MCP endpoint 本身（实例在上面创建，两条路由共享以复用上游连接池与工具缓存）。
	engine.Any(gatewaymcp.GatewayPath, gin.WrapH(mcpGateway.Handler()))
	engine.Any(gatewaymcp.GatewayPath+"/*path", gin.WrapH(mcpGateway.Handler()))

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: engine}
	go func() {
		if cfg.Server.Mode == "release" {
			log.Printf("openNexus %s 启动于 %s（单端口模式，前端 + API）", version, addr)
		} else {
			log.Printf("openNexus API 启动于 %s（开发模式，前端请访问 vite dev server）", addr)
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务器启动失败: %v", err)
		}
	}()

	// 桌面模式：自动打开浏览器
	if *openBrowser {
		go openBrowserAfterDelay(fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port))
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("服务正在关闭...")

	go func() {
		time.Sleep(5 * time.Second)
		log.Println("关闭超时，强制退出...")
		os.Exit(1)
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP 服务关闭超时: %v", err)
	}

	acpSvc.StopHealthCheck()
	noteClassifyWorker.Stop()
	stopWithTimeout("定时任务调度器", 3*time.Second, schedulerSvc.Stop)
	// 兜底清理：扫杀可能残留的 acp 孤儿进程（正常退出已由 StopHealthCheck 关闭内存连接，
	// 此处覆盖崩溃恢复遗留或未被 pool 跟踪的进程）。
	if n, err := acp.KillOrphanACPProcesses(); err != nil {
		log.Printf("清理 acp 孤儿进程失败: %v", err)
	} else if n > 0 {
		log.Printf("已清理 %d 个 acp 孤儿进程", n)
	}
	os.Exit(0)
}

// watchdogPIDFile 返回 watchdog 进程 PID 文件路径（与 DB 同目录）。
func watchdogPIDFile(dbPath string) string {
	return dbPath + ".watchdog.pid"
}

// spawnWatchdog 拉起独立 watchdog 进程。
// 若已有 watchdog 在运行（PID 文件存在且进程存活），先杀旧再起新。
// watchdog 以独立进程组（Setpgid）运行，与主 server 生命周期解耦：
// 主 server 正常退出时由退出逻辑兜底清理 acp 进程；
// 主 server 崩溃时 watchdog 仍存活，通过心跳超时检测主程序死亡并完成清理。
func spawnWatchdog(dbPath string) {
	pidFile := watchdogPIDFile(dbPath)

	// 若已有 watchdog 在运行，终止旧实例（满足「已运行则重启一次」）
	if old := readAlivePID(pidFile); old > 0 {
		log.Printf("检测到已有 watchdog 进程 (pid=%d)，先终止旧实例", old)
		killProcessByPID(old)
		removePIDFile(pidFile)
		// 给旧进程退出留点时间
		time.Sleep(300 * time.Millisecond)
	}

	exe, err := os.Executable()
	if err != nil {
		log.Printf("获取可执行文件路径失败，跳过 watchdog: %v", err)
		return
	}
	cmd := exec.Command(exe, "watchdog", "--db", dbPath)
	applyWatchdogDetach(cmd) // 独立进程组，脱离父进程
	// 重定向 stdin/stdout/stderr 到/dev/null 或日志文件，避免 watchdog 依赖主 server 的标准流
	if devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	if err := cmd.Start(); err != nil {
		log.Printf("启动 watchdog 失败（不影响主服务）: %v", err)
		return
	}
	// 写入 PID 文件供下次启动检测；释放句柄，让 watchdog 由 init/launchd 接管
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0o644); err != nil {
		log.Printf("写 watchdog PID 文件失败: %v", err)
	}
	_ = cmd.Process.Release()
	log.Printf("已启动 watchdog 进程 (pid=%d)", cmd.Process.Pid)
}

// readAlivePID 读取 PID 文件，若进程已不存在则返回 0 并删除失效文件。
func readAlivePID(pidFile string) int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid := 0
	for _, b := range data {
		if b >= '0' && b <= '9' {
			pid = pid*10 + int(b-'0')
		}
	}
	if pid <= 0 {
		return 0
	}
	if !processAliveByPID(pid) {
		removePIDFile(pidFile)
		return 0
	}
	return pid
}

func removePIDFile(pidFile string) {
	_ = os.Remove(pidFile)
}

// openBrowserAfterDelay 延迟后用系统默认浏览器打开 URL（开发模式快速预览）。
// 生产桌面客户端使用 Pake (Tauri) 打包，不依赖此函数。
func openBrowserAfterDelay(url string) {
	time.Sleep(800 * time.Millisecond)
	log.Printf("打开浏览器: %s", url)
	openDefaultBrowser(url)
}

// openDefaultBrowser 用系统默认浏览器打开 URL。
func openDefaultBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return
	}
	_ = cmd.Start()
}

const defaultClaudeCodeType = "claude-code"

// seedDefaultClaudeCodeAgent 首次启动时写入默认启用的 Claude Code 配置。
func seedDefaultClaudeCodeAgent(repo *repository.AgentConfigRepository, cc config.ClaudeCodeConfig) {
	if _, err := repo.FindByType(defaultClaudeCodeType); err == nil {
		return
	}
	argsJSON := ""
	if len(cc.Args) > 0 {
		b, err := json.Marshal(cc.Args)
		if err != nil {
			log.Printf("编码 Claude Code args 失败: %v", err)
			return
		}
		argsJSON = string(b)
	}
	enabled := true
	cfg := &models.AgentConfig{
		Type:        defaultClaudeCodeType,
		DisplayName: "Claude Code",
		Description: "Anthropic Claude Code via ACP",
		Command:     cc.Command,
		Args:        argsJSON,
		APIKeyEnv:   cc.APIKeyEnv,
		Timeout:     cc.Timeout.String(),
		Enabled:     &enabled,
	}
	if err := repo.Create(cfg); err != nil {
		log.Printf("写入默认 Claude Code agent 失败: %v", err)
	}
}

// loadRegistryAgents 加载 ACP registry 并合并写入数据库。
// 启动时默认先从 CDN 拉取最新 registry（更新一次）；网络失败则回退到内嵌的 registry.json。
// 合并语义：新 agent 以 enabled=false 创建；已有 agent 仅刷新名称/描述，保留用户修改。
// 不在此处注册 backend——统一交由 loadDBAgentConfigs 根据 DB 中的 enabled 状态注册。
func loadRegistryAgents(repo *repository.AgentConfigRepository) {
	var agents []acp.RegistryAgent
	source := "cdn"
	if doc, err := acp.FetchRegistryFull(); err != nil {
		log.Printf("启动更新 registry 失败，回退内嵌 registry: %v", err)
		source = "embedded"
	} else {
		agents = doc.Agents
		log.Printf("已从 CDN 拉取最新 registry（version=%s）", doc.Version)
	}
	if len(agents) == 0 {
		embedded, err := acp.FetchEmbeddedRegistry()
		if err != nil {
			log.Printf("加载内嵌 registry 失败: %v", err)
			return
		}
		agents = embedded
		source = "embedded"
	}
	log.Printf("加载到 %d 个 agent（来源 %s）", len(agents), source)

	added, updated, _ := acp.SyncRegistryToStore(agents, repo)
	log.Printf("registry agent 已同步到数据库（新增 %d，更新 %d），等待用户在设置中启用", added, updated)
}

// loadDBAgentConfigs 加载数据库中启用的 agent 配置并注册到 registry 与 acp service。
func loadDBAgentConfigs(repo *repository.AgentConfigRepository, registrar *agent.Registrar) {
	list, err := repo.FindAllEnabled()
	if err != nil {
		log.Printf("加载数据库 agent 配置失败: %v", err)
		return
	}
	for i := range list {
		cfg := list[i]
		// 根据 agent 类型自动选择 ConfigBackend 或 BinaryBackend（后者延迟下载）
		backend := acp.NewBackendFromAgentConfig(cfg)
		registrar.ReplaceBackend(backend)
		_ = registrar.RegisterAgent(&agent.AgentDescriptor{
			Type:        cfg.Type,
			DisplayName: cfg.DisplayName,
			Description: cfg.Description,
			Backend:     backend,
		})
		log.Printf("已注册数据库 agent: %s", cfg.Type)
	}
}
