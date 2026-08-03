package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"opennexus/internal/database"
	"opennexus/internal/middleware"
	"opennexus/internal/repository"
)

func setupAgentPrefsRouter(t *testing.T, userID uint) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	db.Exec("DELETE FROM user_agent_prefs")
	h := NewAgentPrefsHandler(repository.NewUserAgentPrefsRepository(db))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.UserIDKey(), userID)
		c.Next()
	})
	g := r.Group("/api/v1")
	g.GET("/agent-prefs", h.Get)
	g.PATCH("/agent-prefs", h.Patch)
	return r
}

func TestAgentPrefs_GetEmpty_AndPatchMerge(t *testing.T) {
	r := setupAgentPrefsRouter(t, 7)

	w := doJSON(t, r, "GET", "/api/v1/agent-prefs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	var empty struct {
		Data struct {
			LastAgentType string                       `json:"last_agent_type"`
			Prefs         map[string]map[string]string `json:"prefs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Data.LastAgentType != "" || len(empty.Data.Prefs) != 0 {
		t.Fatalf("期望空默认: %+v", empty.Data)
	}

	w = doJSON(t, r, "PATCH", "/api/v1/agent-prefs", gin.H{
		"last_agent_type": "claude-code",
		"agent_type":      "claude-code",
		"configs":         gin.H{"model": "m1", "mode": "ask"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH status=%d body=%s", w.Code, w.Body.String())
	}
	var patched struct {
		Data struct {
			LastAgentType string                       `json:"last_agent_type"`
			Prefs         map[string]map[string]string `json:"prefs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Data.LastAgentType != "claude-code" {
		t.Fatalf("last=%q", patched.Data.LastAgentType)
	}
	if patched.Data.Prefs["claude-code"]["model"] != "m1" || patched.Data.Prefs["claude-code"]["mode"] != "ask" {
		t.Fatalf("prefs=%v", patched.Data.Prefs)
	}

	w = doJSON(t, r, "PATCH", "/api/v1/agent-prefs", gin.H{
		"agent_type": "claude-code",
		"configs":    gin.H{"mode": ""},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH delete status=%d body=%s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &patched)
	if _, ok := patched.Data.Prefs["claude-code"]["mode"]; ok {
		t.Fatalf("mode 应删除: %v", patched.Data.Prefs)
	}
	if patched.Data.Prefs["claude-code"]["model"] != "m1" {
		t.Fatalf("model 应保留: %v", patched.Data.Prefs)
	}
}
