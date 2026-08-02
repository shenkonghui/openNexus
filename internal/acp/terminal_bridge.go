package acp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/coder/acp-go-sdk"
	"github.com/creack/pty"
)

// defaultTerminalOutputLimit 是 agent 未指定 outputByteLimit 时的输出缓冲上限（1MB），
// 防止长时间运行的命令把内存撑爆。
const defaultTerminalOutputLimit = 1 << 20

// terminalEventChanSize 是单个订阅者的事件 channel 缓冲大小。
// 输出按 PTY 读块推送，4096 足够吸收突发输出；满则丢弃（对齐 Client.SessionUpdate 策略）。
const terminalEventChanSize = 4096

// 终端事件类型。
const (
	TerminalEventCreated  = "created"
	TerminalEventOutput   = "output"
	TerminalEventExit     = "exit"
	TerminalEventReleased = "released"
)

// TerminalEvent 是 agent 终端的实时事件，经 WebSocket 推给前端终端面板。
type TerminalEvent struct {
	Type       string
	TerminalID string
	Command    string  // created 事件：完整命令（含参数拼接，仅展示用）
	Cwd        string  // created 事件
	Data       []byte  // output 事件：原始输出字节（PTY 含 ANSI 转义）
	ExitCode   *int    // exit 事件
	Signal     *string // exit 事件
}

// TerminalSnapshot 是订阅时刻单个活跃终端的完整状态（含已缓冲输出），供前端回放。
type TerminalSnapshot struct {
	TerminalID string
	Command    string
	Cwd        string
	Output     []byte
	Exited     bool
	ExitCode   *int
	Signal     *string
}

// terminalSubscriber 是一个按 DB session ID 订阅终端事件的前端连接。
type terminalSubscriber struct {
	ch chan TerminalEvent
}

// bridgeTerminal 是一个由 agent 通过 terminal/create 发起的命令执行实例。
type bridgeTerminal struct {
	id      string
	dbID    uint
	display string // 展示用命令行（command + args 拼接）
	cwd     string

	cmd    *exec.Cmd
	closer func() // 关闭 PTY / 管道读端

	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
	exited    bool
	exitCode  *int
	signal    *string
	done      chan struct{}
}

// appendOutput 追加输出并按 limit 从头部截断（保证 UTF-8 字符边界）。
func (t *bridgeTerminal) appendOutput(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, data...)
	if len(t.buf) > t.limit {
		cut := len(t.buf) - t.limit
		// 向后推进到 rune 边界（跳过 UTF-8 连续字节 0b10xxxxxx）
		for cut < len(t.buf) && !utf8.RuneStart(t.buf[cut]) {
			cut++
		}
		t.buf = t.buf[cut:]
		t.truncated = true
	}
}

// snapshotOutput 返回当前输出副本与截断标志。
func (t *bridgeTerminal) snapshotOutput() ([]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]byte, len(t.buf))
	copy(out, t.buf)
	return out, t.truncated
}

// exitStatus 返回退出状态（未退出时 exited=false）。
func (t *bridgeTerminal) exitStatus() (exited bool, code *int, sig *string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exited, t.exitCode, t.signal
}

