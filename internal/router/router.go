package router

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"opennexus/internal/agent"
	"opennexus/internal/config"
	"opennexus/internal/handlers"
	"opennexus/internal/middleware"
	"opennexus/internal/services"
)

func Setup(authSvc *services.AuthService, jwtSvc *services.JWTService, agentRouter *agent.Router, agentCfgH *handlers.AgentConfigHandler, registryH *handlers.RegistryHandler, schedTaskH *handlers.ScheduledTaskHandler, noteH *handlers.NoteHandler, taskSettingsH *handlers.TaskSettingsHandler, goalSettingsH *handlers.GoalSettingsHandler, agentPrefsH *handlers.AgentPrefsHandler, configH *handlers.ConfigHandler, mcpH *handlers.MCPHandler, logH *handlers.LogHandler, debugH *handlers.DebugHandler, subAgentH *handlers.SubAgentHandler, tmH *handlers.TaskManagerHandler, permSettingsH *handlers.PermissionSettingsHandler, toolCallH *handlers.ToolCallHandler, tmSvc *services.TaskManagerService, skillsCfg config.SkillsConfig, commandsCfg config.CommandsConfig, rulesCfg config.RulesConfig, subAgentsCfg config.SubAgentsConfig, selectorCfg config.SelectorConfig, mode, webDist string, autoLogin bool) *gin.Engine {
	gin.SetMode(mode)
	r := gin.New()
	r.Use(gin.Recovery())

	authHandler := handlers.NewAuthHandler(authSvc, autoLogin)
	fsHandler := handlers.NewFileSystemHandler(skillsCfg, commandsCfg, rulesCfg, subAgentsCfg)
	browserH := handlers.NewBrowserHandler()
	// 把 FileSystemHandler 注入 ConfigHandler，使软重载能同时刷新两处扫描目录副本。
	if configH != nil {
		configH.SetFileSystemHandler(fsHandler)
	}

	v1 := r.Group("/api/v1")
	{
		auth := v1.Group("/auth")
		{
			auth.POST("/register", authHandler.Register)
			auth.POST("/login", authHandler.Login)
			auth.GET("/auto-login", authHandler.AutoLogin)
			auth.POST("/refresh", authHandler.Refresh)
			auth.POST("/logout", authHandler.Logout)
		}

		protected := v1.Group("")
		protected.Use(middleware.AuthRequired(jwtSvc))
		{
			protected.GET("/me", authHandler.Me)
			protected.PUT("/me", authHandler.UpdateProfile)
			protected.POST("/me/password", authHandler.ChangePassword)

			agentH := handlers.NewAgentHandler(agentRouter, agentRouter, agentRouter, agentRouter)
			agentH.SetSelectorFilters(selectorCfg.Filters)
			// 设置页保存 selector 过滤后热更新到 AgentHandler，无需重启
			if configH != nil {
				configH.SetSelectorFiltersApplier(agentH.SetSelectorFilters)
			}
			protected.GET("/agents", agentH.List)
			protected.GET("/agents/status", agentH.Status)
			// agent 类型最近一次 ACP 握手的能力信息（设置页能力展示）
			protected.GET("/agents/:type/capabilities", agentH.Capabilities)
			protected.GET("/agents/:type/models", agentH.Models)
			protected.GET("/agents/:type/commands", agentH.Commands)
			protected.GET("/agents/:type/modes", agentH.Modes)
			protected.POST("/agents/:type/probe", agentH.Probe)
			protected.POST("/agents/:type/preconnect", agentH.Preconnect)

			agentCfg := protected.Group("/agent-configs")
			{
				agentCfg.GET("", agentCfgH.List)
				agentCfg.POST("", agentCfgH.Create)
				agentCfg.PUT("/:id", agentCfgH.Update)
				agentCfg.DELETE("/:id", agentCfgH.Delete)
				// 取单个 agent 的 registry 默认 command/args（供前端"重置为默认"按钮预填表单）
				agentCfg.GET("/:id/registry-default", agentCfgH.GetRegistryDefault)
				// 从 CDN 最新 registry 同步单个 agent（含 binary 缓存清理触发重下）
				agentCfg.POST("/:id/update-from-registry", agentCfgH.UpdateFromRegistry)
				// 在线拉取最新 ACP registry 并合并到本地存储（对运行中后端零影响）
				if registryH != nil {
					agentCfg.POST("/registry/refresh", registryH.Refresh)
				}
			}

			sessionH := handlers.NewSessionHandler(agentRouter)
			// 注入任务管理服务：新建会话首次发送 prompt 时同步登记到 tasks.json，
			// 使"新建对话"与任务在 tasks.json 中统一可见。
			if tmSvc != nil {
				sessionH.SetTaskRegistrar(tmSvc)
			}
			protected.POST("/sessions", sessionH.Create)
			protected.GET("/sessions", sessionH.List)
			protected.GET("/sessions/running", sessionH.RunningSessions)
			protected.GET("/sessions/latest", sessionH.LatestByWorkspace)
			protected.GET("/sessions/:id", sessionH.Get)
			protected.PUT("/sessions/:id/title", sessionH.UpdateTitle)
			protected.PUT("/sessions/:id/yolo", sessionH.UpdateYolo)
			protected.DELETE("/sessions/:id", sessionH.Delete)
			protected.POST("/sessions/:id/prompt", sessionH.Prompt)
			protected.POST("/sessions/:id/cancel", sessionH.Cancel)
			protected.POST("/sessions/:id/resume", sessionH.Resume)
			protected.POST("/sessions/:id/clear-context", sessionH.ClearContext)
			protected.GET("/sessions/:id/messages", sessionH.Messages)
			protected.GET("/sessions/:id/executions", sessionH.Executions)
			protected.GET("/sessions/:id/commands", sessionH.Commands)
			protected.GET("/sessions/:id/modes", sessionH.Modes)
			protected.GET("/sessions/:id/skills", sessionH.Skills)
			protected.GET("/sessions/:id/config-options", sessionH.ConfigOptions)
			protected.POST("/sessions/:id/config-options", sessionH.SetConfigOption)
			protected.POST("/sessions/:id/mode", sessionH.SetMode)
			protected.POST("/sessions/:id/permissions/:requestId/respond", sessionH.RespondPermission)
			// 流续传与中断任务恢复
			protected.GET("/sessions/:id/stream", sessionH.Stream)
			protected.GET("/sessions/:id/interrupted-tasks", sessionH.InterruptedTasks)
			protected.POST("/running-tasks/:taskId/resume", sessionH.ResumeInterruptedTask)

			// Workspace 路由
			workspaceH := handlers.NewWorkspaceHandler(agentRouter)
			protected.POST("/workspaces", workspaceH.Create)
			protected.GET("/workspaces", workspaceH.List)
			protected.GET("/workspaces/:id", workspaceH.Get)
			protected.PUT("/workspaces/:id", workspaceH.Update)
			protected.DELETE("/workspaces/:id", workspaceH.Delete)
			protected.POST("/workspaces/:id/save", workspaceH.Save)
			// 拖拽文件上传(落盘到工作区管理数据目录的 uploads/,返回绝对路径供 @ 引用)
			protected.POST("/workspaces/:id/uploads", workspaceH.Upload)

			// 会话工作目录文件浏览与编辑（路径限制在 session cwd 内）
			sessionFileH := handlers.NewSessionFileHandler(agentRouter)
			protected.GET("/sessions/:id/files", sessionFileH.ListFiles)
			protected.GET("/sessions/:id/files/content", sessionFileH.ReadFile)
			protected.PUT("/sessions/:id/files/content", sessionFileH.WriteFile)
			protected.POST("/sessions/:id/files/undo", sessionFileH.UndoFileChanges)
			protected.POST("/sessions/:id/files/restore", sessionFileH.RestoreToCheckpoint)
			protected.GET("/sessions/:id/files/changes", sessionFileH.ListFileChanges)

			// ACP 调试数据（需 debug.acp.enabled）
			if debugH != nil {
				protected.GET("/sessions/:id/debug", debugH.Meta)
				protected.GET("/sessions/:id/debug/events", debugH.Events)
				protected.GET("/sessions/:id/debug/raw", debugH.Raw)
			}

			// 配置管理（config.yaml agents 块下的 skills/commands/rules 配置）
			configG := protected.Group("/config")
			{
				configG.GET("/agents", configH.GetAgentsConfig)
				configG.PUT("/agents", configH.UpdateAgentsConfig)
				// config.yaml 原生编辑：读取/校验/保存全文（保存前强制校验）
				configG.GET("/raw", configH.GetRawConfig)
				configG.POST("/raw/validate", configH.ValidateRawConfig)
				configG.PUT("/raw", configH.UpdateRawConfig)
				// agent+模型 合并下拉的显示过滤（agents.selector.filters，保存即生效）
				configG.GET("/selector", configH.GetSelectorFilters)
				configG.PUT("/selector", configH.UpdateSelectorFilters)
				// 软重载：热刷新 skill/command/rule 扫描目录配置（不杀进程）
				configG.POST("/reload", configH.Reload)
				if mcpH != nil {
					configG.GET("/mcp", mcpH.GetMCPConfig)
					configG.PUT("/mcp", mcpH.UpdateMCPConfig)
					configG.GET("/mcp/status", mcpH.GetMCPStatus)
					// MCP 聚合网关：状态查询与开关
					configG.GET("/mcp/gateway", mcpH.GetGatewayStatus)
					configG.POST("/mcp/gateway", mcpH.SetGatewayEnabled)
					// 网关上游管理：禁用/启用/自定义 server
					configG.POST("/mcp/gateway/upstreams/:name/disable", mcpH.DisableUpstream)
					configG.POST("/mcp/gateway/upstreams/:name/enable", mcpH.EnableUpstream)
					configG.POST("/mcp/gateway/custom-servers", mcpH.AddCustomServer)
					configG.DELETE("/mcp/gateway/custom-servers/:name", mcpH.RemoveCustomServer)
				}
			}

			// 文件系统目录浏览（用于前端目录选择器）
			protected.GET("/filesystem/dirs", fsHandler.ListDirs)
			protected.GET("/filesystem/worktrees", fsHandler.ListWorktrees)
			protected.POST("/filesystem/worktrees", fsHandler.CreateWorktree)
			protected.GET("/filesystem/list", fsHandler.ListFiles)
			protected.GET("/filesystem/docs", fsHandler.ListDocs)
			protected.GET("/filesystem/skills", fsHandler.Skills)
			protected.GET("/filesystem/commands", fsHandler.Commands)
			protected.GET("/filesystem/rules", fsHandler.Rules)
			protected.GET("/filesystem/sub-agents", fsHandler.SubAgents)
			protected.GET("/filesystem/file", fsHandler.ReadFile)
			protected.PUT("/filesystem/file", fsHandler.WriteFile)
			protected.POST("/filesystem/create", fsHandler.CreateEntry)
			protected.DELETE("/filesystem/entry", fsHandler.DeleteEntry)

			// Git 只读查询（Git 管理面板：worktree 提交记录与文件 diff）
			gitH := handlers.NewGitHandler()
			protected.GET("/git/log", gitH.Log)
			protected.GET("/git/status", gitH.Status)
			protected.GET("/git/commit-files", gitH.CommitFiles)
			protected.GET("/git/diff", gitH.Diff)

			// 内置浏览器：抓取网页并提取正文，供任务对话框引用
			protected.GET("/browser/fetch", browserH.Fetch)

			// 定时任务
			sched := protected.Group("/scheduled-tasks")
			{
				sched.POST("", schedTaskH.Create)
				sched.GET("", schedTaskH.List)
				sched.GET("/:id", schedTaskH.Get)
				sched.PUT("/:id", schedTaskH.Update)
				sched.DELETE("/:id", schedTaskH.Delete)
				sched.POST("/:id/run", schedTaskH.Run)
				sched.GET("/:id/executions", schedTaskH.Executions)
			}

			// 任务管理（基于 tasks.json + git worktree 隔离）
			if tmH != nil {
				tm := protected.Group("/taskmanager")
				{
					tm.GET("", tmH.Get)
					tm.PUT("", tmH.Save)
					tm.GET("/status", tmH.Status)
					tm.GET("/events", tmH.Events)
					tm.GET("/git-status", tmH.GitStatus)
					tm.POST("/git-init", tmH.GitInit)
					tm.POST("/start", tmH.Start)
					tm.POST("/stop", tmH.Stop)
					tm.PUT("/max-parallel", tmH.SetMaxParallel)
					tm.POST("/tasks", tmH.UpsertTask)
					tm.DELETE("/tasks/:task_id", tmH.DeleteTask)
					tm.POST("/archive", tmH.Archive)
					tm.GET("/archived", tmH.ListArchived)
					tm.POST("/archived/:task_id/restore", tmH.RestoreArchived)
					tm.DELETE("/archived/:task_id", tmH.DeleteArchived)
				}
			}

			// 全局笔记
			notes := protected.Group("/notes")
			{
				notes.GET("/tags", noteH.ListTags)
				notes.GET("/settings", noteH.GetSettings)
				notes.PUT("/settings", noteH.UpdateSettings)
				notes.POST("/settings/mcp-token", noteH.GenerateMCPToken)
				notes.POST("", noteH.Create)
				notes.GET("", noteH.List)
				notes.GET("/export", noteH.Export)
				notes.POST("/import", noteH.Import)
				notes.GET("/:id", noteH.Get)
				notes.PUT("/:id", noteH.Update)
				notes.POST("/:id/classify", noteH.ClassifyNow)
				notes.DELETE("/:id", noteH.Delete)
			}

			// 任务设置（自动打标签 / AI 标题生成）
			tasks := protected.Group("/tasks")
			{
				tasks.GET("/settings", taskSettingsH.GetSettings)
				tasks.PUT("/settings", taskSettingsH.UpdateSettings)
			}

			// goal 设置（通用 /goal 循环：评估 agent/模型 + 限制条件）
			if goalSettingsH != nil {
				goal := protected.Group("/goal")
				{
					goal.GET("/settings", goalSettingsH.GetSettings)
					goal.PUT("/settings", goalSettingsH.UpdateSettings)
					// 评估角色列表（文件式定义；增删改走 /filesystem 通用文件接口）
					goal.GET("/roles", goalSettingsH.Roles)
				}
			}

			// 工具调用历史（「工具调用记录」页面）
			if toolCallH != nil {
				protected.GET("/tool-calls", toolCallH.List)
			}

			// 全局权限规则设置（yolo / 白名单 / 黑名单）
			permissions := protected.Group("/permissions")
			{
				permissions.GET("/settings", permSettingsH.GetSettings)
				permissions.PUT("/settings", permSettingsH.UpdateSettings)
			}

			// 用户 agent 最近使用偏好
			protected.GET("/agent-prefs", agentPrefsH.Get)
			protected.PATCH("/agent-prefs", agentPrefsH.Patch)

			// 内置 MCP 条目同步（token 生成后由前端触发）
			if subAgentH != nil {
				subAgentH.RegisterRoutes(protected)
			}

			// 实时日志流（SSE，供前端日志查看器订阅）
			protected.GET("/logs/stream", logH.Stream)
		}

		// 终端 WebSocket（通过 query token 认证，不走 AuthRequired 中间件）
		terminalH := handlers.NewTerminalHandler(agentRouter, jwtSvc)
		v1.GET("/sessions/:id/terminal", terminalH.HandleTerminal)
		// 工作区终端：任务（会话）尚未开始时在工作区 cwd 下启动 shell
		v1.GET("/workspaces/:id/terminal", terminalH.HandleWorkspaceTerminal)
		// agent 终端事件桥接（只读）：agent 执行 shell 时推送 created/output/exit 事件
		v1.GET("/sessions/:id/agent-terminals", terminalH.HandleAgentTerminal)
		// terminal 类型认证的交互式登录终端（设置页「登录」按钮）
		v1.GET("/agents/:type/auth-terminal", terminalH.HandleAgentAuthTerminal)
	}

	health := r.Group("/health")
	{
		health.GET("", func(c *gin.Context) {
			c.JSON(200, gin.H{"status": "ok"})
		})
	}

	// 生产模式（release）下由后端服务前端静态文件，实现单端口部署。
	// debug 模式保留双端口 + vite proxy，便于前端热更新。
	log.Printf("[debug] gin.Mode()=%q webDist=%q", gin.Mode(), webDist)
	if gin.Mode() == gin.ReleaseMode {
		setupStatic(r, webDist)
	}

	return r
}

// setupStatic 让 Gin 服务前端 SPA 静态文件：
//   - 已存在的静态资源（如 /assets/*.js）直接返回
//   - 非 /api 的未匹配路径返回 index.html，由前端路由处理（BrowserRouter SPA fallback）
//   - /api 下未匹配路径返回 404 JSON，避免把 API 请求误判为前端路由
func setupStatic(r *gin.Engine, webDist string) {
	if webDist == "" {
		log.Printf("[setupStatic] webDist is empty, skipping")
		return
	}
	if info, err := os.Stat(webDist); err != nil || !info.IsDir() {
		log.Printf("[setupStatic] webDist %q stat error: %v isDir=%v, skipping", webDist, err, info != nil && info.IsDir())
		return
	}
	indexFile := filepath.Join(webDist, "index.html")
	log.Printf("[setupStatic] serving static from %q, index=%q", webDist, indexFile)
	fileServer := http.FileServer(http.Dir(webDist))

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// 尝试服务真实存在的静态文件
		cleanPath := filepath.Join(webDist, filepath.Clean("/"+path))
		if info, err := os.Stat(cleanPath); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		// SPA fallback：返回 index.html，交给前端路由
		c.Header("Cache-Control", "no-cache")
		c.File(indexFile)
	})
}
