package notesmcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opennexus/internal/database"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

func setupNotesMCP(t *testing.T) (http.Handler, *repository.NoteRepository, *repository.NoteSettingsRepository) {
	t.Helper()
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("DELETE FROM notes")
	db.Exec("DELETE FROM note_settings")
	noteRepo := repository.NewNoteRepository(db)
	settingsRepo := repository.NewNoteSettingsRepository(db)
	return Handler(noteRepo, settingsRepo), noteRepo, settingsRepo
}

func TestHandler_Unauthorized(t *testing.T) {
	h, _, _ := setupNotesMCP(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp/notes", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, 期望 401", w.Code)
	}
}

func TestTools_ListAndGet(t *testing.T) {
	_, noteRepo, settingsRepo := setupNotesMCP(t)
	if err := settingsRepo.SetMCPTokenOnce(7, "test-token-7"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	n1 := &models.Note{UserID: 7, Title: "有标题", Content: "secret-body", Tags: `["work"]`, UpdatedAt: now}
	n2 := &models.Note{UserID: 7, Title: "无标题", Content: "full-text-here", Tags: `["work"]`, UpdatedAt: now}
	n3 := &models.Note{UserID: 8, Title: "别人的", Content: "nope", Tags: `["work"]`, UpdatedAt: now}
	_ = noteRepo.Create(n1)
	_ = noteRepo.Create(n2)
	_ = noteRepo.Create(n3)

	ctx := withUserID(t.Context(), 7)
	_, out, err := handleListNotes(ctx, noteRepo, listNotesIn{Tag: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Notes) != 2 {
		t.Fatalf("notes=%d, 期望 2", len(out.Notes))
	}
	var titled, untitled *noteListItem
	for i := range out.Notes {
		if out.Notes[i].ID == n1.ID {
			titled = &out.Notes[i]
		}
		if out.Notes[i].ID == n2.ID {
			untitled = &out.Notes[i]
		}
	}
	if titled == nil || untitled == nil {
		t.Fatalf("缺少期望笔记: %+v", out.Notes)
	}
	if titled.Content != "" {
		t.Fatalf("有标题不应返回 content: %q", titled.Content)
	}
	if untitled.Content != "full-text-here" {
		t.Fatalf("无标题应返回全文, got %q", untitled.Content)
	}
	_, _, err = handleGetNote(ctx, noteRepo, getNoteIn{ID: n3.ID})
	if err == nil || err.Error() != "未找到" {
		t.Fatalf("越权 get_note err=%v", err)
	}
	_, full, err := handleGetNote(ctx, noteRepo, getNoteIn{ID: n1.ID})
	if err != nil || full.Content != "secret-body" {
		t.Fatalf("get_note: %+v err=%v", full, err)
	}
}

func TestListNotes_RequiresTag(t *testing.T) {
	db, _ := database.Connect("file::memory:?cache=shared", "")
	repo := repository.NewNoteRepository(db)
	_, _, err := handleListNotes(withUserID(t.Context(), 1), repo, listNotesIn{})
	if err == nil || !strings.Contains(err.Error(), "tag") {
		t.Fatalf("err=%v", err)
	}
}

func TestNoTitle(t *testing.T) {
	if !noTitle("无标题") || !noTitle("") || noTitle("x") {
		t.Fatal("noTitle 判断错误")
	}
}

func TestHandler_Initialize(t *testing.T) {
	h, _, settingsRepo := setupNotesMCP(t)
	_ = settingsRepo.SetMCPTokenOnce(1, "tok-init")
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/notes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer tok-init")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	raw := w.Body.String()
	if !strings.Contains(raw, "opennexus-notes") && !strings.Contains(raw, "result") {
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		t.Fatalf("unexpected body: %s", raw)
	}
}
