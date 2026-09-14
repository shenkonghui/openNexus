package services

import (
	"crypto/subtle"
	"errors"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"opennexus/internal/models"
	"opennexus/internal/repository"
)

var (
	ErrWeakPassword     = errors.New("密码强度不足")
	ErrUserExists       = errors.New("用户名或邮箱已存在")
	ErrInvalidCreds     = errors.New("账号或密码错误")
	ErrUserDisabled     = errors.New("用户已被禁用")
	ErrInvalidToken     = errors.New("无效或已过期的令牌")
	ErrWrongOldPassword = errors.New("原密码错误")
)

var (
	hasLetter = regexp.MustCompile(`[a-zA-Z]`)
	hasDigit  = regexp.MustCompile(`[0-9]`)
)

type AuthService struct {
	db          *gorm.DB
	users       *repository.UserRepository
	tokens      *repository.RefreshTokenRepository
	jwt         *JWTService
	bcryptCost  int
	staticToken string
}

func NewAuthService(db *gorm.DB, jwtSvc *JWTService, bcryptCost int) *AuthService {
	return &AuthService{
		db:         db,
		users:      repository.NewUserRepository(db),
		tokens:     repository.NewRefreshTokenRepository(db),
		jwt:        jwtSvc,
		bcryptCost: bcryptCost,
	}
}

// SetStaticToken 注入静态访问令牌（config.yaml auth.static_token，可被 AUTH_STATIC_TOKEN 覆盖）。
func (s *AuthService) SetStaticToken(token string) {
	s.staticToken = strings.TrimSpace(token)
}

// SeedAdminUser 确保内置 admin 用户存在，不存在时自动创建。
func (s *AuthService) SeedAdminUser() {
	_, err := s.users.FindByUsername("admin")
	if err == nil {
		return // 已存在
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("123456"), s.bcryptCost)
	if err != nil {
		return
	}
	user := &models.User{
		Username:     "admin",
		Email:        "admin@opennexus.local",
		PasswordHash: string(hash),
		Role:         models.RoleAdmin,
		Status:       models.StatusActive,
	}
	_ = s.users.Create(user)
}

func (s *AuthService) validatePassword(password string) bool {
	return len(password) >= 8 && hasLetter.MatchString(password) && hasDigit.MatchString(password)
}

func (s *AuthService) Register(username, email, password string) (*models.User, error) {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)

	if !s.validatePassword(password) {
		return nil, ErrWeakPassword
	}

	exists, err := s.users.ExistsByUsernameOrEmail(username, email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrUserExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return nil, err
	}

	user := &models.User{
		Username:     username,
		Email:        email,
		PasswordHash: string(hash),
		Role:         models.RoleUser,
		Status:       models.StatusActive,
	}
	if err := s.users.Create(user); err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, ErrUserExists
		}
		if isUniqueViolation(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return user, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed")
}

type AuthResult struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresIn    int64        `json:"expires_in"`
	User         *models.User `json:"user"`
}

func (s *AuthService) Login(account, password, userAgent, ip string) (*AuthResult, error) {
	user, err := s.findUserByAccount(account)
	if err != nil {
		return nil, ErrInvalidCreds // 统一错误，防用户枚举
	}
	if user.Status == models.StatusDisabled {
		return nil, ErrUserDisabled
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCreds
	}
	return s.issueTokens(user, userAgent, ip)
}

// AutoLoginAsAdmin 为内置 admin 直接签发 token（免登录模式，不校验密码）。
func (s *AuthService) AutoLoginAsAdmin(userAgent, ip string) (*AuthResult, error) {
	user, err := s.users.FindByUsername("admin")
	if err != nil {
		return nil, ErrInvalidCreds
	}
	if user.Status == models.StatusDisabled {
		return nil, ErrUserDisabled
	}
	return s.issueTokens(user, userAgent, ip)
}

