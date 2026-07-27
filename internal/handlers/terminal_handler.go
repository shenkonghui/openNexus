package handlers

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"opennexus/internal/acp"
	"opennexus/internal/middleware"
	"opennexus/internal/models"
	"opennexus/internal/services"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域，认证由 query token 保证
	},
}

// TerminalHandler 处理终端 WebSocket 连接，在会话工作目录下启动交互式 shell。
type TerminalHandler struct {
	store  SessionStore
	jwtSvc *services.JWTService
}

// NewTerminalHandler 创建 TerminalHandler。
func NewTerminalHandler(store SessionStore, jwtSvc *services.JWTService) *TerminalHandler {
	return &TerminalHandler{store: store, jwtSvc: jwtSvc}
}

// HandleTerminal GET /api/v1/sessions/:id/terminal?token=...
// 通过 WebSocket 升级连接，在 session cwd 下启动 PTY shell，
// 双向转发 stdin/stdout。认证通过 query 参数 token 传递（WebSocket 不支持自定义 header）。
func (h *TerminalHandler) HandleTerminal(c *gin.Context) {
	sess, ok := h.authWSSession(c)
	if !ok {
		return
	}
	// 获取 cwd：普通会话取工作区 cwd，编排任务会话取其 git worktree 路径（见 resolveSessionWorkingDir）。
	cwd := resolveSessionWorkingDir(h.store, sess)
	if cwd == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "NO_CWD", "message": "该会话没有工作目录"}})
		return
	}

	// 验证 cwd 存在且是目录
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_CWD", "message": "工作目录路径无效"}})
		return
	}
	if info, err := os.Stat(cwdAbs); err != nil || !info.IsDir() {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "CWD_NOT_FOUND", "message": "工作目录不存在"}})
		return
	}

	// 3. WebSocket 升级
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 已写入错误响应
		return
	}
	defer conn.Close()

	// 4. 启动 PTY shell（提示符仅显示当前目录最后一级，而非完整路径）
	cmd, cleanup := buildTerminalCommand(cwdAbs)
	defer cleanup()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteJSON(gin.H{"type": "error", "message": "启动终端失败: " + err.Error()})
		return
	}
	defer func() {
		ptmx.Close()
		_ = cmd.Process.Kill()
		cmd.Wait()
	}()

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	// 5. PTY -> WebSocket（stdout 转发）
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// 6. WebSocket -> PTY（stdin 转发）
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		// 处理控制消息（resize）
		if len(data) > 0 && data[0] == 0x01 {
			// 简单的 resize 协议：0x01 + 4 字节 cols + 4 字节 rows
			if len(data) >= 9 {
				cols := int(data[1])<<8 | int(data[2])
				rows := int(data[3])<<8 | int(data[4])
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
			}
			continue
		}
		if _, err := ptmx.Write(data); err != nil {
			return
		}
	}
}

// authWSSession 是终端类 WebSocket 端点的公共前置：query token 认证 + 会话归属校验。
// 失败时已写入错误响应，返回 ok=false。
func (h *TerminalHandler) authWSSession(c *gin.Context) (*models.Session, bool) {
	userID, ok := h.authWSUser(c)
	if !ok {
		return nil, false
	}

	// 加载会话并校验归属
	id, ok := parseSessionID(c)
	if !ok {
		return nil, false
	}
	sess, err := h.store.GetSessionByDBID(id)
	if err != nil || sess == nil || sess.UserID != userID {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "SESSION_NOT_FOUND", "message": "会话不存在"}})
		return nil, false
	}
	return sess, true
}

// authWSUser 是 WebSocket 端点的 query token 认证（WebSocket 不支持自定义 header）。
// 失败时已写入错误响应，返回 ok=false。
func (h *TerminalHandler) authWSUser(c *gin.Context) (uint, bool) {
	tokenStr := strings.TrimSpace(c.Query("token"))
	if tokenStr == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "缺少认证令牌"}})
		return 0, false
	}
	claims, err := h.jwtSvc.Parse(tokenStr)
	if err != nil || claims.TokenType != services.TokenTypeAccess {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "无效的令牌"}})
		return 0, false
	}
	c.Set(middleware.UserIDKey(), claims.UserID)
	return claims.UserID, true
}

// AgentAuthTerminalSpecProvider 提供 terminal 认证登录进程启动参数（*agent.Router 实现）。
type AgentAuthTerminalSpecProvider interface {
	AgentAuthTerminalSpec(agentType, methodID string) (acp.AuthTerminalSpec, error)
}

