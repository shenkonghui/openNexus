package services

import (
	"testing"
	"time"

	"opennexus/internal/models"
)

// TestTaskEvents_SaveNotifiesSubscriber 验证任意经 TaskStore.Save 的写入
// 都会广播变更事件给对应 cwd 的订阅者（前端自动刷新依赖此链路）。
func TestTaskEvents_SaveNotifiesSubscriber(t *testing.T) {
	cwd := t.TempDir()
	ch, cancel := SubscribeTaskChanges(cwd)
	defer cancel()

	store := NewTaskStore(cwd)
	if err := store.UpsertTask(models.TaskManagerTask{ID: "t1", Title: "任务"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("Save 后应收到变更通知")
	}
}

// TestTaskEvents_OtherCwdNotNotified 验证事件按 cwd 隔离：别的工作区写入不触发本订阅。
func TestTaskEvents_OtherCwdNotNotified(t *testing.T) {
	cwd := t.TempDir()
	other := t.TempDir()
	ch, cancel := SubscribeTaskChanges(cwd)
	defer cancel()

	if err := NewTaskStore(other).UpsertTask(models.TaskManagerTask{ID: "x", Title: "别处"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	select {
	case <-ch:
		t.Fatal("其他 cwd 的写入不应通知本订阅者")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestTaskEvents_CancelUnsubscribes 验证 cancel 后不再接收通知（防泄漏）。
func TestTaskEvents_CancelUnsubscribes(t *testing.T) {
	cwd := t.TempDir()
	ch, cancel := SubscribeTaskChanges(cwd)
	cancel()

	if err := NewTaskStore(cwd).UpsertTask(models.TaskManagerTask{ID: "t1", Title: "任务"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	select {
	case <-ch:
		t.Fatal("cancel 后不应再收到通知")
	case <-time.After(100 * time.Millisecond):
	}
}