// LoginWithStaticToken 用静态访问令牌登录：匹配即以 admin 身份签发 JWT。
// 浏览器通过 ?token=xxx 一次性授权后长期保存自动换取 JWT（设备级凭证）。
func (s *AuthService) LoginWithStaticToken(token, userAgent, ip string) (*AuthResult, error) {
	if s.staticToken == "" || token == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(s.staticToken)) != 1 {
		return nil, ErrInvalidCreds // 统一错误，防探测
	}
	return s.AutoLoginAsAdmin(userAgent, ip)
}

func (s *AuthService) findUserByAccount(account string) (*models.User, error) {
	if strings.Contains(account, "@") {
		return s.users.FindByEmail(account)
	}
	return s.users.FindByUsername(account)
}

func (s *AuthService) issueTokens(user *models.User, userAgent, ip string) (*AuthResult, error) {
	access, err := s.jwt.GenerateAccessToken(user.ID, user.Username, user.Role)
	if err != nil {
		return nil, err
	}
	refresh, jti, err := s.jwt.GenerateRefreshToken(user.ID)
	if err != nil {
		return nil, err
	}
	rt := &models.RefreshToken{
		UserID:    user.ID,
		TokenID:   jti,
		ExpiresAt: time.Now().Add(s.jwt.refreshTTL),
		UserAgent: userAgent,
		IP:        ip,
	}
	if err := s.tokens.Create(rt); err != nil {
		return nil, err
	}
	return &AuthResult{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int64(s.jwt.accessTTL.Seconds()),
		User:         user,
	}, nil
}

func (s *AuthService) Refresh(refreshToken, userAgent, ip string) (*AuthResult, error) {
	claims, err := s.jwt.Parse(refreshToken)
	if err != nil || claims.TokenType != TokenTypeRefresh {
		return nil, ErrInvalidToken
	}

	stored, err := s.tokens.FindByJTI(claims.JTI)
	if err != nil {
		return nil, ErrInvalidToken
	}

	// 重放检测：已吊销的 token 被再次使用 → 吊销该用户全部令牌
	if stored.Revoked {
		_ = s.tokens.RevokeAllByUser(stored.UserID)
		return nil, ErrInvalidToken
	}

	// 校验用户仍存在且未禁用
	user, err := s.users.FindByID(stored.UserID)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if user.Status == models.StatusDisabled {
		return nil, ErrUserDisabled
	}

	// 轮换：吊销旧 token
	if err := s.tokens.Revoke(stored.TokenID); err != nil {
		return nil, err
	}

	return s.issueTokens(user, userAgent, ip)
}

func (s *AuthService) Logout(refreshToken string) error {
	claims, err := s.jwt.Parse(refreshToken)
	if err != nil || claims.TokenType != TokenTypeRefresh {
		return ErrInvalidToken
	}
	return s.tokens.Revoke(claims.JTI)
}

func (s *AuthService) GetUserByID(id uint) (*models.User, error) {
	return s.users.FindByID(id)
}

// UpdateProfile 更新用户名与邮箱。校验唯一性（排除自身）。
func (s *AuthService) UpdateProfile(id uint, username, email string) (*models.User, error) {
	username = strings.TrimSpace(username)
	email = strings.TrimSpace(email)
	if username == "" || email == "" {
		return nil, errors.New("用户名与邮箱不能为空")
	}
	exists, err := s.users.ExistsByUsernameOrEmailExcludingID(id, username, email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrUserExists
	}
	if err := s.users.UpdateProfile(id, username, email); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return s.users.FindByID(id)
}

// ChangePassword 校验原密码后更新为新密码。
func (s *AuthService) ChangePassword(id uint, oldPassword, newPassword string) error {
	if !s.validatePassword(newPassword) {
		return ErrWeakPassword
	}
	user, err := s.users.FindByID(id)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)); err != nil {
		return ErrWrongOldPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.bcryptCost)
	if err != nil {
		return err
	}
	return s.users.UpdatePassword(id, string(hash))
}
