package services

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"opennexus/internal/config"
)

// TunnelState 隧道运行状态。
type TunnelState string

const (
	TunnelStateStopped  TunnelState = "stopped"  // 未运行
	TunnelStateStarting TunnelState = "starting" // 进程已拉起，等待隧道就绪（quick 等待分配域名）
	TunnelStateRunning  TunnelState = "running"  // 隧道已建立
	TunnelStateError    TunnelState = "error"    // 最近一次启动/运行失败
)

// quick 模式域名：cloudflared 日志输出 "https://xxx.trycloudflare.com"。
var quickTunnelURLRe = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// token 模式就绪信号：日志输出 "Registered tunnel connection"。
var tokenReadyRe = regexp.MustCompile(`Registered tunnel connection`)

// ngrok 模式域名：--log-format=logfmt 下 "started tunnel" 行含 url=https://xxx.ngrok-free.app。
var ngrokURLRe = regexp.MustCompile(`url=(https://\S+)`)

// vscode 模式就绪信号：code tunnel 输出 "https://vscode.dev/tunnel/<name>[/folder]"。
var vscodeURLRe = regexp.MustCompile(`https://vscode\.dev/tunnel/\S+`)

// vscode 模式设备授权提示：GitHub "log into ... and use code X"；
// Microsoft "open the page ... and enter the code X"。等待用户授权期间透出给前端展示。
var (
	vscodeDeviceRe   = regexp.MustCompile(`log into (https://\S+) and use code ([A-Za-z0-9-]+)`)
	vscodeDeviceMSRe = regexp.MustCompile(`open the page (https://\S+) and enter the code ([A-Za-z0-9-]+)`)
)

// TunnelStatus 是返回给前端的隧道快照。
type TunnelStatus struct {
	State      TunnelState `json:"state"`
	Mode       string      `json:"mode"`
	URL        string      `json:"url"`   // 公网访问地址；token 模式取配置的 hostname（可能为空）
	Error      string      `json:"error"` // 最近一次错误（含进程退出原因）
	Installed  bool        `json:"installed"`
	BinPath    string      `json:"bin_path"`
	LoginURL   string      `json:"login_url,omitempty"`   // vscode 模式等待授权时的设备登录地址
	DeviceCode string      `json:"device_code,omitempty"` // vscode 模式等待授权时的设备代码
}

// TunnelService 管理 cloudflared / ngrok / code 子进程。
// quick 模式免账号（随机 trycloudflare.com 域名）；token 模式使用 Cloudflare 具名隧道；
// ngrok 模式拉起 `ngrok http`（token 即 authtoken，可留空走 ngrok 本地已保存配置）；
// vscode 模式拉起 `code tunnel`，建立 VS Code Remote Tunnel（不直接暴露本服务，
// 授权后可在 vscode.dev / 桌面 VS Code 远程访问本机并转发端口）。
// 不自动重启：进程意外退出即标记 error，由用户决定是否重开。
type TunnelService struct {
	mu   sync.Mutex
	cfg  config.TunnelConfig
	port int
	// guard 启动前安全检查（如 auto_login 开启时拒绝暴露公网）；nil 表示不检查。
	guard func() error

	cmd        *exec.Cmd
	exitDone   chan struct{} // 进程退出时由 waitExit 关闭（Wait 的唯一调用方）
	state      TunnelState
	url        string
	errText    string
	loginURL   string   // vscode 模式设备授权地址（starting 等待授权期间）
	deviceCode string   // vscode 模式设备授权代码
	loginPhase bool     // vscode 模式：当前进程为 user login 授权前置步骤，成功退出后接力拉起隧道
	logTail    []string // 最近若干行隧道进程输出，供失败诊断
}

// binName 返回当前模式对应的子进程名（用于查找与报错文案）。
func (s *TunnelService) binName() string {
	switch s.cfg.Mode {
	case config.TunnelModeNgrok:
		return "ngrok"
	case config.TunnelModeVSCode:
		return "code"
	}
	return "cloudflared"
}

