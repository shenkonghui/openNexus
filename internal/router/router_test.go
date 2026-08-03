package router

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/acp"
	"opennexus/internal/agent"
	"opennexus/internal/config"
	"opennexus/internal/database"
	"opennexus/internal/handlers"
	"opennexus/internal/logging"
	"opennexus/internal/models"
	"opennexus/internal/repository"
	"opennexus/internal/services"
)

// noopRegistrar 是测试用的空 AgentRegistrar。
type noopRegistrar struct{}

func (noopRegistrar) RegisterBackend(acp.Backend)                {}
func (noopRegistrar) ReplaceBackend(acp.Backend)                 {}
func (noopRegistrar) UnregisterBackend(string)                   {}
func (noopRegistrar) RegisterAgent(*agent.AgentDescriptor) error { return nil }
func (noopRegistrar) ReplaceAgent(*agent.AgentDescriptor)        {}
func (noopRegistrar) UnregisterAgent(string)                     {}
func (noopRegistrar) PreconnectAgent(string)                     {}

// noopSchedulerMgr 是测试用的空 SchedulerManager。
type noopSchedulerMgr struct{}

func (noopSchedulerMgr) AddTask(string, *models.TaskManagerTask) error    { return nil }
func (noopSchedulerMgr) UpdateTask(string, *models.TaskManagerTask) error { return nil }
func (noopSchedulerMgr) RemoveTask(string, string) error                  { return nil }
func (noopSchedulerMgr) RunTask(string, string) error                     { return nil }

func TestSetup_RegistersP5Routes(t *testing.T) {
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	jwtSvc := services.NewJWTService("this-is-a-very-long-jwt-secret-key-32+bytes!", 15*time.Minute, time.Hour)
	authSvc := services.NewAuthService(db, jwtSvc, 10)
	agentRouter := agent.NewRouter(agent.NewRegistry(), nil)
	agentCfgH := handlers.NewAgentConfigHandler(repository.NewAgentConfigRepository(db), noopRegistrar{})
	wsRepo := repository.NewWorkspaceRepository(db)
	schedTaskH := handlers.NewScheduledTaskHandler(wsRepo, noopSchedulerMgr{})

	skillsCfg := config.SkillsConfig{UserDirs: []string{t.TempDir()}}
	commandsCfg := config.CommandsConfig{UserDirs: []string{t.TempDir()}}
	rulesCfg := config.RulesConfig{UserDirs: []string{t.TempDir()}}
	noteRepo := repository.NewNoteRepository(db)
	noteSettingsRepo := repository.NewNoteSettingsRepository(db)
	noteH := handlers.NewNoteHandler(noteRepo, noteSettingsRepo, nil, "", "")
	taskSettingsRepo := repository.NewTaskSettingsRepository(db)
	taskSettingsH := handlers.NewTaskSettingsHandler(taskSettingsRepo)
	goalSettingsH := handlers.NewGoalSettingsHandler(repository.NewGoalSettingsRepository(db))
	agentPrefsH := handlers.NewAgentPrefsHandler(repository.NewUserAgentPrefsRepository(db))
	logH := handlers.NewLogHandler(logging.NewLogHub(0))
	engine := Setup(authSvc, jwtSvc, agentRouter, agentCfgH, nil, schedTaskH, noteH, taskSettingsH, goalSettingsH, agentPrefsH, nil, nil, logH, nil, nil, nil, nil, nil, nil, nil, nil, skillsCfg, commandsCfg, rulesCfg, config.SubAgentsConfig{}, config.SelectorConfig{}, gin.TestMode, "", false)

	want := []string{
		"GET /api/v1/agents",
		"POST /api/v1/sessions",
		"GET /api/v1/sessions",
		"GET /api/v1/sessions/:id",
		"GET /api/v1/sessions/:id/connection",
		"DELETE /api/v1/sessions/:id",
		"POST /api/v1/sessions/:id/prompt",
		"POST /api/v1/sessions/:id/cancel",
		"POST /api/v1/sessions/:id/resume",
		"GET /api/v1/sessions/:id/messages",
		"GET /api/v1/sessions/:id/commands",
		"GET /api/v1/sessions/:id/config-options",
		"POST /api/v1/sessions/:id/config-options",
		"GET /api/v1/agent-configs",
		"POST /api/v1/agent-configs",
		"PUT /api/v1/agent-configs/:id",
		"DELETE /api/v1/agent-configs/:id",
		"POST /api/v1/scheduled-tasks",
		"GET /api/v1/scheduled-tasks",
		"GET /api/v1/scheduled-tasks/:id",
		"PUT /api/v1/scheduled-tasks/:id",
		"DELETE /api/v1/scheduled-tasks/:id",
		"POST /api/v1/scheduled-tasks/:id/run",
		"GET /api/v1/scheduled-tasks/:id/executions",
		"GET /api/v1/logs/stream",
		"GET /api/v1/agent-prefs",
		"PATCH /api/v1/agent-prefs",
		"GET /api/v1/goal/settings",
		"PUT /api/v1/goal/settings",
	}
	got := make(map[string]bool)
	for _, ri := range engine.Routes() {
		got[ri.Method+" "+ri.Path] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("缺少路由 %s", w)
		}
	}
}
