package models

import (
	"encoding/json"
	"testing"
	"time"
)

// 回归：ArchivedTask 内嵌 TaskManagerTask 后其 UnmarshalJSON 会被提升，
// 若不显式覆盖，archived_at 反序列化后为零值，归档条目会被过期清理立即误删。
func TestArchivedTask_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	src := ArchivedTask{
		TaskManagerTask: TaskManagerTask{ID: "t1", Title: "标题", Status: "done"},
		ArchivedAt:      now,
	}
	data, err := json.Marshal([]ArchivedTask{src})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var list []ArchivedTask
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	if got := list[0].ID; got != "t1" {
		t.Errorf("id = %q, want \"t1\"", got)
	}
	if list[0].ArchivedAt.IsZero() || !list[0].ArchivedAt.Equal(now) {
		t.Errorf("archived_at = %v, want %v（零值说明被内嵌 UnmarshalJSON 吞掉）", list[0].ArchivedAt, now)
	}
}

// 兼容数字 id 的容错反序列化在 ArchivedTask 上同样生效。
func TestArchivedTask_NumericID(t *testing.T) {
	data := []byte(`[{"id": 7, "title": "a", "status": "pending", "archived_at": "2026-07-29T10:00:00Z"}]`)
	var list []ArchivedTask
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got := list[0].ID; got != "7" {
		t.Errorf("id = %q, want \"7\"", got)
	}
	if list[0].ArchivedAt.IsZero() {
		t.Error("archived_at 为零值")
	}
}