// NewTunnelService 创建隧道服务；port 为本地 HTTP 服务端口（quick 模式的回源地址）。
func NewTunnelService(cfg config.TunnelConfig, port int) *TunnelService {
	return &TunnelService{cfg: cfg, port: port, state: TunnelStateStopped}
}

// SetGuard 注入启动前安全检查：返回 error 时拒绝启动并进入 error 状态。
// 每次 Start 时执行，使改密码这类运行期变化也能被及时感知。
func (s *TunnelService) SetGuard(g func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guard = g
}

// SetConfig 更新隧道配置（设置页保存后调用）；不影响已运行的隧道。
func (s *TunnelService) SetConfig(cfg config.TunnelConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// Status 返回当前状态快照（installed/bin_path 实时探测，未安装时前端展示安装指引）。
func (s *TunnelService) Status() TunnelStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	bin, err := s.resolveBinary()
	return TunnelStatus{
		State:      s.state,
		Mode:       s.cfg.Mode,
		URL:        s.url,
		Error:      s.errText,
		Installed:  err == nil,
		BinPath:    bin,
		LoginURL:   s.loginURL,
		DeviceCode: s.deviceCode,
	}
}

// Start 按当前配置拉起隧道子进程。进程异步就绪：
// quick 模式解析输出中的 trycloudflare.com 域名后进入 running；
// token 模式等到 "Registered tunnel connection" 后进入 running；
// ngrok 模式解析 "started tunnel" 日志行中的 url= 后进入 running；
// vscode 模式未登录时先跑 user login（设备授权，输出代码经 status 透出），
// 授权成功接力拉起 code tunnel，解析输出中的 vscode.dev/tunnel 地址后进入 running。
func (s *TunnelService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == TunnelStateStarting || s.state == TunnelStateRunning {
		return nil
	}
	// vscode 模式跳过 guard：Remote Tunnel 由 GitHub/Microsoft 账号端到端鉴权，
	// 不直接暴露本服务登录页，auto_login / 默认密码的前提不成立。
	if s.guard != nil && s.cfg.Mode != config.TunnelModeVSCode {
		if err := s.guard(); err != nil {
			s.state = TunnelStateError
			s.errText = err.Error()
			return err
		}
	}
	bin, err := s.resolveBinary()
	if err != nil {
		s.state = TunnelStateError
		s.errText = err.Error()
		return err
	}

	// token 经环境变量传给子进程，避免出现在 ps 命令行里被本机其他用户读取。
	var args []string
	var tokenEnv string
	var loginPhase bool
	switch s.cfg.Mode {
	case config.TunnelModeNgrok:
		args = []string{"http", "--log=stdout", "--log-format=logfmt"}
		if h := ngrokDomain(s.cfg.Hostname); h != "" {
			args = append(args, "--url="+h)
		}
		args = append(args, strconv.Itoa(s.port))
		if token := strings.TrimSpace(s.cfg.Token); token != "" {
			tokenEnv = "NGROK_AUTHTOKEN=" + token
		}
	case config.TunnelModeVSCode:
		args = s.vscodeTunnelArgs()
		// code tunnel 不接受 --provider（那是 `tunnel user login` 的参数）；
		// 无凭据时先跑 user login 做设备授权存凭据，成功退出后由 waitExit 接力拉起隧道。
		// 已有凭据（user show 退出码 0）时跳过，直接拉起隧道。
		if !vscodeLoggedIn(bin) {
			provider := s.cfg.Provider
			if provider != "microsoft" {
				provider = "github"
			}
			args = []string{"tunnel", "user", "login", "--provider", provider}
			loginPhase = true
		}
	default:
		args = []string{"tunnel", "--no-autoupdate"}
		if s.cfg.Mode == config.TunnelModeToken {
			token := strings.TrimSpace(s.cfg.Token)
			if token == "" {
				s.state = TunnelStateError
				s.errText = "tunnel.mode=token 但未配置 token"
				return errors.New(s.errText)
			}
			args = append(args, "run")
			tokenEnv = "TUNNEL_TOKEN=" + token
		} else {
			args = append(args, "--url", fmt.Sprintf("http://127.0.0.1:%d", s.port))
		}
	}

	return s.spawnLocked(bin, args, tokenEnv, loginPhase)
}

