package services

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"opennexus/internal/database"
	"opennexus/internal/models"
)

func newAuthSvc(t *testing.T) (*AuthService, *gorm.DB) {
	t.Helper()
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	db.Exec("DELETE FROM users")
	db.Exec("DELETE FROM refresh_tokens")
	jwtSvc := NewJWTService("this-is-a-very-long-jwt-secret-key-32+bytes!", 15*time.Minute, time.Hour)
	svc := NewAuthService(db, jwtSvc, 10) // bcrypt cost=10 加速测试
	return svc, db
}

func TestAuthService_Register_Success(t *testing.T) {
	svc, db := newAuthSvc(t)
	user, err := svc.Register("alice", "alice@example.com", "Password123")
	if err != nil {
		t.Fatalf("Register 错误: %v", err)
	}
	if user.ID == 0 {
		t.Error("期望 ID 非零")
	}
	if user.PasswordHash == "Password123" {
		t.Error("密码不应明文存储")
	}
	var count int64
	db.Model(&models.User{}).Count(&count)
	if count != 1 {
		t.Errorf("期望 1 条用户，实际 %d", count)
	}
}

func TestAuthService_Register_WeakPassword(t *testing.T) {
	svc, _ := newAuthSvc(t)
	if _, err := svc.Register("bob", "bob@example.com", "short"); err != ErrWeakPassword {
		t.Errorf("期望 ErrWeakPassword，实际 %v", err)
	}
}

func TestAuthService_Register_DuplicateUsername(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("carol", "carol@example.com", "Password123")
	if _, err := svc.Register("carol", "other@example.com", "Password123"); err != ErrUserExists {
		t.Errorf("期望 ErrUserExists，实际 %v", err)
	}
}

func TestAuthService_Register_DuplicateEmail(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("dave", "dave@example.com", "Password123")
	if _, err := svc.Register("dave2", "dave@example.com", "Password123"); err != ErrUserExists {
		t.Errorf("期望 ErrUserExists，实际 %v", err)
	}
}

func TestAuthService_Login_ByUsername(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("alice", "alice@example.com", "Password123")

	result, err := svc.Login("alice", "Password123", "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login 错误: %v", err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		t.Error("期望非空令牌")
	}
	if result.User.Username != "alice" {
		t.Errorf("Username = %q", result.User.Username)
	}
}

func TestAuthService_Login_ByEmail(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("bob", "bob@example.com", "Password123")

	result, err := svc.Login("bob@example.com", "Password123", "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login 错误: %v", err)
	}
	if result.User.Username != "bob" {
		t.Errorf("Username = %q", result.User.Username)
	}
}

func TestAuthService_Login_WrongPassword(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("carol", "carol@example.com", "Password123")

	if _, err := svc.Login("carol", "WrongPass1", "ua", "127.0.0.1"); err != ErrInvalidCreds {
		t.Errorf("期望 ErrInvalidCreds，实际 %v", err)
	}
}

func TestAuthService_Login_UnknownAccount(t *testing.T) {
	svc, _ := newAuthSvc(t)
	if _, err := svc.Login("ghost", "Password123", "ua", "127.0.0.1"); err != ErrInvalidCreds {
		t.Errorf("期望 ErrInvalidCreds，实际 %v", err)
	}
}

func TestAuthService_Login_DisabledUser(t *testing.T) {
	svc, db := newAuthSvc(t)
	u, _ := svc.Register("dave", "dave@example.com", "Password123")
	db.Model(&models.User{}).Where("id = ?", u.ID).Update("status", models.StatusDisabled)

	if _, err := svc.Login("dave", "Password123", "ua", "127.0.0.1"); err != ErrUserDisabled {
		t.Errorf("期望 ErrUserDisabled，实际 %v", err)
	}
}

func TestAuthService_Refresh_Success_Rotates(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("alice", "alice@example.com", "Password123")
	r1, _ := svc.Login("alice", "Password123", "ua", "ip")

	r2, err := svc.Refresh(r1.RefreshToken, "ua", "ip")
	if err != nil {
		t.Fatalf("Refresh 错误: %v", err)
	}
	if r2.RefreshToken == r1.RefreshToken {
		t.Error("期望轮换后 refresh token 不同")
	}
	if r2.AccessToken == "" {
		t.Error("期望非空 access token")
	}
}

func TestAuthService_Refresh_OldTokenRevoked(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("bob", "bob@example.com", "Password123")
	r1, _ := svc.Login("bob", "Password123", "ua", "ip")

	_, _ = svc.Refresh(r1.RefreshToken, "ua", "ip")
	// 旧 token 再次使用应失败（已吊销）
	if _, err := svc.Refresh(r1.RefreshToken, "ua", "ip"); err != ErrInvalidToken {
		t.Errorf("期望旧 token 失败，实际 %v", err)
	}
}

func TestAuthService_Refresh_ReplayRevokesAll(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("carol", "carol@example.com", "Password123")
	r1, _ := svc.Login("carol", "Password123", "ua", "ip")
	r2, _ := svc.Refresh(r1.RefreshToken, "ua", "ip")

	// r1 已吊销；重放 r1 应触发吊销该用户全部令牌
	_, err := svc.Refresh(r1.RefreshToken, "ua", "ip")
	if err != ErrInvalidToken {
		t.Fatalf("期望 ErrInvalidToken，实际 %v", err)
	}
	// 现在 r2 也应失效
	if _, err := svc.Refresh(r2.RefreshToken, "ua", "ip"); err != ErrInvalidToken {
		t.Errorf("重放后 r2 应被吊销，实际 %v", err)
	}
}

func TestAuthService_Logout(t *testing.T) {
	svc, _ := newAuthSvc(t)
	_, _ = svc.Register("alice", "alice@example.com", "Password123")
	r, _ := svc.Login("alice", "Password123", "ua", "ip")

	if err := svc.Logout(r.RefreshToken); err != nil {
		t.Fatalf("Logout 错误: %v", err)
	}
	if _, err := svc.Refresh(r.RefreshToken, "ua", "ip"); err != ErrInvalidToken {
		t.Errorf("期望登出后刷新失败，实际 %v", err)
	}
}

func TestAuthService_GetUserByID(t *testing.T) {
	svc, _ := newAuthSvc(t)
	u, _ := svc.Register("bob", "bob@example.com", "Password123")

	got, err := svc.GetUserByID(u.ID)
	if err != nil {
		t.Fatalf("GetUserByID 错误: %v", err)
	}
	if got.Username != "bob" {
		t.Errorf("Username = %q", got.Username)
	}
}
