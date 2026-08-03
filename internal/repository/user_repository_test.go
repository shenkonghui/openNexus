package repository

import (
	"testing"

	"gorm.io/gorm"

	"opennexus/internal/database"
	"opennexus/internal/models"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	db.Exec("DELETE FROM users")
	db.Exec("DELETE FROM refresh_tokens")
	db.Exec("DELETE FROM sessions")
	db.Exec("DELETE FROM messages")
	db.Exec("DELETE FROM note_settings")
	db.Exec("DELETE FROM notes")
	db.Exec("DELETE FROM user_agent_prefs")
	return db
}

func TestUserRepo_Create(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepository(db)

	user := &models.User{Username: "alice", Email: "alice@example.com", PasswordHash: "hash", Role: models.RoleUser, Status: models.StatusActive}
	if err := repo.Create(user); err != nil {
		t.Fatalf("Create 返回错误: %v", err)
	}
	if user.ID == 0 {
		t.Error("期望创建后 ID 非零")
	}
}

func TestUserRepo_DuplicateUsername(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepository(db)
	_ = repo.Create(&models.User{Username: "bob", Email: "bob1@example.com", PasswordHash: "h", Role: models.RoleUser, Status: models.StatusActive})

	err := repo.Create(&models.User{Username: "bob", Email: "bob2@example.com", PasswordHash: "h", Role: models.RoleUser, Status: models.StatusActive})
	if err == nil {
		t.Error("期望重复用户名返回错误")
	}
}

func TestUserRepo_FindByUsername(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepository(db)
	_ = repo.Create(&models.User{Username: "carol", Email: "carol@example.com", PasswordHash: "h", Role: models.RoleUser, Status: models.StatusActive})

	got, err := repo.FindByUsername("carol")
	if err != nil {
		t.Fatalf("FindByUsername 返回错误: %v", err)
	}
	if got.Email != "carol@example.com" {
		t.Errorf("Email = %q", got.Email)
	}
}

func TestUserRepo_FindByUsername_NotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepository(db)

	if _, err := repo.FindByUsername("nobody"); err == nil {
		t.Error("期望未找到时返回错误")
	}
}

func TestUserRepo_FindByEmail(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepository(db)
	_ = repo.Create(&models.User{Username: "dave", Email: "dave@example.com", PasswordHash: "h", Role: models.RoleUser, Status: models.StatusActive})

	got, err := repo.FindByEmail("dave@example.com")
	if err != nil {
		t.Fatalf("FindByEmail 返回错误: %v", err)
	}
	if got.Username != "dave" {
		t.Errorf("Username = %q", got.Username)
	}
}

func TestUserRepo_FindByID(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepository(db)
	u := &models.User{Username: "eve", Email: "eve@example.com", PasswordHash: "h", Role: models.RoleUser, Status: models.StatusActive}
	_ = repo.Create(u)

	got, err := repo.FindByID(u.ID)
	if err != nil {
		t.Fatalf("FindByID 返回错误: %v", err)
	}
	if got.Username != "eve" {
		t.Errorf("Username = %q", got.Username)
	}
}
