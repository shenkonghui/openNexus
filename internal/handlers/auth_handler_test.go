package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/database"
	"opennexus/internal/services"
)

func setupRouter(t *testing.T) (*gin.Engine, *services.AuthService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	db.Exec("DELETE FROM users")
	db.Exec("DELETE FROM refresh_tokens")
	jwtSvc := services.NewJWTService("this-is-a-very-long-jwt-secret-key-32+bytes!", 15*time.Minute, time.Hour)
	authSvc := services.NewAuthService(db, jwtSvc, 10)
	h := NewAuthHandler(authSvc, false)

	r := gin.New()
	v1 := r.Group("/api/v1")
	auth := v1.Group("/auth")
	auth.POST("/register", h.Register)
	auth.POST("/login", h.Login)
	auth.POST("/refresh", h.Refresh)
	auth.POST("/logout", h.Logout)
	v1.GET("/me", h.Me) // 未经中间件保护，仅测 handler 内部逻辑
	return r, authSvc
}

func doJSON(t *testing.T, r http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHandler_Register_Success(t *testing.T) {
	r, _ := setupRouter(t)
	w := doJSON(t, r, "POST", "/api/v1/auth/register", gin.H{
		"username": "alice", "email": "alice@example.com", "password": "Password123",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d, 期望 201, body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_Register_WeakPassword(t *testing.T) {
	r, _ := setupRouter(t)
	w := doJSON(t, r, "POST", "/api/v1/auth/register", gin.H{
		"username": "bob", "email": "bob@example.com", "password": "short",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400", w.Code)
	}
}

func TestHandler_Register_Duplicate(t *testing.T) {
	r, _ := setupRouter(t)
	_ = doJSON(t, r, "POST", "/api/v1/auth/register", gin.H{"username": "carol", "email": "carol@example.com", "password": "Password123"})
	w := doJSON(t, r, "POST", "/api/v1/auth/register", gin.H{"username": "carol", "email": "other@example.com", "password": "Password123"})
	if w.Code != http.StatusConflict {
		t.Fatalf("状态码 = %d, 期望 409", w.Code)
	}
}

func TestHandler_Login_Success(t *testing.T) {
	r, _ := setupRouter(t)
	_ = doJSON(t, r, "POST", "/api/v1/auth/register", gin.H{"username": "dave", "email": "dave@example.com", "password": "Password123"})

	w := doJSON(t, r, "POST", "/api/v1/auth/login", gin.H{"account": "dave", "password": "Password123"})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.AccessToken == "" || resp.Data.RefreshToken == "" {
		t.Error("期望返回非空令牌")
	}
}

func TestHandler_Login_InvalidCreds(t *testing.T) {
	r, _ := setupRouter(t)
	w := doJSON(t, r, "POST", "/api/v1/auth/login", gin.H{"account": "nobody", "password": "Password123"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, 期望 401", w.Code)
	}
}

func TestHandler_Refresh_Success(t *testing.T) {
	r, _ := setupRouter(t)
	_ = doJSON(t, r, "POST", "/api/v1/auth/register", gin.H{"username": "eve", "email": "eve@example.com", "password": "Password123"})
	lw := doJSON(t, r, "POST", "/api/v1/auth/login", gin.H{"account": "eve", "password": "Password123"})

	var login struct {
		Data struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(lw.Body.Bytes(), &login)

	w := doJSON(t, r, "POST", "/api/v1/auth/refresh", gin.H{"refresh_token": login.Data.RefreshToken})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200, body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_Logout_Success(t *testing.T) {
	r, _ := setupRouter(t)
	_ = doJSON(t, r, "POST", "/api/v1/auth/register", gin.H{"username": "frank", "email": "frank@example.com", "password": "Password123"})
	lw := doJSON(t, r, "POST", "/api/v1/auth/login", gin.H{"account": "frank", "password": "Password123"})

	var login struct {
		Data struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(lw.Body.Bytes(), &login)

	w := doJSON(t, r, "POST", "/api/v1/auth/logout", gin.H{"refresh_token": login.Data.RefreshToken})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", w.Code)
	}
}