// HandleAgentAuthTerminal GET /api/v1/agents/:type/auth-terminal?token=...&method_id=...
// 在 PTY 中拉起 agent 的交互式登录流程（terminal 类型认证方式），双向转发输入输出：
// 用户在网页终端完成 OAuth/TUI 登录，凭证落盘后重连 agent 即生效。
func (h *TerminalHandler) HandleAgentAuthTerminal(c *gin.Context) {
	if _, ok := h.authWSUser(c); !ok {
		return
	}
	provider, ok := h.store.(AgentAuthTerminalSpecProvider)
	if !ok {
		c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "NOT_SUPPORTED", "message": "当前后端不支持认证终端"}})
		return
	}
	agentType := strings.TrimSpace(c.Param("type"))
	spec, err := provider.AgentAuthTerminalSpec(agentType, strings.TrimSpace(c.Query("method_id")))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "AUTH_TERMINAL_UNAVAILABLE", "message": err.Error()}})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 已写入错误响应
		return
	}
	defer conn.Close()

	// 登录进程不依赖工作目录，在用户 home 下运行（凭证通常落在 home 下）
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Env = spec.Env
	if home, herr := os.UserHomeDir(); herr == nil {
		cmd.Dir = home
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": "启动登录进程失败: " + err.Error()})
		return
	}
	defer func() {
		ptmx.Close()
		_ = cmd.Process.Kill()
		cmd.Wait()
	}()

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	// PTY -> WebSocket；登录进程退出后推送提示并主动关闭连接，解除下方 ReadMessage 阻塞
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					cancel()
					return
				}
			}
			if rerr != nil {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[33m登录进程已退出，若已完成授权可关闭此窗口并重新连接 agent\x1b[0m\r\n"))
				cancel()
				conn.Close()
				return
			}
		}
	}()

	// WebSocket -> PTY（含 resize 控制帧，协议同 HandleTerminal）
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, data, rerr := conn.ReadMessage()
		if rerr != nil {
			return
		}
		if len(data) > 0 && data[0] == 0x01 {
			if len(data) >= 9 {
				cols := int(data[1])<<8 | int(data[2])
				rows := int(data[3])<<8 | int(data[4])
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
			}
			continue
		}
		if _, err := ptmx.Write(data); err != nil {
			return
		}
	}
}

// AgentTerminalSubscriber 按 DB 会话 ID 订阅 agent 终端事件（*agent.Router 实现）。
type AgentTerminalSubscriber interface {
	SubscribeAgentTerminal(dbSessionID uint) ([]acp.TerminalSnapshot, <-chan acp.TerminalEvent, func(), error)
}

// agentTermFrame 是 agent 终端事件的 WebSocket JSON 帧。
type agentTermFrame struct {
	Type       string  `json:"type"` // created | output | exit | released
	TerminalID string  `json:"terminalId"`
	Command    string  `json:"command,omitempty"`
	Cwd        string  `json:"cwd,omitempty"`
	Data       string  `json:"data,omitempty"` // base64（PTY 原始字节，含 ANSI 转义）
	ExitCode   *int    `json:"exitCode,omitempty"`
	Signal     *string `json:"signal,omitempty"`
}

// terminalEventFrame 把 bridge 事件转为 WebSocket 帧。
func terminalEventFrame(ev acp.TerminalEvent) agentTermFrame {
	f := agentTermFrame{Type: ev.Type, TerminalID: ev.TerminalID, Command: ev.Command, Cwd: ev.Cwd, ExitCode: ev.ExitCode, Signal: ev.Signal}
	if len(ev.Data) > 0 {
		f.Data = base64.StdEncoding.EncodeToString(ev.Data)
	}
	return f
}