// TerminalBridge 实现 ACP terminal/* 能力：代 agent 执行命令（优先 PTY），
// 缓存输出供 agent 查询，同时把「创建/输出/退出」事件按 DB session ID 广播给前端订阅者。
// Service 级单例，被所有 Connection 的 Client 共享。
type TerminalBridge struct {
	mu    sync.Mutex
	seq   int
	terms map[string]*bridgeTerminal                // terminalId → 终端
	byDB  map[uint]map[string]*bridgeTerminal       // DB session ID → 活跃终端集合
	subs  map[uint]map[*terminalSubscriber]struct{} // DB session ID → 前端订阅者

	// resolve 把 ACP SessionId 映射为 DB session ID（0 表示未知，事件不路由）。
	resolve func(acp.SessionId) uint

	// denyCheck 可选：执行前的安全策略裁决（返回命中的 deny 规则原文；空串=放行）。
	// SetDenyCheck 注入；nil 则不裁决。命中时不启动进程，返回合成失败终端，
	// agent 通过 terminal/output 能读到拒绝原因并自行调整。
	denyCheck func(command string) string

	// onExit 可选：命令退出时回调（工具调用记录回填退出码）。SetOnExit 注入；nil 则跳过。
	onExit func(dbID uint, terminalID, command, cwd string, exitCode *int, signal *string)

	// backendProvider 可选：按 ACP SessionId 反查所属 agent 的 Backend，
	// 用于沙箱包裹 terminal/create 命令时构建与 agent 一致的 WriteDirs 白名单
	// （含 agent 配置目录如 ~/.claude）。SetBackendProvider 注入；nil 则用 nil backend
	// （仅工作目录 + 数据目录 + 临时目录可写，agent 配置目录不在白名单内）。
	backendProvider func(acp.SessionId) Backend
}

// SetDenyCheck 注入执行前安全策略裁决函数（返回命中的 deny 规则原文；空串=放行）。
func (b *TerminalBridge) SetDenyCheck(fn func(command string) string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.denyCheck = fn
}

// SetOnExit 注入命令退出回调（在广播 exit 事件后同 goroutine 调用）。
func (b *TerminalBridge) SetOnExit(fn func(dbID uint, terminalID, command, cwd string, exitCode *int, signal *string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onExit = fn
}

// SetBackendProvider 注入按 ACP SessionId 反查 Backend 的回调，
// 用于沙箱包裹 terminal/create 命令时构建与 agent 一致的 WriteDirs 白名单。
func (b *TerminalBridge) SetBackendProvider(fn func(acp.SessionId) Backend) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.backendProvider = fn
}

// NewTerminalBridge 创建 TerminalBridge。resolve 可为 nil（此时事件不路由到前端）。
func NewTerminalBridge(resolve func(acp.SessionId) uint) *TerminalBridge {
	return &TerminalBridge{
		terms:   make(map[string]*bridgeTerminal),
		byDB:    make(map[uint]map[string]*bridgeTerminal),
		subs:    make(map[uint]map[*terminalSubscriber]struct{}),
		resolve: resolve,
	}
}

// displayCommand 拼接展示用命令行。
func displayCommand(command string, args []string) string {
	if len(args) == 0 {
		return command
	}
	return command + " " + strings.Join(args, " ")
}

// findShell 查找可用的 shell，优先 zsh > bash > sh（与网页交互终端一致）。
func findShell() string {
	for _, sh := range []string{"zsh", "bash", "sh"} {
		if path, err := exec.LookPath(sh); err == nil {
			return path
		}
	}
	return "/bin/sh"
}

// EnsureUTF8Locale 保证环境变量中包含 UTF-8 locale，否则 shell 行编辑与程序输出会按
// 单字节处理多字节字符（如中文），导致终端乱码。按 locale 优先级（LC_ALL > LANG）
// 判断实际生效值：已是 UTF-8 时不做修改；否则追加 LANG 与 LC_ALL（LC_ALL 优先级
// 最高，可覆盖继承到的非 UTF-8 值，如 LC_ALL=C）。
func EnsureUTF8Locale(env []string) []string {
	var lcAll, lang string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "LC_ALL="); ok {
			lcAll = v
		} else if v, ok := strings.CutPrefix(e, "LANG="); ok {
			lang = v
		}
	}
	effective := lcAll
	if effective == "" {
		effective = lang
	}
	if v := strings.ToUpper(effective); strings.Contains(v, "UTF-8") || strings.Contains(v, "UTF8") {
		return env
	}
	// Debian/musl 内置 C.UTF-8；macOS 无 C.UTF-8，用 en_US.UTF-8。
	loc := "C.UTF-8"
	if runtime.GOOS == "darwin" {
		loc = "en_US.UTF-8"
	}
	return append(env, "LANG="+loc, "LC_ALL="+loc)
}

