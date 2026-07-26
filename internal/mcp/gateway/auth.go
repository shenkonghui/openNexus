package gatewaymcp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"opennexus/internal/repository"
)

type userIDKey struct{}

// Authenticator 抽象网关的 token 校验与获取逻辑，使网关可以脱离主程序的 sqlite
// settings 仓库独立运行（独立二进制用 StaticAuthenticator，主程序用 DBAuthenticator）。
type Authenticator interface {
	// Authenticate 从请求的 Authorization Bearer 解析 token 并返回 userID。
	Authenticate(r *http.Request) (uint, error)
	// TokenForUser 返回指定用户可用的 Bearer token（用于设置页展示）。
	TokenForUser(userID uint) string
	// SharedToken 返回内置 MCP server 共享的全局 token（用于写入 mcp.json 条目）。
	SharedToken() string
}

// DBAuthenticator 复用 opennexus-notes 的 token 体系（token 存在 sqlite note_settings 表）。
type DBAuthenticator struct {
	settings *repository.NoteSettingsRepository
}

// NewDBAuthenticator 用主程序的 settings 仓库构造 Authenticator。
func NewDBAuthenticator(settings *repository.NoteSettingsRepository) *DBAuthenticator {
	return &DBAuthenticator{settings: settings}
}

// Authenticate 从 Authorization Bearer 解析 MCP Token 并返回 userID。
func (a *DBAuthenticator) Authenticate(r *http.Request) (uint, error) {
	tok, ok := bearerToken(r)
	if !ok {
		return 0, errors.New("unauthorized")
	}
	s, err := a.settings.FindByMCPToken(tok)
	if err != nil {
		return 0, err
	}
	return s.UserID, nil
}

// TokenForUser 返回指定用户的 MCP token；未生成时回退到全局共享 token。
func (a *DBAuthenticator) TokenForUser(userID uint) string {
	if a.settings == nil {
		return ""
	}
	if userID > 0 {
		if st, err := a.settings.FindByUserID(userID); err == nil {
			if tok := strings.TrimSpace(st.McpToken); tok != "" {
				return tok
			}
		}
	}
	return a.SharedToken()
}

// SharedToken 返回内置 MCP server 共用的全局 token（取第一个已生成的）。
func (a *DBAuthenticator) SharedToken() string {
	list, err := a.settings.FindAllWithMcpToken()
	if err != nil || len(list) == 0 {
		return ""
	}
	return strings.TrimSpace(list[0].McpToken)
}

// StaticAuthenticator 使用单一固定 token，供独立二进制模式使用。
// 所有请求只要带正确 token 即视为同一个虚拟用户（userID=1）。
type StaticAuthenticator struct {
	token string
}

// NewStaticAuthenticator 用固定 token 构造 Authenticator。
func NewStaticAuthenticator(token string) *StaticAuthenticator {
	return &StaticAuthenticator{token: strings.TrimSpace(token)}
}

// Authenticate 校验 Bearer token 是否与配置的固定 token 一致。
func (a *StaticAuthenticator) Authenticate(r *http.Request) (uint, error) {
	tok, ok := bearerToken(r)
	if !ok || tok != a.token {
		return 0, errors.New("unauthorized")
	}
	return 1, nil
}

// TokenForUser 独立模式下直接返回固定 token。
func (a *StaticAuthenticator) TokenForUser(_ uint) string { return a.token }

// SharedToken 独立模式下直接返回固定 token。
func (a *StaticAuthenticator) SharedToken() string { return a.token }

// bearerToken 从 Authorization 头取出 Bearer token。
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	if tok == "" {
		return "", false
	}
	return tok, true
}

func withUserID(ctx context.Context, uid uint) context.Context {
	return context.WithValue(ctx, userIDKey{}, uid)
}

func userIDFrom(ctx context.Context) (uint, bool) {
	uid, ok := ctx.Value(userIDKey{}).(uint)
	return uid, ok && uid > 0
}
