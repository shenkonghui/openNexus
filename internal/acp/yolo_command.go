package acp

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
)

// 会话级 YOLO 的内置 slash 命令：/yolo-on、/yolo-off、/yolo-status。
// 与 /goal 相同的客户端侧拦截机制：命令在 client 层处理并合成回复，
// 不发给 agent；拦截优先于 agent 原生同名命令，旧前缀名 /opennexus-yolo-*
// 仍作为兼容别名被解析（不在 "/" 弹窗展示）。
// on/off 可携带任务内容一起发送：切换 YOLO 后剩余文本作为 prompt 继续发给 agent。

// 会话级 YOLO 开关的 slash 命令名。
const (
	yoloOnCommandName     = "yolo-on"
	yoloOffCommandName    = "yolo-off"
	yoloStatusCommandName = "yolo-status"
)

// 历史带前缀命令名，仅兼容解析（存量任务 detail / 用户习惯）。
const (
	legacyYoloOnCommandName     = "opennexus-yolo-on"
	legacyYoloOffCommandName    = "opennexus-yolo-off"
	legacyYoloStatusCommandName = "opennexus-yolo-status"
)

// parseYoloCommand 解析 "/yolo-*"（含兼容别名 /opennexus-yolo-*）输入。ok=false 表示不是 yolo 命令。
// action 取值：on / off / status；rest 是命令后携带的任务内容（可为空）。
func parseYoloCommand(prompt string) (action, rest string, ok bool) {
	trimmed := strings.TrimSpace(prompt)
	for _, c := range []struct{ name, action string }{
		{yoloOnCommandName, "on"},
		{yoloOffCommandName, "off"},
		{yoloStatusCommandName, "status"},
		{legacyYoloOnCommandName, "on"},
		{legacyYoloOffCommandName, "off"},
		{legacyYoloStatusCommandName, "status"},
	} {
		cmd := "/" + c.name
		if trimmed == cmd {
			return c.action, "", true
		}
		if strings.HasPrefix(trimmed, cmd+" ") || strings.HasPrefix(trimmed, cmd+"\n") {
			return c.action, strings.TrimSpace(strings.TrimPrefix(trimmed, cmd)), true
		}
	}
	return "", "", false
}

// builtinYoloCommands 是内置 yolo 命令描述，供 "/" 弹窗展示（所有 agent 通用）。
func builtinYoloCommands() []acp.AvailableCommand {
	taskInput := &acp.AvailableCommandInput{
		Unstructured: &acp.UnstructuredCommandInput{Hint: "[任务内容，可选]"},
	}
	return []acp.AvailableCommand{
		{
			Name:        yoloOnCommandName,
			Description: "开启本会话 YOLO（自动批准权限请求），可附带任务内容一起发送",
			Input:       taskInput,
		},
		{
			Name:        yoloOffCommandName,
			Description: "关闭本会话 YOLO（权限恢复人工确认），可附带任务内容一起发送",
			Input:       taskInput,
		},
		{
			Name:        yoloStatusCommandName,
			Description: "查看本会话 YOLO 状态",
		},
	}
}

// interceptYolo 在 PromptWithExecution 入口处拦截 /yolo-* 命令。
// 返回 handled=true 时调用方直接返回 ch（合成回复，不打扰 agent）；
// on/off 携带任务内容时切换 YOLO 后返回 handled=false，并把 *promptForAgent
// 改写为剩余任务内容，继续正常发送流程（与 goal set 一致）。
func (s *Service) interceptYolo(session *models.Session, sessionID, prompt string, executionID *uint, promptForAgent *string) (handled bool, ch <-chan models.Message) {
	action, rest, ok := parseYoloCommand(prompt)
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
		slog.Info("会话 YOLO 已切换", "session", sessionID, "yolo", updated.Yolo, "with_task", rest != "")
		if rest != "" {
			*promptForAgent = rest
			return false, nil
		}
		if updated.Yolo {
			return true, s.syntheticCommandReply(session, prompt, "⚡ 本会话 YOLO 已开启：权限请求将按全局名单自动批准。用 /yolo-off 关闭。", executionID)
		}
		return true, s.syntheticCommandReply(session, prompt, "✅ 本会话 YOLO 已关闭：权限请求恢复人工确认。", executionID)
	default: // status
		state := "关闭"
		if session.Yolo {
			state = "开启"
		}
		return true, s.syntheticCommandReply(session, prompt, fmt.Sprintf("本会话 YOLO 当前为：%s。用 /yolo-on 或 /yolo-off 切换。", state), executionID)
	}
}
