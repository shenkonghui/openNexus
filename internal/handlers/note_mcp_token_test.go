package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"opennexus/internal/database"
	"opennexus/internal/middleware"
	"opennexus/internal/repository"
)

func setupNoteMCPRouter(t *testing.T, userID uint, mcpPath string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Connect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	db.Exec("DELETE FROM note_settings")
	db.Exec("DELETE FROM notes")
	h := NewNoteHandler(
		repository.NewNoteRepository(db),
		repository.NewNoteSettingsRepository(db),
		nil,
		mcpPath,
		"http://127.0.0.1:8008",
	)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.UserIDKey(), userID)
		c.Next()
	})
	notes := r.Group("/api/v1/notes")
	notes.GET("/settings", h.GetSettings)
	notes.POST("/settings/mcp-token", h.GenerateMCPToken)
	return r
}

// newNoteHandlerForTest 构造一个绑定指定 DB 的 NoteHandler，供非 router 的单元测试使用。
func newNoteHandlerForTest(t *testing.T, mcpPath string) (*NoteHandler, *repository.NoteSettingsRepository) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Connect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	db.Exec("DELETE FROM note_settings")
	db.Exec("DELETE FROM notes")
	settingsRepo := repository.NewNoteSettingsRepository(db)
	h := NewNoteHandler(repository.NewNoteRepository(db), settingsRepo, nil, mcpPath, "http://127.0.0.1:8008")
	return h, settingsRepo
}

func TestGenerateMCPToken_Once(t *testing.T) {
	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	r := setupNoteMCPRouter(t, 42, mcpPath)

	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, httptest.NewRequest(http.MethodPost, "/api/v1/notes/settings/mcp-token", nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("首次生成 status=%d body=%s", w1.Code, w1.Body.String())
	}
	var resp1 struct {
		Data struct {
			McpToken string `json:"mcp_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &resp1); err != nil || resp1.Data.McpToken == "" {
		t.Fatalf("解析 token 失败: %v body=%s", err, w1.Body.String())
	}

	// 生成 token 后应自动写入 opennexus-notes 条目到 mcp.json
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("mcp.json 未被创建: %v", err)
	}
	var file struct {
		McpServers map[string]struct {
			Type    string            `json:"type"`
			Url     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("mcp.json 非法 JSON: %v body=%s", err, data)
	}
	entry, ok := file.McpServers[NotesMCPName]
	if !ok {
		t.Fatalf("mcp.json 缺少 %q 条目: %s", NotesMCPName, data)
	}
	if entry.Type != "http" {
		t.Errorf("entry.Type = %q, 期望 http", entry.Type)
	}
	if entry.Url != "http://127.0.0.1:8008/mcp/notes" {
		t.Errorf("entry.Url = %q", entry.Url)
	}
	if entry.Headers["Authorization"] != "Bearer "+resp1.Data.McpToken {
		t.Errorf("Authorization = %q, 期望 Bearer %s", entry.Headers["Authorization"], resp1.Data.McpToken)
	}

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/api/v1/notes/settings/mcp-token", nil))
	if w2.Code != http.StatusConflict {
		t.Fatalf("再次生成 status=%d, 期望 409", w2.Code)
	}

	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/api/v1/notes/settings", nil))
	if w3.Code != http.StatusOK {
		t.Fatalf("GetSettings status=%d", w3.Code)
	}
	var resp3 struct {
		Data struct {
			McpToken string `json:"mcp_token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w3.Body.Bytes(), &resp3)
	if resp3.Data.McpToken != resp1.Data.McpToken {
		t.Fatalf("settings.mcp_token=%q, 期望 %q", resp3.Data.McpToken, resp1.Data.McpToken)
	}
}

// TestSyncAllNotesMCP_BackfillsExistingToken 验证启动同步逻辑：
// 即使 token 是在自动写入功能之前生成的（直接写库），SyncAllNotesMCP 也能补写到 mcp.json。
func TestSyncAllNotesMCP_BackfillsExistingToken(t *testing.T) {
	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	h, settingsRepo := newNoteHandlerForTest(t, mcpPath)

	// 模拟存量场景：直接写库设置 token（不经 GenerateMCPToken）
	existingToken := "preexisting-token-abc"
	if err := settingsRepo.SetMCPTokenOnce(99, existingToken); err != nil {
		t.Fatalf("预设 token 失败: %v", err)
	}

	// mcp.json 此时应不存在
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("同步前 mcp.json 应不存在")
	}

	// 执行启动同步
	h.SyncAllNotesMCP()

	// 验证 mcp.json 已含 opennexus-notes 条目
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("mcp.json 未被创建: %v", err)
	}
	var file struct {
		McpServers map[string]struct {
			Url     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("mcp.json 非法: %v", err)
	}
	entry, ok := file.McpServers[NotesMCPName]
	if !ok {
		t.Fatalf("缺少 %q 条目: %s", NotesMCPName, data)
	}
	if entry.Headers["Authorization"] != "Bearer "+existingToken {
		t.Errorf("Authorization = %q, 期望 Bearer %s", entry.Headers["Authorization"], existingToken)
	}
}

// TestSyncAllNotesMCP_NoTokenSkips 验证无 token 时不写入。
func TestSyncAllNotesMCP_NoTokenSkips(t *testing.T) {
	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	h, _ := newNoteHandlerForTest(t, mcpPath)

	// 无任何 token，同步应无副作用
	h.SyncAllNotesMCP()
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("无 token 时不应创建 mcp.json")
	}
}

