package taskmanagermcp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"opennexus/internal/repository"
)

type userIDKey struct{}

// BearerUserID 从 Authorization Bearer 解析 MCP Token（复用 opennexus-notes 的 token 体系）并返回 userID。
func BearerUserID(r *http.Request, settings *repository.NoteSettingsRepository) (uint, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return 0, errors.New("unauthorized")
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	s, err := settings.FindByMCPToken(tok)
	if err != nil {
		return 0, err
	}
	return s.UserID, nil
}

func withUserID(ctx context.Context, uid uint) context.Context {
	return context.WithValue(ctx, userIDKey{}, uid)
}

func userIDFrom(ctx context.Context) (uint, bool) {
	uid, ok := ctx.Value(userIDKey{}).(uint)
	return uid, ok && uid > 0
}
