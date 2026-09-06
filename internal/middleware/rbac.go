package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
