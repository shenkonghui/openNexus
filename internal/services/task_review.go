package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"opennexus/internal/acp"
	"opennexus/internal/models"
)

// DefaultTaskReviewPrompt 是编排任务 review 的默认提示词模板。
const DefaultTaskReviewPrompt = `你是一个严格的任务审查助手。请根据任务要求、执行 agent 的最终回复和代码改动，判断任务是否已经真正完成。

任务标题：{{title}}
任务要求：
{{detail}}

执行 agent 的最终回复：
{{result}}

代码改动：
{{diff}}

判断标准：任务要求的功能/修改是否全部落实；代码改动是否与任务要求相符；是否存在明显遗漏或半成品。
仅输出 JSON 对象，例如 {"passed": false, "feedback": "未实现 XX，建议补充 YY"}，不要输出其他任何文字。
passed 为 true 表示任务已完成；为 false 时 feedback 必须给出未通过原因与具体修复建议。`

// DefaultTaskReviewMaxRounds 是全局未配置时的默认最大 review 轮数。
const DefaultTaskReviewMaxRounds = 2

// taskReviewRoundTimeout 是单轮 reviewer 审查（RunSubAgent）的超时。
const taskReviewRoundTimeout = 5 * time.Minute

// taskReviewFixTimeout 是单轮修复对话（向原会话追加 prompt 后等待完成）的超时。
const taskReviewFixTimeout = 5 * time.Minute

// TaskReviewSettingsSource 提供全局任务设置（review 默认配置）。
// *repository.TaskSettingsRepository 实现该接口。
type TaskReviewSettingsSource interface {
	FindByUserID(userID uint) (*models.TaskSettings, error)
}

// SetReviewSettingsSource 注入全局 review 配置来源。不调用时 review 功能整体关闭
//（任务级 Review.Enabled=true 仍可单独启用）。
func (s *TaskManagerService) SetReviewSettingsSource(src TaskReviewSettingsSource) {
	s.reviewSettings = src
}

// taskReviewConfig 是合成后的生效 review 配置（全局默认 + 任务级覆盖）。
type taskReviewConfig struct {
	Enabled    bool
	AgentType  string
	ModelValue string
	MaxRounds  int
	Prompt     string
}

// reviewVerdict 是 reviewer agent 的判定结果。
type reviewVerdict struct {
	Passed   bool   `json:"passed"`
	Feedback string `json:"feedback"`
}

// effectiveReviewConfig 合成任务的生效 review 配置：以全局 TaskSettings 为基底，
// 任务级 t.Review 非 nil 时覆盖开关与 agent/模型/轮数。
// reviewer agent 为空时回退任务自身 agent，再回退首个已注册 agent。
func (s *TaskManagerService) effectiveReviewConfig(t *models.TaskManagerTask, userID uint) taskReviewConfig {
	cfg := taskReviewConfig{MaxRounds: DefaultTaskReviewMaxRounds, Prompt: DefaultTaskReviewPrompt}
	if s.reviewSettings != nil {
		if settings, err := s.reviewSettings.FindByUserID(userID); err == nil && settings != nil {
			cfg.Enabled = settings.ReviewEnabled
			cfg.AgentType = strings.TrimSpace(settings.ReviewAgentType)
			cfg.ModelValue = strings.TrimSpace(settings.ReviewModelValue)
			if settings.ReviewMaxRounds > 0 {
				cfg.MaxRounds = settings.ReviewMaxRounds
			}
			if p := strings.TrimSpace(settings.ReviewPrompt); p != "" {
				cfg.Prompt = p
			}
		} else if err != nil {
			slog.Warn("读取全局 review 配置失败，视为未启用", "task", t.ID, "err", err)
		}
	}
	if t.Review != nil {
		cfg.Enabled = t.Review.Enabled
		if a := strings.TrimSpace(t.Review.AgentType); a != "" {
			cfg.AgentType = a
			cfg.ModelValue = strings.TrimSpace(t.Review.ModelValue)
		}
		if t.Review.MaxRounds > 0 {
			cfg.MaxRounds = t.Review.MaxRounds
		}
	}
	if cfg.AgentType == "" {
		cfg.AgentType = t.AgentType
	}
	if cfg.AgentType == "" {
		cfg.AgentType = s.exec.DefaultAgentType()
	}
	return cfg
}

