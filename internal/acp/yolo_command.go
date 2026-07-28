package acp

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
)

// 会话级 YOLO 的内置 slash 命令：/yolo-opennexus [on|off|status]。
// 与 /goal-opennexus 相同的客户端侧拦截机制：命令在 client 层处理并合成回复，
// 不发给 agent；命令名带 -opennexus 后缀避免与 agent 原生命令冲突。

// yoloCommandName 是会话级 YOLO 开关的 slash 命令名。
const yoloCommandName = "yolo-opennexus"

// yoloOnAliases / yoloOffAliases 是开启/关闭 YOLO 的子命令别名。
var (
	yoloOnAliases  = map[string]bool{"on": true, "enable": true, "true": true, "1": true}
	yoloOffAliases = map[string]bool{"off": true, "disable": true, "false": true, "0": true, "clear": true, "stop": true}
)

// parseYoloCommand 解析 "/yolo-opennexus ..." 输入。ok=false 表示不是 yolo 命令。
// action 取值：on / off / status（空参数=status）。无法识别的参数返回 help。
func parseYoloCommand(prompt string) (action string, ok bool) {
	trimmed := strings.TrimSpace(prompt)
	cmd := "/" + yoloCommandName
	if trimmed != cmd && !strings.HasPrefix(trimmed, cmd+" ") && !strings.HasPrefix(trimmed, cmd+"\n") {
		return "", false
	}
	rest := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, cmd)))
	switch {
	case rest == "" || rest == "status":
		return "status", true
	case yoloOnAliases[rest]:
		return "on", true
	case yoloOffAliases[rest]:
		return "off", true
	default:
		return "help", true
	}
}

// builtinYoloCommand 是内置 yolo 命令描述，供 "/" 弹窗展示（所有 agent 通用）。
func builtinYoloCommand() acp.AvailableCommand {
	return acp.AvailableCommand{
		Name:        yoloCommandName,
		Description: "开关本会话 YOLO（自动批准权限请求）：on 开启 / off 关闭 / status 查看",
		Input: &acp.AvailableCommandInput{
			Unstructured: &acp.UnstructuredCommandInput{Hint: "on | off | status"},
		},
	}
}

// interceptYolo 在 PromptWithExecution 入口处拦截 /yolo-opennexus 命令。
// yolo 命令总是本地处理（handled=true 且返回合成回复 channel），不会发给 agent。
func (s *Service) interceptYolo(session *models.Session, sessionID, prompt string, executionID *uint) (handled bool, ch <-chan models.Message) {
	action, ok := parseYoloCommand(prompt)
	if !ok {
		return false, nil
	}
	switch action {
	case "on", "off":
		enable := action == "on"
		updated, err := s.SetSessionYolo(session.ID, enable)
		if err != nil {
			return true, s.syntheticCommandReply(session, prompt, fmt.Sprintf("⚠️ 设置 YOLO 失败：%v", err), executionID)
		}
		slog.Info("会话 YOLO 已切换", "session", sessionID, "yolo", updated.Yolo)
		if updated.Yolo {
			return true, s.syntheticCommandReply(session, prompt, "⚡ 本会话 YOLO 已开启：权限请求将按全局名单自动批准。用 /yolo-opennexus off 关闭。", executionID)
		}
		return true, s.syntheticCommandReply(session, prompt, "✅ 本会话 YOLO 已关闭：权限请求恢复人工确认。", executionID)
	case "status":
		state := "关闭"
		if session.Yolo {
			state = "开启"
		}
		return true, s.syntheticCommandReply(session, prompt, fmt.Sprintf("本会话 YOLO 当前为：%s。用 /yolo-opennexus on|off 切换。", state), executionID)
	default: // help：无法识别的参数
		return true, s.syntheticCommandReply(session, prompt, "用法：/yolo-opennexus on|off|status", executionID)
	}
}
