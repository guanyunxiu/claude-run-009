// Package auth 提供会话级令牌鉴权。
//
// 模型：
//   - 创建会话时网关生成两枚随机令牌：hostToken（主播）与 viewToken（观众）。
//   - 主播端可上传分片 / 结束会话 / 查看字幕；观众端只能查看字幕 / 挂 WebSocket。
//   - 可选的全局 GATEWAY_API_KEY 供运维脚本（如 RTMP 拉流）访问全部接口。
//   - 令牌通过 Authorization: Bearer <token> 传递；浏览器 WebSocket 无法自定义
//     请求头，允许通过 ?token=<token> 传递（中间件对 query token 仅放行读路径）。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type Role string

const (
	RoleHost Role = "host"
	RoleView Role = "view"
)

// SessionIdentity 令牌解析出的会话身份。
type SessionIdentity struct {
	SessionID string
	Role      Role
}

// TokenLookup 由持久层实现：按令牌反查会话身份。
type TokenLookup interface {
	IdentityByToken(token string) (*SessionIdentity, error)
}

// tokenFromRequest 提取 Bearer 令牌；allowQuery 时额外接受 ?token=（仅 WS/读路径）。
func tokenFromRequest(ctx *gin.Context, allowQuery bool) string {
	if header := ctx.GetHeader("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	if allowQuery {
		return strings.TrimSpace(ctx.Query("token"))
	}
	return ""
}

func generateToken() (string, error) {
	buf := make([]byte, 24) // 192-bit，hex 后 48 字符
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// NewTokenPair 生成一组 host/view 令牌。
func NewTokenPair() (host, view string, err error) {
	if host, err = generateToken(); err != nil {
		return "", "", err
	}
	if view, err = generateToken(); err != nil {
		return "", "", err
	}
	return host, view, nil
}

// Middleware 构造会话鉴权中间件。
//
//	apiKey    全局运维密钥；为空则禁用。
//	allowQuery 允许 query token（WebSocket 场景）。
//	roles     允许的角色（host 自动满足 view 权限）。
func Middleware(lookup TokenLookup, apiKey string, allowQuery bool, roles ...Role) gin.HandlerFunc {
	hostAllowed := containsRole(roles, RoleHost)
	viewAllowed := containsRole(roles, RoleView)

	return func(ctx *gin.Context) {
		token := tokenFromRequest(ctx, allowQuery)
		if token == "" {
			abortUnauthorized(ctx, "missing bearer token")
			return
		}

		// 全局运维密钥：持有者等价于 host，可跨会话访问（RTMP 拉流脚本等）。
		if apiKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) == 1 {
			ctx.Set("authRole", RoleHost)
			ctx.Set("authGlobal", true)
			ctx.Next()
			return
		}

		identity, err := lookup.IdentityByToken(token)
		if err != nil || identity == nil {
			abortUnauthorized(ctx, "invalid token")
			return
		}

		// 会话作用域令牌：必须与路径上的 :id 一致，防止 A 会话令牌访问 B 会话。
		if pathID := ctx.Param("id"); pathID != "" && pathID != identity.SessionID {
			abortUnauthorized(ctx, "token does not match session")
			return
		}

		if !roleSatisfies(identity.Role, hostAllowed, viewAllowed) {
			abortForbidden(ctx, "insufficient role")
			return
		}

		ctx.Set("sessionID", identity.SessionID)
		ctx.Set("authRole", identity.Role)
		ctx.Next()
	}
}

// roleSatisfies 判断身份角色是否满足本路由要求。
// host 同时具备 view 权限；要求 host 的路由不接受 view。
func roleSatisfies(have Role, wantHost, wantView bool) bool {
	switch have {
	case RoleHost:
		// host 满足任何需要会话身份的路由。
		return wantHost || wantView
	case RoleView:
		return wantView && !wantHost
	default:
		return false
	}
}

func containsRole(roles []Role, target Role) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

// GlobalAPIKeyOnly 保护全局接口（如列会话）：仅持有运维 API key 可访问。
// 未配置 apiKey 时接口直接 404（不暴露会话列表）。
func GlobalAPIKeyOnly(apiKey string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if apiKey == "" {
			ctx.AbortWithStatus(http.StatusNotFound)
			return
		}
		token := tokenFromRequest(ctx, false)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
			abortUnauthorized(ctx, "global api key required")
			return
		}
		ctx.Set("authGlobal", true)
		ctx.Next()
	}
}

func abortUnauthorized(ctx *gin.Context, msg string) {
	ctx.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": msg})
}

func abortForbidden(ctx *gin.Context, msg string) {
	ctx.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": msg})
}

// IdentityFromContext 取出已认证身份（handler 使用）。
func IdentityFromContext(ctx *gin.Context) (sessionID string, role Role, global bool) {
	if v, ok := ctx.Get("authGlobal"); ok {
		global, _ = v.(bool)
	}
	if v, ok := ctx.Get("sessionID"); ok {
		sessionID, _ = v.(string)
	}
	if v, ok := ctx.Get("authRole"); ok {
		role, _ = v.(Role)
	}
	return sessionID, role, global
}