// startProcess 启动命令，优先 PTY（保留彩色输出，stdout/stderr 合并）；
// PTY 不可用时（如 Windows）回退管道。返回读端 reader 与关闭函数。
func startProcess(cmd *exec.Cmd) (*os.File, func(), error) {
	ptmx, err := pty.Start(cmd)
	if err == nil {
		return ptmx, func() { _ = ptmx.Close() }, nil
	}
	// 回退：合并 stdout/stderr 到同一管道
	pr, pw, perr := os.Pipe()
	if perr != nil {
		return nil, nil, fmt.Errorf("创建输出管道: %w", perr)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if serr := cmd.Start(); serr != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, nil, serr
	}
	_ = pw.Close() // 父进程侧写端关闭，子进程退出后读端 EOF
	return pr, func() { _ = pr.Close() }, nil
}

// Create 执行 terminal/create：启动命令并开始采集输出。
// 命中安全策略 deny 名单的命令不会启动，返回合成失败终端（非零退出码 + 拒绝原因）。
func (b *TerminalBridge) Create(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	command := params.Command
	args := params.Args
	// agent 常把整条 shell 命令塞进 command（无 args）；含空格时经 shell 解释执行
	if len(args) == 0 && strings.ContainsAny(command, " \t|&;<>()$`") {
		args = []string{"-c", command}
		command = findShell()
	}

	// 执行前安全策略裁决：deny 命中 → 不 spawn，合成失败终端让 agent 可读原因。
	// 用原始 command+args 拼展示串裁决（与权限名单的 title 语义一致）。
	b.mu.Lock()
	denyCheck := b.denyCheck
	b.mu.Unlock()
	if denyCheck != nil {
		if rule := denyCheck(displayCommand(params.Command, params.Args)); rule != "" {
			return b.createDenied(params, rule), nil
		}
	}

	cmd := exec.Command(command, args...)
	if params.Cwd != nil && *params.Cwd != "" {
		cmd.Dir = *params.Cwd
	}
	// UTF-8 locale 兼平台兼容；agent 显式传入的 LANG/LC_ALL 在后，优先生效
	// baseEnv 为主进程环境（沙箱生效时会被 SanitizeEnvForSandbox 剥离凭证类变量），
	// agentEnv 为 agent 显式声明的变量（始终透传，不受净化影响）。
	baseEnv := append(EnsureUTF8Locale(os.Environ()), "TERM=xterm-256color")
	agentEnv := make([]string, 0, len(params.Env))
	for _, e := range params.Env {
		agentEnv = append(agentEnv, e.Name+"="+e.Value)
	}
	cmd.Env = append(baseEnv, agentEnv...)

	// 沙箱开启时，TerminalBridge 执行的命令也须用 sandbox-exec 包裹，
	// 否则 agent 通过 terminal/create 协议在主 server 进程中执行命令会绕过沙箱。
	// 白名单沿用 agent 的沙箱 profile（工作目录 + 数据目录 + 临时目录 + agent 配置目录等）。
	if sb := CurrentSandboxSettings(); sb.Enabled {
		workDir := cmd.Dir
		if workDir == "" {
			workDir, _ = os.Getwd()
		}
		// 按 SessionId 反查所属 agent 的 Backend，构建与 agent 一致的 WriteDirs 白名单。
		// 查不到时退化为 nil backend（仅工作目录 + 数据目录 + 临时目录可写）。
		b.mu.Lock()
		provider := b.backendProvider
		b.mu.Unlock()
		var backend Backend
		if provider != nil {
			backend = provider(params.SessionId)
		}
		profile := BuildSandboxProfile(backend, workDir)
		argv := append([]string{command}, args...)
		wrapped, degraded := SandboxWrap(argv, profile)
		if !degraded {
			// 沙箱生效时剥离 baseEnv 中的凭证类变量（DOCKER_/AWS_/KUBECONFIG 等），
			// 与 process.go / bridge_transport.go 保持一致——sandbox-exec/bwrap 仅隔离文件系统，
			// 环境变量仍会透传，不剥离则 agent 可经 terminal/create 执行 printenv/env 窃取凭证。
			// agentEnv 为 agent 显式声明的工作所需变量，在净化后追加，不受净化影响。
			cmd.Env = append(SanitizeEnvForSandbox(baseEnv), agentEnv...)
			cmd.Path = wrapped[0]
			cmd.Args = wrapped
		} else if sb.Mode == SandboxModeEnforce {
			return b.createDenied(params, "沙箱模式为 enforce 但当前平台沙箱不可用"), nil
		}
	}

	reader, closer, err := startProcess(cmd)
	if err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("启动终端命令 %q: %w", params.Command, err)
	}

	limit := defaultTerminalOutputLimit
	if params.OutputByteLimit != nil && *params.OutputByteLimit > 0 {
		limit = *params.OutputByteLimit
	}
	var dbID uint
	if b.resolve != nil {
		dbID = b.resolve(params.SessionId)
	}

	b.mu.Lock()
	b.seq++
	term := &bridgeTerminal{
		id:      fmt.Sprintf("term-%d", b.seq),
		dbID:    dbID,
		display: displayCommand(params.Command, params.Args),
		cwd:     cmd.Dir,
		cmd:     cmd,
		closer:  closer,
		limit:   limit,
		done:    make(chan struct{}),
	}
	b.terms[term.id] = term
	if dbID != 0 {
		if b.byDB[dbID] == nil {
			b.byDB[dbID] = make(map[string]*bridgeTerminal)
		}
		b.byDB[dbID][term.id] = term
	}
	b.mu.Unlock()

	slog.Debug("ACP terminal/create", "terminal", term.id, "session", params.SessionId, "db_session", dbID,
		"command", term.display, "cwd", term.cwd)
	b.broadcast(dbID, TerminalEvent{Type: TerminalEventCreated, TerminalID: term.id, Command: term.display, Cwd: term.cwd})

	// 读输出直至 EOF，随后回收进程并广播退出状态
	go b.pump(term, reader)

	return acp.CreateTerminalResponse{TerminalId: term.id}, nil
}

