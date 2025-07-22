package middleware

import (
	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// SetupCommonMiddleware adds all common middleware to a router
func SetupCommonMiddleware(r *gin.Engine, logger logging.Logger) {
	r.Use(RequestIDMiddleware())
	r.Use(LoggingMiddleware(logger))
	r.Use(RecoveryMiddleware(logger))
	r.Use(CORSMiddleware())
}

// GetRequestID gets the request ID from the context
func GetRequestID(c *gin.Context) string {
	if id, exists := c.Get("request_id"); exists {
		if strID, ok := id.(string); ok {
			return strID
		}
	}
	return ""
}

// GetContextLogger gets a logger with request context
func GetContextLogger(c *gin.Context, logger logging.Logger) *logrus.Entry {
	return logger.WithFields(logging.Fields{
		"request_id": GetRequestID(c),
		"method":     c.Request.Method,
		"path":       c.Request.URL.Path,
		"client_ip":  c.ClientIP(),
		"tenant_id":  c.GetString("tenant_id"),
		"user_id":    c.GetString("user_id"),
	})
}
