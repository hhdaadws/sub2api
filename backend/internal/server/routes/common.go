package routes

import (
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine, cfg *config.Config) {
	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Claude Code 遥测日志
	// Privacy: rewrite telemetry payload to normalize fingerprints before
	// discarding. The rewritten body is not forwarded anywhere today but
	// the normalization code is exercised so it stays tested and ready.
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		if cfg.Privacy.Enabled {
			body, err := io.ReadAll(c.Request.Body)
			if err == nil && len(body) > 0 {
				_ = service.PrivacyRewriteEventBatch(body, &cfg.Privacy)
			}
		}
		c.Status(http.StatusOK)
	})

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}