// createDenied 生成 deny 命中的合成失败终端：不启动进程，缓冲写入拒绝原因，
// 立即标记退出（exit code 1）。agent 走 terminal/output / wait_for_exit 均能拿到
// 确定性结果，前端也能看到"命令被安全策略拒绝"的记录。
func (b *TerminalBridge) createDenied(params acp.CreateTerminalRequest, rule string) acp.CreateTerminalResponse {
	var dbID uint
	if b.resolve != nil {
		dbID = b.resolve(params.SessionId)
	}
	display := displayCommand(params.Command, params.Args)
	reason := fmt.Sprintf("命令被安全策略拒绝（命中规则: %s）。该命令属于受限操作，请调整方案后继续。\r\n", rule)
	code := 1

	b.mu.Lock()
	b.seq++
	term := &bridgeTerminal{
		id:       fmt.Sprintf("term-%d", b.seq),
		dbID:     dbID,
		display:  display,
		cwd:      derefStr(params.Cwd),
		limit:    defaultTerminalOutputLimit,
		buf:      []byte(reason),
		exited:   true,
		exitCode: &code,
		done:     make(chan struct{}),
	}
	close(term.done)
	b.terms[term.id] = term
	if dbID != 0 {
		if b.byDB[dbID] == nil {
			b.byDB[dbID] = make(map[string]*bridgeTerminal)
		}
		b.byDB[dbID][term.id] = term
	}
	onExit := b.onExit
	b.mu.Unlock()

	slog.Warn("ACP terminal/create 命中安全策略拒绝", "terminal", term.id, "session", params.SessionId,
		"command", display, "rule", rule)
	b.broadcast(dbID, TerminalEvent{Type: TerminalEventCreated, TerminalID: term.id, Command: display, Cwd: term.cwd})
	b.broadcast(dbID, TerminalEvent{Type: TerminalEventOutput, TerminalID: term.id, Data: []byte(reason)})
	b.broadcast(dbID, TerminalEvent{Type: TerminalEventExit, TerminalID: term.id, ExitCode: &code})
	if onExit != nil {
		onExit(dbID, term.id, display, term.cwd, &code, nil)
	}
	return acp.CreateTerminalResponse{TerminalId: term.id}
}

