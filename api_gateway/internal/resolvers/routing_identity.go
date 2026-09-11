package resolvers

import (
	"context"
	"net/http"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedmw "github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/gin-gonic/gin"
)

// RoutingClientIP uses the identity resolved by HTTP or WebSocket proxy-trust middleware.
// Without that middleware, only the direct peer is authoritative; forwarding headers are not.
func RoutingClientIP(ctx context.Context) *string {
	if ip := ctxkeys.GetClientIP(ctx); ip != "" {
		return &ip
	}
	if c, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && c != nil {
		if value, ok := c.Get(string(ctxkeys.KeyClientIP)); ok {
			if ip, ok := value.(string); ok && ip != "" {
				return &ip
			}
		}
		if c.Request != nil {
			if ip := ctxkeys.GetClientIP(c.Request.Context()); ip != "" {
				return &ip
			}
			if ip := sharedmw.RemoteAddrIP(c.Request); ip != "" {
				return &ip
			}
		}
	}
	if req, ok := ctx.Value(ctxkeys.KeyHTTPRequest).(*http.Request); ok && req != nil {
		if ip := ctxkeys.GetClientIP(req.Context()); ip != "" {
			return &ip
		}
		if ip := sharedmw.RemoteAddrIP(req); ip != "" {
			return &ip
		}
	}
	return nil
}
