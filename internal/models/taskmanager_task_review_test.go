package models

import (
	"encoding/json"
	"testing"
)

// TestIsTaskRunning 验证运行态判定：reviewing 计入运行中（占用执行槽位、Stop 可取消）。
func TestIsTaskRunning(t *testing.T) {
	running := []string{TaskStatusQueued, TaskStatusRunning, TaskStatusReviewing}
	for _, st := range running {
		if !IsTaskRunning(st) {
			t.Errorf("IsTaskRunning(%q) = false, want true", st)
		}
	}
	notRunning := []string{TaskStatusPending, TaskStatusDone, TaskStatusFailed, TaskStatusCanceled, TaskStatusInterrupt, ""}
	for _, st := range notRunning {
		if IsTaskRunning(st) {
			t.Errorf("IsTaskRunning(%q) = true, want false", st)
		}
	}
}

// TestTaskReviewConfigJSONRoundTrip 验证任务级 review 配置随任务 JSON 序列化往返，
// 且未设置时（nil）不输出 review 字段。
func TestTaskReviewConfigJSONRoundTrip(t *testing.T) {
	passed := true
	task := TaskManagerTask{
		ID:     "t1",
		Title:  "T",
		Detail: "d",
		Review: &TaskReviewConfig{Enabled: true, AgentType: "claude-code", ModelValue: "sonnet", MaxRounds: 3},

		ReviewRounds:   2,
		ReviewPassed:   &passed,
		ReviewFeedback: "ok",
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got TaskManagerTask
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Review == nil || !got.Review.Enabled || got.Review.AgentType != "claude-code" ||
		got.Review.ModelValue != "sonnet" || got.Review.MaxRounds != 3 {
		t.Fatalf("Review 配置往返丢失: %+v", got.Review)
	}
	if got.ReviewRounds != 2 || got.ReviewPassed == nil || !*got.ReviewPassed || got.ReviewFeedback != "ok" {
		t.Fatalf("Review 运行时字段往返丢失: rounds=%d passed=%v feedback=%q", got.ReviewRounds, got.ReviewPassed, got.ReviewFeedback)
	}

	// 未配置 review 的任务不应输出 review 字段（nil = 跟随全局）
	data, err = json.Marshal(TaskManagerTask{ID: "t2", Title: "T", Detail: "d"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal map: %v", err)
	}
	if _, ok := m["review"]; ok {
		t.Fatalf("nil Review 不应序列化 review 字段: %s", data)
	}
}
