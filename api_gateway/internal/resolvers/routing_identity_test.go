package resolvers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/gin-gonic/gin"
)

func TestRoutingClientIP(t *testing.T) {
	for _, tc := range []struct {
		name, peer, trusted, want   string
		gin, ginValue, requestValue bool
	}{
		{name: "raw peer ignores forwarding", peer: "192.0.2.8:4321", want: "192.0.2.8"},
		{name: "gin peer ignores forwarding", gin: true, peer: "192.0.2.8:4321", want: "192.0.2.8"},
		{name: "raw IPv6", peer: "[2001:db8::1]:4321", want: "2001:db8::1"},
		{name: "IPv6 without port", peer: "2001:db8::1", want: "2001:db8::1"},
		{name: "websocket middleware", peer: "127.0.0.1:4321", trusted: "198.51.100.4", want: "198.51.100.4"},
		{name: "gin middleware", gin: true, ginValue: true, peer: "127.0.0.1:4321", trusted: "198.51.100.4", want: "198.51.100.4"},
		{name: "request middleware", requestValue: true, peer: "127.0.0.1:4321", trusted: "198.51.100.4", want: "198.51.100.4"},
		{name: "gin request middleware", gin: true, requestValue: true, peer: "127.0.0.1:4321", trusted: "198.51.100.4", want: "198.51.100.4"},
		{name: "missing identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), "POST", "/graphql", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.99")
			req.Header.Set("X-Real-IP", "203.0.113.98")
			ctx := context.Background()
			if tc.requestValue {
				req = req.WithContext(context.WithValue(ctx, ctxkeys.KeyClientIP, tc.trusted))
			} else if !tc.ginValue && tc.trusted != "" {
				ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, tc.trusted)
			}
			if tc.gin {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = req
				if tc.ginValue {
					c.Set(string(ctxkeys.KeyClientIP), tc.trusted)
				}
				ctx = context.WithValue(ctx, ctxkeys.KeyGinContext, c)
			} else {
				ctx = context.WithValue(ctx, ctxkeys.KeyHTTPRequest, req)
			}
			got := RoutingClientIP(ctx)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("got %q, want unknown identity", *got)
				}
			} else if got == nil || *got != tc.want {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
		})
	}
	if got := RoutingClientIP(context.Background()); got != nil {
		t.Fatalf("empty context resolved to %q", *got)
	}
}