// derefStr 解引用可空字符串指针（nil 返回空串）。
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// pump 持续读取命令输出：追加缓冲、广播 output 事件；EOF 后 Wait 回收并广播 exit。
func (b *TerminalBridge) pump(term *bridgeTerminal, reader *os.File) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			term.appendOutput(chunk)
			b.broadcast(term.dbID, TerminalEvent{Type: TerminalEventOutput, TerminalID: term.id, Data: chunk})
		}
		if err != nil {
			break // EOF 或 PTY 关闭（进程退出）
		}
	}

	err := term.cmd.Wait()
	var exitCode *int
	var sig *string
	if state := term.cmd.ProcessState; state != nil {
		if code := state.ExitCode(); code >= 0 {
			exitCode = &code
		} else {
			// 被信号终止（ExitCode()==-1）；具体信号名跨平台不可靠，统一记 killed
			s := "killed"
			sig = &s
		}
	} else if err != nil {
		s := err.Error()
		sig = &s
	}

	term.mu.Lock()
	term.exited = true
	term.exitCode = exitCode
	term.signal = sig
	term.mu.Unlock()
	close(term.done)

	slog.Debug("ACP terminal 命令退出", "terminal", term.id, "exit_code", exitCode, "signal", sig)
	b.broadcast(term.dbID, TerminalEvent{Type: TerminalEventExit, TerminalID: term.id, ExitCode: exitCode, Signal: sig})

	b.mu.Lock()
	onExit := b.onExit
	b.mu.Unlock()
	if onExit != nil {
		onExit(term.dbID, term.id, term.display, term.cwd, exitCode, sig)
	}
}

// get 按 terminalId 查找终端。
func (b *TerminalBridge) get(terminalID string) (*bridgeTerminal, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	term, ok := b.terms[terminalID]
	if !ok {
		return nil, fmt.Errorf("terminal %s 不存在或已释放", terminalID)
	}
	return term, nil
}

// Output 执行 terminal/output：返回当前输出与退出状态。
func (b *TerminalBridge) Output(params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	term, err := b.get(params.TerminalId)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	out, truncated := term.snapshotOutput()
	resp := acp.TerminalOutputResponse{Output: string(out), Truncated: truncated}
	if exited, code, sig := term.exitStatus(); exited {
		resp.ExitStatus = &acp.TerminalExitStatus{ExitCode: code, Signal: sig}
	}
	return resp, nil
}

// WaitForExit 执行 terminal/wait_for_exit：阻塞至命令退出或 ctx 取消。
func (b *TerminalBridge) WaitForExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	term, err := b.get(params.TerminalId)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	select {
	case <-term.done:
		_, code, sig := term.exitStatus()
		return acp.WaitForTerminalExitResponse{ExitCode: code, Signal: sig}, nil
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	}
}

// Kill 执行 terminal/kill：终止进程但保留输出缓冲（agent 仍可查询）。
func (b *TerminalBridge) Kill(params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	term, err := b.get(params.TerminalId)
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	term.kill()
	return acp.KillTerminalResponse{}, nil
}

// kill 终止终端进程（幂等；PTY 关闭会给前台进程组发 SIGHUP）。
func (t *bridgeTerminal) kill() {
	if exited, _, _ := t.exitStatus(); exited {
		return
	}
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	if t.closer != nil {
		t.closer()
	}
}

