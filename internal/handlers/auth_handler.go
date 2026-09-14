package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"opennexus/internal/middleware"
	"opennexus/internal/services"
)

type AuthHandler struct {
	svc       *services.AuthService
	autoLogin bool
}

func NewAuthHandler(svc *services.AuthService, autoLogin bool) *AuthHandler {
	return &AuthHandler{svc: svc, autoLogin: autoLogin}
}

// AutoLogin GET /api/v1/auth/auto-login — 若启用免登录则自动签发 admin token。
func (h *AuthHandler) AutoLogin(c *gin.Context) {
	if !h.autoLogin {
		Fail(c, http.StatusForbidden, "AUTO_LOGIN_DISABLED", "自动登录未启用")
		return
	}
	result, err := h.svc.AutoLoginAsAdmin(c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusOK, result)
}

// TokenLogin POST /api/v1/auth/token — 静态访问令牌登录（config.yaml auth.static_token）。
// 浏览器通过 ?token=xxx 一次性授权后长期保存此令牌并自动换取 JWT。
func (h *AuthHandler) TokenLogin(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	result, err := h.svc.LoginWithStaticToken(req.Token, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusOK, result)
}

type registerRequest struct {
	Username string `json:"username" binding:"required"`
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	user, err := h.svc.Register(req.Username, req.Email, req.Password)
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusCreated, user)
}

type loginRequest struct {
	Account  string `json:"account" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	result, err := h.svc.Login(req.Account, req.Password, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusOK, result)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	result, err := h.svc.Refresh(req.RefreshToken, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusOK, result)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if err := h.svc.Logout(req.RefreshToken); err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

func (h *AuthHandler) Me(c *gin.Context) {
	userID, exists := c.Get(middleware.UserIDKey())
	if !exists {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	user, err := h.svc.GetUserByID(userID.(uint))
	if err != nil {
		Fail(c, http.StatusNotFound, "USER_NOT_FOUND", "用户不存在")
		return
	}
	Success(c, http.StatusOK, user)
}

type updateProfileRequest struct {
	Username string `json:"username" binding:"required"`
	Email    string `json:"email" binding:"required"`
}

// UpdateProfile PUT /api/v1/me — 更新当前用户名与邮箱。
func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	var req updateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	user, err := h.svc.UpdateProfile(uid, req.Username, req.Email)
	if err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusOK, user)
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// ChangePassword POST /api/v1/me/password — 修改当前用户密码。
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	if err := h.svc.ChangePassword(uid, req.OldPassword, req.NewPassword); err != nil {
		h.writeAuthError(c, err)
		return
	}
	Success(c, http.StatusOK, struct{}{})
}

// writeAuthError 将 service 层错误映射为统一 HTTP 响应
func (h *AuthHandler) writeAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrWeakPassword):
		Fail(c, http.StatusBadRequest, "WEAK_PASSWORD", "密码强度不足（至少 8 位，含字母与数字）")
	case errors.Is(err, services.ErrUserExists):
		Fail(c, http.StatusConflict, "USER_EXISTS", "用户名或邮箱已存在")
	case errors.Is(err, services.ErrInvalidCreds):
		Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "账号或密码错误")
	case errors.Is(err, services.ErrUserDisabled):
		Fail(c, http.StatusForbidden, "USER_DISABLED", "用户已被禁用")
	case errors.Is(err, services.ErrInvalidToken):
		Fail(c, http.StatusUnauthorized, "INVALID_TOKEN", "无效或已过期的令牌")
	case errors.Is(err, services.ErrWrongOldPassword):
		Fail(c, http.StatusBadRequest, "WRONG_OLD_PASSWORD", "原密码错误")
	default:
		Fail(c, http.StatusInternalServerError, "INTERNAL", "内部错误")
	}
}