// TestSyncAllNotesMCP_SkipsWhenConsistent 验证条目已存在且 token 一致时不重写文件。
func TestSyncAllNotesMCP_SkipsWhenConsistent(t *testing.T) {
	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	h, settingsRepo := newNoteHandlerForTest(t, mcpPath)

	token := "stable-token-xyz"
	if err := settingsRepo.SetMCPTokenOnce(7, token); err != nil {
		t.Fatalf("预设 token 失败: %v", err)
	}
	// 首次同步写入
	h.SyncAllNotesMCP()
	dataBefore, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("首次同步后读取失败: %v", err)
	}
	// 再次同步：条目一致，应跳过（文件内容不变）
	h.SyncAllNotesMCP()
	dataAfter, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("二次同步后读取失败: %v", err)
	}
	if string(dataBefore) != string(dataAfter) {
		t.Errorf("一致时不应重写文件\nbefore: %s\nafter: %s", dataBefore, dataAfter)
	}
}

// TestSyncAllNotesMCP_UpdatesWhenStale 验证条目存在但 token 与库不一致时更新。
// 模拟"mcp.json 中残留旧 token，库中已更新"的自愈场景。
func TestSyncAllNotesMCP_UpdatesWhenStale(t *testing.T) {
	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	h, settingsRepo := newNoteHandlerForTest(t, mcpPath)

	// 库中的当前 token
	currentToken := "current-token-789"
	if err := settingsRepo.SetMCPTokenOnce(7, currentToken); err != nil {
		t.Fatalf("预设 token 失败: %v", err)
	}
	// 预写一个带"过期 token"的 opennexus-notes 条目（模拟不一致）
	staleEntry := `{
  "mcpServers": {
    "opennexus-notes": {
      "type": "http",
      "url": "http://127.0.0.1:8008/mcp/notes",
      "headers": { "Authorization": "Bearer stale-and-wrong" }
    }
  }
}`
	if err := os.WriteFile(mcpPath, []byte(staleEntry), 0o644); err != nil {
		t.Fatalf("预写 mcp.json 失败: %v", err)
	}

	// 同步应检测到不一致并更新为当前 token
	h.SyncAllNotesMCP()

	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("读取 mcp.json 失败: %v", err)
	}
	var file struct {
		McpServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("mcp.json 非法: %v", err)
	}
	if file.McpServers[NotesMCPName].Headers["Authorization"] != "Bearer "+currentToken {
		t.Errorf("未更新为当前 token: %+v", file.McpServers[NotesMCPName])
	}
}