// HandleAgentTerminal GET /api/v1/sessions/:id/agent-terminals?token=...
// 订阅 agent 通过 ACP terminal 能力执行的 shell 命令事件：
// 连接后先回放活跃终端快照（created+output+exit），再持续推送实时事件，
// 供前端终端面板自动弹出只读 tab 展示执行情况。
func (h *TerminalHandler) HandleAgentTerminal(c *gin.Context) {
	sess, ok := h.authWSSession(c)
	if !ok {
		return
	}
	sub, ok := h.store.(AgentTerminalSubscriber)
	if !ok {
		c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": gin.H{"code": "NOT_SUPPORTED", "message": "当前后端不支持 agent 终端订阅"}})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 已写入错误响应
		return
	}
	defer conn.Close()

	snapshots, events, cancel, err := sub.SubscribeAgentTerminal(sess.ID)
	if err != nil {
		_ = conn.WriteJSON(gin.H{"type": "error", "message": err.Error()})
		return
	}
	defer cancel()

	// 回放快照：每个活跃终端依次下发 created / output / exit 帧，前端统一按事件回放
	for _, snap := range snapshots {
		if err := conn.WriteJSON(agentTermFrame{Type: acp.TerminalEventCreated, TerminalID: snap.TerminalID, Command: snap.Command, Cwd: snap.Cwd}); err != nil {
			return
		}
		if len(snap.Output) > 0 {
			if err := conn.WriteJSON(agentTermFrame{Type: acp.TerminalEventOutput, TerminalID: snap.TerminalID, Data: base64.StdEncoding.EncodeToString(snap.Output)}); err != nil {
				return
			}
		}
		if snap.Exited {
			if err := conn.WriteJSON(agentTermFrame{Type: acp.TerminalEventExit, TerminalID: snap.TerminalID, ExitCode: snap.ExitCode, Signal: snap.Signal}); err != nil {
				return
			}
		}
	}

	// 读 goroutine：仅用于感知客户端断开（前端不上行数据）
	ctx, cancelCtx := context.WithCancel(c.Request.Context())
	defer cancelCtx()
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancelCtx()
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := conn.WriteJSON(terminalEventFrame(ev)); err != nil {
				return
			}
		}
	}
}

// findShell 查找可用的 shell，优先 zsh > bash > sh。
func findShell() string {
	for _, sh := range []string{"zsh", "bash", "sh"} {
		if path, err := exec.LookPath(sh); err == nil {
			return path
		}
	}
	return "/bin/sh"
}

// buildTerminalCommand 构造在 cwd 下启动的交互式 shell 命令，并将提示符改为仅显示
// 当前目录的最后一级（如 task-001），而非完整路径。返回清理函数用于删除临时 rc 文件。
//
// 由于用户的 .zshrc/.bashrc 通常会自定义提示符，仅设置 PROMPT/PS1 环境变量会被其覆盖，
// 因此对 zsh 使用临时 ZDOTDIR、对 bash 使用 --rcfile：先加载用户原有配置，再覆盖提示符；
// sh 及回退情况直接通过 PS1 环境变量设置（POSIX shell 无 rc 覆盖问题）。
func buildTerminalCommand(cwd string) (*exec.Cmd, func()) {
	shell := findShell()
	env := append(os.Environ(), "TERM=xterm-256color")
	cleanup := func() {}

	switch filepath.Base(shell) {
	case "zsh":
		if dir, err := os.MkdirTemp("", "onx-zsh-"); err == nil {
			orig := os.Getenv("ZDOTDIR")
			if orig == "" {
				orig = os.Getenv("HOME")
			}
			// %1~ 表示当前路径的最后一级目录（在 $HOME 下显示为 ~）。
			rc := "[[ -f \"" + orig + "/.zshrc\" ]] && source \"" + orig + "/.zshrc\"\nPROMPT='%1~ %# '\nRPROMPT=''\n"
			if werr := os.WriteFile(filepath.Join(dir, ".zshrc"), []byte(rc), 0o600); werr == nil {
				env = append(env, "ZDOTDIR="+dir)
				cleanup = func() { _ = os.RemoveAll(dir) }
			} else {
				_ = os.RemoveAll(dir)
			}
		}
		cmd := exec.Command(shell)
		cmd.Dir = cwd
		cmd.Env = env
		return cmd, cleanup
	case "bash":
		if f, err := os.CreateTemp("", "onx-bash-*.sh"); err == nil {
			// \W 表示当前工作目录的最后一级（basename）。
			_, _ = f.WriteString("[ -f \"$HOME/.bashrc\" ] && source \"$HOME/.bashrc\"\nPS1='\\W \\$ '\n")
			_ = f.Close()
			cmd := exec.Command(shell, "--rcfile", f.Name(), "-i")
			cmd.Dir = cwd
			cmd.Env = env
			return cmd, func() { _ = os.Remove(f.Name()) }
		}
		fallthrough
	default:
		// sh 及回退：${PWD##*/} 在每次显示提示符时取 $PWD 的最后一级路径。
		cmd := exec.Command(shell)
		cmd.Dir = cwd
		cmd.Env = append(env, "PS1=${PWD##*/} $ ")
		return cmd, cleanup
	}
}

// 确保 models 包被引用（避免未使用导入）
var _ = models.SessionStatusActive
