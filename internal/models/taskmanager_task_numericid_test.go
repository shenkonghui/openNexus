package models

import (
	"encoding/json"
	"testing"
)

func TestTaskManagerDef_NumericTaskID(t *testing.T) {
	data := []byte(`{
  "max_parallel": 3,
  "parent_session_id": 84,
  "tasks": [
    {"id": 1, "title": "a", "detail": "d", "status": "pending", "devops_id": "310016", "sprint": "s", "priority": 2, "depends_on": [3, "t4"]},
    {"id": 2, "title": "b", "detail": "d", "status": "pending"}
  ]
}`)
	var def TaskManagerDef
	if err := json.Unmarshal(data, &def); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got := def.Tasks[0].ID; got != "1" {
		t.Errorf("task0 id = %q, want \"1\"", got)
	}
	if got := def.Tasks[1].ID; got != "2" {
		t.Errorf("task1 id = %q, want \"2\"", got)
	}
	if len(def.Tasks[0].DependsOn) != 2 || def.Tasks[0].DependsOn[0] != "3" || def.Tasks[0].DependsOn[1] != "t4" {
		t.Errorf("task0 depends_on = %v, want [3 t4]", def.Tasks[0].DependsOn)
	}
	if got := def.Tasks[0].Priority; got != TaskPriorityP2 {
		t.Errorf("task0 priority = %q, want %q", got, TaskPriorityP2)
	}
}

func TestNormalizeTaskPriority(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", TaskPriorityP1},
		{"p0", TaskPriorityP0},
		{"P0", TaskPriorityP0},
		{"0", TaskPriorityP0},
		{"p1", TaskPriorityP1},
		{"2", TaskPriorityP2},
		{"p2", TaskPriorityP2},
		{"x", TaskPriorityP1},
	}
	for _, c := range cases {
		if got := NormalizeTaskPriority(c.in); got != c.want {
			t.Errorf("NormalizeTaskPriority(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
