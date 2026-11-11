package middleware

import (
	"context"
	"strings"
	"unicode"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/gin-gonic/gin"
)

func clientDisplayHint(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

// CaptureAuthClientMetadata uses the same trusted proxy boundary as rate limits.
// Device labels and versions remain untrusted display hints.
func CaptureAuthClientMetadata(proxies *TrustedProxies) gin.HandlerFunc {
	return func(c *gin.Context) {
		info := ctxkeys.AuthClientMetadata{
			IP:          ClientIPFromRequestWithTrust(c.Request, proxies),
			UserAgent:   clientDisplayHint(c.Request.UserAgent(), 1024),
			DeviceLabel: clientDisplayHint(c.GetHeader("X-Device-Label"), 200),
			Version:     clientDisplayHint(c.GetHeader("X-Client-Version"), 100),
		}
		ctx := ctxkeys.WithAuthClientMetadata(c.Request.Context(), info)
		ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, info.IP)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
