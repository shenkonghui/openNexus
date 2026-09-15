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

// TunnelStatus 是返回给前端的隧道快照。
type TunnelStatus struct {
	State     TunnelState `json:"state"`
	Mode      string      `json:"mode"`
	URL       string      `json:"url"`   // 公网访问地址；token 模式取配置的 hostname（可能为空）
	Error     string      `json:"error"` // 最近一次错误（含进程退出原因）
	Installed bool        `json:"installed"`
	BinPath   string      `json:"bin_path"`
}

// TunnelService 管理 cloudflared 子进程，把本地 HTTP 服务暴露到公网。
// quick 模式免账号（随机 trycloudflare.com 域名）；token 模式使用具名隧道。
// 不自动重启：进程意外退出即标记 error，由用户决定是否重开。
type TunnelService struct {
	mu   sync.Mutex
	cfg  config.TunnelConfig
	port int
	// guard 启动前安全检查（如 auto_login 开启时拒绝暴露公网）；nil 表示不检查。
	guard func() error

	cmd      *exec.Cmd
	exitDone chan struct{} // 进程退出时由 waitExit 关闭（Wait 的唯一调用方）
	state    TunnelState
	url      string
	errText  string
	logTail  []string // 最近若干行 cloudflared 输出，供失败诊断
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
		State:     s.state,
		Mode:      s.cfg.Mode,
		URL:       s.url,
		Error:     s.errText,
		Installed: err == nil,
		BinPath:   bin,
	}
}

// Start 按当前配置拉起 cloudflared。进程异步就绪：
// quick 模式解析输出中的 trycloudflare.com 域名后进入 running；
// token 模式等到 "Registered tunnel connection" 后进入 running。
func (s *TunnelService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == TunnelStateStarting || s.state == TunnelStateRunning {
		return nil
	}
	if s.guard != nil {
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

	args := []string{"tunnel", "--no-autoupdate"}
	var token string
	if s.cfg.Mode == config.TunnelModeToken {
		token = strings.TrimSpace(s.cfg.Token)
		if token == "" {
			s.state = TunnelStateError
			s.errText = "tunnel.mode=token 但未配置 token"
			return errors.New(s.errText)
		}
		args = append(args, "run")
	} else {
		args = append(args, "--url", fmt.Sprintf("http://127.0.0.1:%d", s.port))
	}

	cmd := exec.Command(bin, args...)
	if token != "" {
		// 经环境变量传 token，避免 --token 出现在 ps 命令行里被本机其他用户读取。
		cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+token)
	}
	// cloudflared 把日志（含 quick 域名）写到 stderr；合并 stdout 一并扫描。
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("捕获 cloudflared stderr 失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		s.state = TunnelStateError
		s.errText = fmt.Sprintf("启动 cloudflared 失败: %v", err)
		return errors.New(s.errText)
	}

	s.cmd = cmd
	s.exitDone = make(chan struct{})
	s.state = TunnelStateStarting
	s.url = ""
	s.errText = ""
	s.logTail = nil
	slog.Info("公网隧道启动中", "mode", s.cfg.Mode, "pid", cmd.Process.Pid)

	go s.scanOutput(stderr)
	go s.waitExit(cmd, s.exitDone)
	return nil
}

// Stop 终止 cloudflared 子进程；未运行时为无操作。
func (s *TunnelService) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	done := s.exitDone
	s.cmd = nil
	s.exitDone = nil
	s.state = TunnelStateStopped
	s.url = ""
	s.errText = ""
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

// scanOutput 逐行扫描 cloudflared 输出：解析 quick 域名 / token 就绪信号，并留存尾部日志。
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
	s.state = TunnelStateError
	s.url = ""
	msg := fmt.Sprintf("cloudflared 已退出: %v", err)
	if tail := strings.TrimSpace(strings.Join(s.logTail, "\n")); tail != "" {
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		msg = fmt.Sprintf("%s\n%s", msg, tail)
	}
	s.errText = msg
	slog.Warn("公网隧道进程退出", "err", err)
}

// resolveBinary 解析 cloudflared 路径：配置路径 → PATH → 常见安装位置。
func (s *TunnelService) resolveBinary() (string, error) {
	if p := s.cfg.CloudflaredPath; p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("配置的 cloudflared 路径不可用: %s", p)
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	candidates := []string{"/usr/local/bin/cloudflared", "/opt/homebrew/bin/cloudflared", "/usr/bin/cloudflared"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".openNexus", "bin", "cloudflared"))
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("未找到 cloudflared，请先安装（如 brew install cloudflared）或在设置中指定路径")
}
