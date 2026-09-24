package graph

import (
	"context"
	"net/http/httptest"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/gin-gonic/gin"
)

func TestMediaResolversUseTrustedRoutingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, peer, trusted, want string
		gin                       bool
	}{
		{name: "raw peer", peer: "192.0.2.8:8080", want: "192.0.2.8"},
		{name: "gin peer", gin: true, peer: "192.0.2.8:8080", want: "192.0.2.8"},
		{name: "raw IPv6", peer: "[2001:db8::8]:8080", want: "2001:db8::8"},
		{name: "trusted middleware", gin: true, peer: "127.0.0.1:8080", trusted: "198.51.100.5", want: "198.51.100.5"},
		{name: "unknown identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), "POST", "/graphql", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", "203.0.113.99")
			req.Header.Set("X-Real-IP", "203.0.113.98")
			ctx := context.Background()
			if tc.gin {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = req
				ctx = context.WithValue(ctx, ctxkeys.KeyGinContext, c)
			} else {
				ctx = context.WithValue(ctx, ctxkeys.KeyHTTPRequest, req)
			}
			if tc.trusted != "" {
				ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, tc.trusted)
			}
			calls := 0
			check := func(ip string) {
				calls++
				if ip != tc.want {
					t.Errorf("routing IP = %q, want %q", ip, tc.want)
				}
			}
			fake := &clientstest.FakeCommodore{
				ResolveIngestEndpointFn: func(_ context.Context, _ string, ip string, _ sharedpb.IngestProtocol) (*sharedpb.IngestEndpointResponse, error) {
					check(ip)
					return &sharedpb.IngestEndpointResponse{}, nil
				},
				ResolveViewerFn: func(_ context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error) {
					check(req.GetViewerIp())
					return &sharedpb.ViewerEndpointResponse{}, nil
				},
			}
			r := &queryResolver{&Resolver{Resolver: &resolvers.Resolver{
				Clients: clientstest.Clients(clientstest.WithCommodore(fake)),
				Logger:  clientstest.DiscardLogger(),
			}}}
			if _, err := r.ResolveViewerEndpoint(ctx, "content", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := r.ResolveIngestEndpoint(ctx, "stream-key", nil); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("got %d authority calls, want 2", calls)
			}
		})
	}
}
