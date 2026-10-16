package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"token-gateway/internal/model"
)

// RequireRole 角色守卫：仅指定角色可访问
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := GetRole(c)
		for _, r := range roles {
			if role == r {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": gin.H{"message": "role " + role + " is not allowed to access this resource", "type": "forbidden"},
		})
	}
}

// RequireSuperAdmin 平台层超管守卫：「子管理员管理 + 操作审计查看」专属。
// 须挂在 RequireRole(platform_admin) 组内；此处再校验 role 属纵深防御。
// 子管理员拥有平台全部业务权限，仅这两个能力为超管专属
func RequireSuperAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if GetRole(c) == model.RolePlatformAdmin && IsSuperAdmin(c) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": gin.H{"message": "super admin only", "type": "forbidden"},
		})
	}
}
