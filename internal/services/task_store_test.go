package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"opennexus/internal/models"
	"opennexus/internal/workspacemeta"
)

// TestTaskStore_MigratesLegacyTasksJSON 验证设置管理数据根目录后，
// 旧版落在 cwd 内的 tasks.json 与执行记录 JSONL 首次访问时自动搬迁到管理数据目录。
func TestTaskStore_MigratesLegacyTasksJSON(t *testing.T) {
	cwd := t.TempDir()
	legacyDef := &models.TaskManagerDef{
		MaxParallel: 2,
		Tasks:       []models.TaskManagerTask{{ID: "t1", Title: "旧任务", Status: models.TaskStatusPending}},
	}
	data, err := json.Marshal(legacyDef)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, tasksFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	legacyExecDir := filepath.Join(cwd, scheduledExecutionsDir)
	if err := os.MkdirAll(legacyExecDir, 0o755); err != nil {
		t.Fatal(err)
	}
	execLine := `{"task_id":"t1","status":"success"}` + "\n"
	if err := os.WriteFile(filepath.Join(legacyExecDir, scheduledExecutionsFileName), []byte(execLine), 0o644); err != nil {
		t.Fatal(err)
	}

	workspacemeta.SetRoot(t.TempDir())
	t.Cleanup(func() { workspacemeta.SetRoot("") })

	store := NewTaskStore(cwd)
	def, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(def.Tasks) != 1 || def.Tasks[0].ID != "t1" {
		t.Fatalf("迁移后读取任务失败: %+v", def.Tasks)
	}

	metaDir := workspacemeta.DirFor(cwd)
	if metaDir == cwd {
		t.Fatal("root 已设置，DirFor 不应回退到 cwd")
	}
	if _, err := os.Stat(filepath.Join(metaDir, tasksFileName)); err != nil {
		t.Errorf("新位置缺少 tasks.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, tasksFileName)); !os.IsNotExist(err) {
		t.Errorf("旧位置 tasks.json 应已搬走, err=%v", err)
	}

	execs, err := store.ListExecutions("t1")
	if err != nil {
		t.Fatalf("ListExecutions: %v", err)
	}
	if len(execs) != 1 {
		t.Errorf("迁移后执行记录数 = %d, 期望 1", len(execs))
	}
	if _, err := os.Stat(filepath.Join(legacyExecDir, scheduledExecutionsFileName)); !os.IsNotExist(err) {
		t.Errorf("旧位置执行记录应已搬走, err=%v", err)
	}
}

// TestTaskStore_NoRootKeepsLegacyLayout 验证 root 未设置时保持旧行为：文件仍落在 cwd 内。
func TestTaskStore_NoRootKeepsLegacyLayout(t *testing.T) {
	cwd := t.TempDir()
	store := NewTaskStore(cwd)
	if err := store.Save(&models.TaskManagerDef{MaxParallel: 1}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, tasksFileName)); err != nil {
		t.Errorf("root 未设置时 tasks.json 应落在 cwd 内: %v", err)
	}
}

// TestTaskStore_NewWorkspaceWritesToMetaDir 验证无旧数据时直接写入管理数据目录。
func TestTaskStore_NewWorkspaceWritesToMetaDir(t *testing.T) {
	cwd := t.TempDir()
	workspacemeta.SetRoot(t.TempDir())
	t.Cleanup(func() { workspacemeta.SetRoot("") })

	store := NewTaskStore(cwd)
	if err := store.UpsertTask(models.TaskManagerTask{ID: "n1", Title: "新任务"}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspacemeta.DirFor(cwd), tasksFileName)); err != nil {
		t.Errorf("tasks.json 应落在管理数据目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, tasksFileName)); !os.IsNotExist(err) {
		t.Errorf("cwd 内不应产生 tasks.json, err=%v", err)
	}
}
