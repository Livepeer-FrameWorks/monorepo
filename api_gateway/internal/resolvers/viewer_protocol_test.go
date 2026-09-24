package resolvers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestViewerProtocolForwarding(t *testing.T) {
	for input, canonical := range map[string]string{"WEBRTC": "webrtc", "WHEP": "whep", "HLS": "hls", "HLS_CMAF": "cmaf", "DASH": "dash", "MEWS": "wsmp4", "RAW_WS": "raw_ws", "MIST_HTML": "mist_html", "RTMP": "rtmp"} {
		t.Run(input, func(t *testing.T) {
			fake := &clientstest.FakeCommodore{ResolveViewerFn: func(_ context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error) {
				if req.GetContentId() != "public" || req.GetViewerIp() != "192.0.2.1" || req.GetViewerToken() != "" || req.GetProtocol() != canonical {
					t.Fatalf("viewer context or protocol changed: %+v", req)
				}
				return &sharedpb.ViewerEndpointResponse{}, nil
			}}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
			ip := "192.0.2.1"
			if _, err := r.DoResolveViewerEndpointForProtocol(t.Context(), "public", &ip, input); err != nil || fake.Calls != 1 {
				t.Fatalf("protocol forwarding: calls=%d err=%v", fake.Calls, err)
			}
		})
	}
}

// A browser's Origin and Referer reach Commodore for the policy's allowed
// origins; a call with no HTTP request forwards neither.
func TestViewerResolveForwardsBrowserOrigin(t *testing.T) {
	var got *sharedpb.ViewerEndpointRequest
	fake := &clientstest.FakeCommodore{ResolveViewerFn: func(_ context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error) {
		got = req
		return &sharedpb.ViewerEndpointResponse{}, nil
	}}
	r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}

	httpReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graphql", nil)
	httpReq.Header.Set("Origin", "https://embed.example")
	httpReq.Header.Set("Referer", "https://embed.example/watch")
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httpReq
	ctx := context.WithValue(t.Context(), ctxkeys.KeyGinContext, ginCtx)
	if _, err := r.DoResolveViewerEndpoint(ctx, "public", nil); err != nil {
		t.Fatal(err)
	}
	if got.GetViewerOrigin() != "https://embed.example" || got.GetViewerReferer() != "https://embed.example/watch" {
		t.Fatalf("forwarded origin/referer = %q/%q", got.GetViewerOrigin(), got.GetViewerReferer())
	}

	if _, err := r.DoResolveViewerEndpoint(t.Context(), "public", nil); err != nil {
		t.Fatal(err)
	}
	if got.ViewerOrigin != nil || got.ViewerReferer != nil {
		t.Fatalf("a call without an HTTP request forwarded headers: %+v", got)
	}
}

func TestViewerProtocolValidationPrecedesAuthority(t *testing.T) {
	for _, value := range []string{"whep", " WHEP", "HTTP", "WEBRTC\n", "unknown"} {
		if _, err := (&Resolver{}).DoResolveViewerEndpointForProtocol(t.Context(), "public", nil, value); err == nil || err.Error() != "unsupported viewer protocol" {
			t.Fatalf("protocol %q = %v", value, err)
		}
	}
}

func TestViewerProtocolDemoDoesNotSubstituteFormatOrCallServices(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxkeys.KeyDemoMode, true)
	r := &Resolver{}
	for _, requested := range []string{"HLS", "WHEP"} {
		response, err := r.DoResolveViewerEndpointForProtocol(ctx, "demo_live_stream_001", nil, requested)
		if err != nil || response.GetPrimary().GetOutputs()[requested] == nil || len(response.Primary.Outputs) != 1 || response.Primary.Url != response.Primary.Outputs[requested].Url {
			t.Fatalf("demo format %s: %+v, %v", requested, response, err)
		}
	}
	if response, err := r.DoResolveViewerEndpointForProtocol(ctx, "demo_live_stream_001", nil, "WEBRTC"); err == nil || response != nil {
		t.Fatal("demo substituted WHEP for WebSocket WebRTC")
	}
}
