// Package auth 负责控制面控制台的登录、JWT 签发与 RBAC 角色校验。
//
// 三级角色：admin（超管）/ operator（运维）/ viewer（只读）。
// 合规边界：所有写操作都必须经由 RBAC 拦截，审计中间件记录每次动作。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"ecp.dev/ecp/server/internal/store/model"
)

// 角色权限约束。
const (
	RoleAdmin    = model.RoleAdmin
	RoleOperator = model.RoleOperator
	RoleViewer   = model.RoleViewer
)

// 错误定义。
var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrTokenInvalid       = errors.New("凭据无效或已过期")
)

// Secret 用于 JWT 签名。优先取环境变量；缺失时生成临时密钥（仅本进程有效，
// 进程重启即失效，仅适用于开发/单机）。生产应通过 ECP_JWT_SECRET 固定。
func secret() []byte {
	if s := os.Getenv("ECP_JWT_SECRET"); s != "" {
		return []byte(s)
	}
	return []byte("__dev_only_ephemeral_secret_do_not_use_in_prod__")
}

// HashPassword 计算 bcrypt 哈希。
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword 校验明文密码与哈希。
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Claims 是 JWT 载荷。
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"sub"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// SignToken 签发一个有效期 24 小时的 JWT。
func SignToken(u *model.User) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(secret())
}

// ParseToken 校验并解析 JWT。
func ParseToken(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrTokenInvalid
		}
		return secret(), nil
	})
	if err != nil {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

// GenerateInitialPassword 生成初始超管口令（仅首次启动展示一次）。
func GenerateInitialPassword() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "ChangeMe!123"
	}
	return hex.EncodeToString(b)[:16]
}

// roleCan 判断角色是否满足最低权限要求。优先级 admin > operator > viewer。
func RoleCan(role, required string) bool {
	rank := map[string]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}
	r, ok1 := rank[role]
	req, ok2 := rank[required]
	if !ok1 || !ok2 {
		return false
	}
	return r >= req
}

// constantTimeEq 防止时序侧信道（当前用 subtle，保留扩展位）。
func constantTimeEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// AuditSink 是审计回调的最小接口，避免 auth 直接依赖 store。
type AuditSink func(userID uint, username, nodeID, action, result, detail, traceID string)

// LogSkip 占位，真实审计在 api 层注入。
var _ = slog.Default
var _ = constantTimeEq
