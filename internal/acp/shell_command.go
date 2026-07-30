package acp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
)

// 内置 /shell 命令：在会话工作目录直接执行 shell 命令并把输出作为回复展示。
// 与 /goal、/yolo-* 相同的客户端侧拦截机制：命令在 client 层处理并合成回复，
// 不发给 agent，无需 agent 连接即可运行。

const shellCommandName = "shell"

// shell 命令执行超时与输出上限（避免命令挂死或输出过大撑爆消息）。
const (
	shellCommandTimeout  = 120 * time.Second
	shellCommandMaxBytes = 64 << 10 // 64KB
)

// parseShellCommand 解析 "/shell <命令>" 输入。ok=false 表示不是 shell 命令。
// cmdline 是 /shell 后携带的完整命令行（可为空，表示未提供命令）。
func parseShellCommand(prompt string) (cmdline string, ok bool) {
	trimmed := strings.TrimSpace(prompt)
	cmd := "/" + shellCommandName
	if trimmed == cmd {
		return "", true
	}
	if strings.HasPrefix(trimmed, cmd+" ") || strings.HasPrefix(trimmed, cmd+"\n") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, cmd)), true
	}
	return "", false
}

// builtinShellCommand 是内置 shell 命令描述，供 "/" 弹窗展示（所有 agent 通用）。
func builtinShellCommand() acp.AvailableCommand {
	return acp.AvailableCommand{
		Name:        shellCommandName,
		Description: "在会话工作目录直接执行 shell 命令并返回输出（不经过 agent）",
		Input: &acp.AvailableCommandInput{
			Unstructured: &acp.UnstructuredCommandInput{Hint: "<要执行的命令>"},
		},
	}
}

// shellExecCommand 按平台构造非交互式 shell 命令：unix 用 sh -c，windows 用 cmd /C。
func shellExecCommand(ctx context.Context, cmdline string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", cmdline)
	}
	shell := "/bin/sh"
	if p, err := exec.LookPath("bash"); err == nil {
		shell = p
	}
	return exec.CommandContext(ctx, shell, "-c", cmdline)
}

// interceptShell 在 PromptWithExecution 入口处拦截 /shell 命令。
// 命中即在会话工作目录执行命令并把输出合成回复返回（handled=true），不打扰 agent。
func (s *Service) interceptShell(session *models.Session, sessionID, prompt string, executionID *uint) (handled bool, ch <-chan models.Message) {
	cmdline, ok := parseShellCommand(prompt)
	if !ok {
		return false, nil
	}
	if cmdline == "" {
		return true, s.syntheticCommandReply(session, prompt, "⚠️ 用法：/shell <要执行的命令>", executionID)
	}

	cwd := sessionCwd(session, s.workspaces)
	ctx, cancel := context.WithTimeout(context.Background(), shellCommandTimeout)
	defer cancel()

	cmd := shellExecCommand(ctx, cmdline)
	cmd.Dir = cwd
	cmd.Env = EnsureUTF8Locale(os.Environ())
	out, runErr := cmd.CombinedOutput()

	// 输出过大时截断，避免撑爆消息与前端渲染。
	body := string(out)
	if len(body) > shellCommandMaxBytes {
		body = body[:shellCommandMaxBytes] + "\n…（输出过长已截断）"
	}
	body = strings.TrimRight(body, "\n")

	var status string
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		status = fmt.Sprintf("⏱️ 命令超时（>%s）已终止", shellCommandTimeout)
	case runErr != nil:
		if exitErr, isExit := runErr.(*exec.ExitError); isExit {
			status = fmt.Sprintf("退出码：%d", exitErr.ExitCode())
		} else {
			status = "执行失败：" + runErr.Error()
		}
	default:
		status = "退出码：0"
	}

	slog.Info("已执行 /shell 命令", "session", sessionID, "cwd", cwd, "bytes", len(out), "err", runErr)

	var sb strings.Builder
	sb.WriteString("```console\n$ ")
	sb.WriteString(cmdline)
	sb.WriteString("\n")
	if body != "" {
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	sb.WriteString("```\n")
	sb.WriteString(status)
	return true, s.syntheticCommandReply(session, prompt, sb.String(), executionID)
}