// spawnLocked 拉起隧道子进程并接管其输出/退出（调用方须持锁）。
// 启动成功置 starting；loginPhase 标记本次为 vscode 授权前置步骤，
// 成功退出后由 waitExit 接力拉起真正的隧道进程。
func (s *TunnelService) spawnLocked(bin string, args []string, tokenEnv string, loginPhase bool) error {
	cmd := exec.Command(bin, args...)
	if tokenEnv != "" {
		cmd.Env = append(os.Environ(), tokenEnv)
	}
	// cloudflared 把日志（含 quick 域名）写到 stderr；ngrok（--log=stdout）与
	// code（设备授权提示）写到 stdout，两路一并扫描，就绪信号落在哪边都能捕获。
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("捕获 %s stdout 失败: %w", s.binName(), err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("捕获 %s stderr 失败: %w", s.binName(), err)
	}
	if err := cmd.Start(); err != nil {
		s.state = TunnelStateError
		s.errText = fmt.Sprintf("启动 %s 失败: %v", s.binName(), err)
		return errors.New(s.errText)
	}

	s.cmd = cmd
	s.exitDone = make(chan struct{})
	s.state = TunnelStateStarting
	s.url = ""
	s.errText = ""
	s.loginURL = ""
	s.deviceCode = ""
	s.loginPhase = loginPhase
	s.logTail = nil
	slog.Info("公网隧道启动中", "mode", s.cfg.Mode, "pid", cmd.Process.Pid)

	go s.scanOutput(stdout)
	go s.scanOutput(stderr)
	go s.waitExit(cmd, s.exitDone)
	return nil
}

// vscodeTunnelArgs 返回 code tunnel 本体参数：免许可条款交互提示；
// 名称取 hostname 配置（--name，vscode.dev 中显示，留空用机器主机名）。
func (s *TunnelService) vscodeTunnelArgs() []string {
	args := []string{"tunnel", "--accept-server-license-terms"}
	if name := strings.TrimSpace(s.cfg.Hostname); name != "" {
		args = append(args, "--name", name)
	}
	return args
}

// vscodeLoggedIn 探测 code CLI 是否已有隧道账号凭据（user show 退出码 0 = 已登录）。
// 已登录时跳过授权前置步骤；凭据关联的账号即为隧道实际使用的账号。
func vscodeLoggedIn(bin string) bool {
	return exec.Command(bin, "tunnel", "user", "show").Run() == nil
}

// ngrokDomain 把配置的对外域名转成 ngrok --url 接受的格式（去 scheme）。
func ngrokDomain(hostname string) string {
	h := strings.TrimSpace(hostname)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return h
}

// Stop 终止隧道子进程；未运行时为无操作。
func (s *TunnelService) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	done := s.exitDone
	s.cmd = nil
	s.exitDone = nil
	s.state = TunnelStateStopped
	s.url = ""
	s.errText = ""
	s.loginURL = ""
	s.deviceCode = ""
	s.loginPhase = false
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	slog.Info("公网隧道停止中", "pid", cmd.Process.Pid)
	if runtime.GOOS == "windows" {
		_ = cmd.Process.Kill() // Windows 无 SIGTERM，直接杀
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
}