// reviewTask 在任务会话成功执行后进入 review 循环：
// reviewer 审查 → 通过置 done；未通过则把审查意见追加回原会话继续修复，再审查，
// 直到通过或轮数耗尽（置 failed）。未启用 review 时直接置 done。
// 全程响应 ctx 取消（Stop 已写 canceled，这里只负责退出）。
func (s *TaskManagerService) reviewTask(ctx context.Context, cwd string, t *models.TaskManagerTask, userID uint, result acp.SessionTaskResult) {
	writeSession := func(task *models.TaskManagerTask) {
		task.SessionID = result.SessionID
		if result.DBSessionID > 0 {
			dbID := result.DBSessionID
			task.DBSessionID = &dbID
		}
	}
	rc := s.effectiveReviewConfig(t, userID)
	if !rc.Enabled {
		fin := time.Now()
		s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
			writeSession(task)
			task.Status = models.TaskStatusDone
			task.FinishedAt = &fin
		})
		return
	}

	// 进入审查态；清空上次运行残留的 review 结果
	s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
		writeSession(task)
		task.Status = models.TaskStatusReviewing
		task.ReviewRounds = 0
		task.ReviewPassed = nil
		task.ReviewFeedback = ""
	})

	lastOutput := result.Result
	var feedback string
	for round := 1; round <= rc.MaxRounds; round++ {
		verdict, err := s.runReviewOnce(ctx, rc, t, userID, lastOutput)
		if ctx.Err() != nil {
			return // 被 Stop 取消：状态已由 Stop 写 canceled
		}
		if err != nil {
			// reviewer 异常（调用失败/输出不可解析）：容错视为通过，避免卡死任务
			slog.Warn("review 审查异常，容错视为通过", "task", t.ID, "round", round, "err", err)
			verdict = reviewVerdict{Passed: true, Feedback: ""}
		}
		feedback = strings.TrimSpace(verdict.Feedback)
		passed := verdict.Passed
		rnd := round
		s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
			task.ReviewRounds = rnd
			p := passed
			task.ReviewPassed = &p
			task.ReviewFeedback = feedback
		})
		if passed {
			fin := time.Now()
			s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
				if models.IsTaskRunning(task.Status) {
					task.Status = models.TaskStatusDone
					task.FinishedAt = &fin
				}
			})
			return
		}
		if round == rc.MaxRounds {
			break
		}
		// 把审查意见追加回原会话继续修复，同步等待本轮修复完成
		out, err := s.continueTaskSession(ctx, result.SessionID, feedback)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			fin := time.Now()
			s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
				if models.IsTaskRunning(task.Status) {
					task.Status = models.TaskStatusFailed
					task.FinishedAt = &fin
					task.Error = fmt.Sprintf("review 修复对话失败: %v", err)
				}
			})
			return
		}
		lastOutput = out
	}

	// 轮数耗尽仍未通过
	fin := time.Now()
	s.updateTask(cwd, t.ID, func(task *models.TaskManagerTask) {
		if models.IsTaskRunning(task.Status) {
			task.Status = models.TaskStatusFailed
			task.FinishedAt = &fin
			task.Error = fmt.Sprintf("review 未通过（%d 轮）: %s", rc.MaxRounds, feedback)
		}
	})
}

// runReviewOnce 执行一轮审查：采集 worktree diff → 临时会话调用 reviewer agent → 解析判定。
func (s *TaskManagerService) runReviewOnce(ctx context.Context, rc taskReviewConfig, t *models.TaskManagerTask, userID uint, lastOutput string) (reviewVerdict, error) {
	diff := ""
	if t.WorktreePath != "" {
		if d, err := acp.WorktreeDiff(t.WorktreePath); err == nil {
			diff = d
		} else {
			slog.Warn("review 采集 worktree diff 失败", "task", t.ID, "err", err)
		}
	}
	if diff == "" {
		diff = "（无代码改动）"
	}
	prompt := rc.Prompt
	prompt = strings.ReplaceAll(prompt, "{{title}}", strings.TrimSpace(t.Title))
	prompt = strings.ReplaceAll(prompt, "{{detail}}", strings.TrimSpace(t.Detail))
	prompt = strings.ReplaceAll(prompt, "{{result}}", strings.TrimSpace(lastOutput))
	prompt = strings.ReplaceAll(prompt, "{{diff}}", diff)

	resp, err := s.exec.RunSubAgent(ctx, acp.SubAgentRunConfig{
		AgentType:  rc.AgentType,
		ModelValue: rc.ModelValue,
		Prompt:     prompt,
		UserID:     userID,
		Timeout:    taskReviewRoundTimeout,
	})
	if err != nil {
		return reviewVerdict{}, fmt.Errorf("reviewer 调用失败: %w", err)
	}
	return parseReviewVerdict(resp)
}

// parseReviewVerdict 从 reviewer 输出解析 JSON 判定：剥离代码围栏后先整体解析，
// 失败再提取首个平衡 {} 段解析。
func parseReviewVerdict(raw string) (reviewVerdict, error) {
	text := stripCodeFence(raw)
	var v reviewVerdict
	if err := json.Unmarshal([]byte(text), &v); err == nil {
		return v, nil
	}
	if seg := extractBalanced(text, '{', '}'); seg != "" {
		if err := json.Unmarshal([]byte(seg), &v); err == nil {
			return v, nil
		}
	}
	return reviewVerdict{}, fmt.Errorf("reviewer 输出无法解析为 JSON: %q", firstLine(raw, 120))
}

// continueTaskSession 向原任务会话追加审查意见 prompt，同步消费消息流收集
// agent 回复文本（供下一轮 review 使用）。超时返回已收集的部分。
func (s *TaskManagerService) continueTaskSession(ctx context.Context, sessionID, feedback string) (string, error) {
	prompt := "任务审查未通过，审查意见如下：\n" + feedback + "\n\n请根据审查意见继续修复，确保任务真正完成。"
	ch, err := s.exec.Prompt(ctx, sessionID, prompt)
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, taskReviewFixTimeout)
	defer cancel()

	var sb strings.Builder
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return strings.TrimSpace(sb.String()), nil
			}
			if msg.Kind == models.MessageKindAgentMessageChunk {
				sb.WriteString(msg.Content)
			}
		case <-runCtx.Done():
			if sb.Len() > 0 {
				return strings.TrimSpace(sb.String()), nil
			}
			return "", fmt.Errorf("修复会话响应超时")
		}
	}
}