// Release 执行 terminal/release：终止进程（若仍在运行）并释放资源。
func (b *TerminalBridge) Release(params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	b.mu.Lock()
	term, ok := b.terms[params.TerminalId]
	if ok {
		delete(b.terms, params.TerminalId)
		if term.dbID != 0 {
			delete(b.byDB[term.dbID], term.id)
			if len(b.byDB[term.dbID]) == 0 {
				delete(b.byDB, term.dbID)
			}
		}
	}
	b.mu.Unlock()
	if !ok {
		// release 幂等：重复释放不报错
		return acp.ReleaseTerminalResponse{}, nil
	}
	term.kill()
	b.broadcast(term.dbID, TerminalEvent{Type: TerminalEventReleased, TerminalID: term.id})
	return acp.ReleaseTerminalResponse{}, nil
}

// ReleaseSession 释放指定 DB 会话的全部终端（会话删除/连接销毁时调用），防进程泄漏。
func (b *TerminalBridge) ReleaseSession(dbID uint) {
	if dbID == 0 {
		return
	}
	b.mu.Lock()
	terms := make([]*bridgeTerminal, 0, len(b.byDB[dbID]))
	for _, term := range b.byDB[dbID] {
		terms = append(terms, term)
		delete(b.terms, term.id)
	}
	delete(b.byDB, dbID)
	b.mu.Unlock()
	for _, term := range terms {
		term.kill()
	}
}

// Subscribe 按 DB session ID 订阅终端事件。
// 返回订阅时刻的活跃终端快照（按创建顺序，含已缓冲输出）、事件 channel 与取消函数。
func (b *TerminalBridge) Subscribe(dbID uint) ([]TerminalSnapshot, <-chan TerminalEvent, func()) {
	sub := &terminalSubscriber{ch: make(chan TerminalEvent, terminalEventChanSize)}

	b.mu.Lock()
	if b.subs[dbID] == nil {
		b.subs[dbID] = make(map[*terminalSubscriber]struct{})
	}
	b.subs[dbID][sub] = struct{}{}
	terms := make([]*bridgeTerminal, 0, len(b.byDB[dbID]))
	for _, term := range b.byDB[dbID] {
		terms = append(terms, term)
	}
	b.mu.Unlock()

	// 按 terminalId 序号排序，保证前端 tab 顺序与创建顺序一致
	sort.Slice(terms, func(i, j int) bool { return terminalSeq(terms[i].id) < terminalSeq(terms[j].id) })
	snapshots := make([]TerminalSnapshot, 0, len(terms))
	for _, term := range terms {
		out, _ := term.snapshotOutput()
		exited, code, sig := term.exitStatus()
		snapshots = append(snapshots, TerminalSnapshot{
			TerminalID: term.id,
			Command:    term.display,
			Cwd:        term.cwd,
			Output:     out,
			Exited:     exited,
			ExitCode:   code,
			Signal:     sig,
		})
	}

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if subs, ok := b.subs[dbID]; ok {
			if _, exists := subs[sub]; exists {
				delete(subs, sub)
				close(sub.ch)
			}
			if len(subs) == 0 {
				delete(b.subs, dbID)
			}
		}
	}
	return snapshots, sub.ch, cancel
}

// terminalSeq 从 "term-N" 提取序号（解析失败返回 0）。
func terminalSeq(id string) int {
	var n int
	_, _ = fmt.Sscanf(id, "term-%d", &n)
	return n
}

// broadcast 把事件推给该 DB 会话的全部订阅者；buffer 满的慢订阅者非阻塞丢弃。
func (b *TerminalBridge) broadcast(dbID uint, ev TerminalEvent) {
	if dbID == 0 {
		return
	}
	b.mu.Lock()
	snapshot := make([]*terminalSubscriber, 0, len(b.subs[dbID]))
	for sub := range b.subs[dbID] {
		snapshot = append(snapshot, sub)
	}
	b.mu.Unlock()
	for _, sub := range snapshot {
		select {
		case sub.ch <- ev:
		default:
			slog.Warn("terminal 事件订阅者 buffer 满，丢弃消息", "db_session", dbID, "type", ev.Type, "terminal", ev.TerminalID)
		}
	}
}
