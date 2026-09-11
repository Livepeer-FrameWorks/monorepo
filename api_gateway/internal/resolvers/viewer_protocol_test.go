package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestViewerProtocolForwarding(t *testing.T) {
	for input, canonical := range map[string]string{"WEBRTC": "webrtc", "WHEP": "whep", "HLS": "hls", "HLS_CMAF": "cmaf", "DASH": "dash", "MEWS": "wsmp4", "RAW_WS": "raw_ws", "MIST_HTML": "mist_html", "RTMP": "rtmp"} {
		t.Run(input, func(t *testing.T) {
			fake := &clientstest.FakeCommodore{ResolveViewerEndpointWithProtocolFn: func(_ context.Context, contentID, ip, token, protocol string) (*sharedpb.ViewerEndpointResponse, error) {
				if contentID != "public" || ip != "192.0.2.1" || token != "" || protocol != canonical {
					t.Fatalf("viewer context or protocol changed: %q %q %q %q", contentID, ip, token, protocol)
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
