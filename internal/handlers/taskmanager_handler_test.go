package handlers

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"opennexus/internal/middleware"
	"opennexus/internal/models"
	"opennexus/internal/services"
)

// stubWorkspaceStore 返回固定工作区，用于编排 handler 测试解析 cwd/归属。
type stubWorkspaceStore struct {
	ws *models.Workspace
}

func (s *stubWorkspaceStore) FindWorkspaceByID(_ uint) (*models.Workspace, error) {
	return s.ws, nil
}

func setupTMRouter(t *testing.T, userID uint, cwd string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := services.NewTaskManagerService(nil)
	ws := &models.Workspace{UserID: userID, Cwd: cwd}
	ws.ID = 1
	h := NewTaskManagerHandler(svc, &stubWorkspaceStore{ws: ws})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.UserIDKey(), userID)
		c.Next()
	})
	g := r.Group("/api/v1")
	g.GET("/taskmanager", h.Get)
	return r
}

// TestGetTaskManagerHandler 验证 GET /taskmanager 返回空任务定义（tasks.json 不存在时）。
func TestGetTaskManagerHandler(t *testing.T) {
	cwd := t.TempDir()
	r := setupTMRouter(t, 9, cwd)

	w := doJSON(t, r, "GET", "/api/v1/taskmanager?workspace_id=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
}