// scanOutput 逐行扫描隧道进程输出：解析 quick 域名 / token 就绪信号 / ngrok url，并留存尾部日志。
func (s *TunnelService) scanOutput(pipe io.ReadCloser) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		line := scanner.Text()
		s.mu.Lock()
		s.logTail = append(s.logTail, line)
		if len(s.logTail) > 30 {
			s.logTail = s.logTail[len(s.logTail)-30:]
		}
		if s.state == TunnelStateStarting {
			switch {
			case s.cfg.Mode == config.TunnelModeNgrok:
				if m := ngrokURLRe.FindStringSubmatch(line); m != nil {
					s.state = TunnelStateRunning
					s.url = m[1]
					slog.Info("公网隧道已就绪", "url", m[1])
				}
			case s.cfg.Mode == config.TunnelModeVSCode:
				// 先捕获设备授权提示（等待用户在浏览器完成登录），就绪后取 vscode.dev 地址。
				if m := vscodeDeviceRe.FindStringSubmatch(line); m != nil {
					s.loginURL, s.deviceCode = m[1], m[2]
					slog.Info("VS Code 隧道等待设备授权", "url", m[1], "code", m[2])
				} else if m := vscodeDeviceMSRe.FindStringSubmatch(line); m != nil {
					s.loginURL, s.deviceCode = m[1], m[2]
					slog.Info("VS Code 隧道等待设备授权", "url", m[1], "code", m[2])
				}
				if m := vscodeURLRe.FindString(line); m != "" {
					s.state = TunnelStateRunning
					s.url = m
					s.loginURL = ""
					s.deviceCode = ""
					slog.Info("VS Code 隧道已就绪", "url", m)
				}
			default:
				if m := quickTunnelURLRe.FindString(line); m != "" {
					s.state = TunnelStateRunning
					s.url = m
					slog.Info("公网隧道已就绪", "url", m)
				} else if s.cfg.Mode == config.TunnelModeToken && tokenReadyRe.MatchString(line) {
					s.state = TunnelStateRunning
					s.url = s.cfg.Hostname
					slog.Info("具名隧道已连接", "hostname", s.cfg.Hostname)
				}
			}
		}
		s.mu.Unlock()
	}
}

// waitExit 等进程退出（Wait 唯一调用方）：启动期退出记为 error（附尾部日志），
// 运行中退出同样记 error；被 Stop 主动停止时静默返回。
func (s *TunnelService) waitExit(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	close(done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != cmd {
		return // 已被 Stop 清理
	}
	s.cmd = nil
	if s.state == TunnelStateStopped {
		return
	}
	// vscode 授权前置步骤成功退出：凭据已写入 ~/.vscode/cli，接力拉起隧道进程。
	if s.loginPhase {
		s.loginPhase = false
		if err == nil {
			bin, berr := s.resolveBinary()
			if berr == nil {
				berr = s.spawnLocked(bin, s.vscodeTunnelArgs(), "", false)
			}
			if berr != nil {
				s.state = TunnelStateError
				s.errText = berr.Error()
			}
			return
		}
	}
	s.state = TunnelStateError
	s.url = ""
	s.loginURL = ""
	s.deviceCode = ""
	msg := fmt.Sprintf("%s 已退出: %v", s.binName(), err)
	if tail := strings.TrimSpace(strings.Join(s.logTail, "\n")); tail != "" {
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		msg = fmt.Sprintf("%s\n%s", msg, tail)
	}
	s.errText = msg
	slog.Warn("公网隧道进程退出", "err", err)
}

// resolveBinary 按当前模式解析子进程路径：配置路径 → PATH → 常见安装位置。
func (s *TunnelService) resolveBinary() (string, error) {
	name := s.binName()
	cfgPath := s.cfg.CloudflaredPath
	switch name {
	case "ngrok":
		cfgPath = s.cfg.NgrokPath
	case "code":
		cfgPath = s.cfg.VSCodePath
	}
	if cfgPath != "" {
		if info, err := os.Stat(cfgPath); err == nil && !info.IsDir() {
			return cfgPath, nil
		}
		return "", fmt.Errorf("配置的 %s 路径不可用: %s", name, cfgPath)
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	candidates := []string{"/usr/local/bin/" + name, "/opt/homebrew/bin/" + name, "/usr/bin/" + name}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".openNexus", "bin", name))
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 %s，请先安装（如 brew install %s）或在设置中指定路径", name, name)
}
