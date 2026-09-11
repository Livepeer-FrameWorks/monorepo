package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestIngestProtocolForwarding(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  sharedpb.IngestProtocol
	}{
		{"", sharedpb.IngestProtocol_INGEST_PROTOCOL_UNSPECIFIED},
		{"WHIP", sharedpb.IngestProtocol_INGEST_PROTOCOL_WHIP},
		{"RTMP", sharedpb.IngestProtocol_INGEST_PROTOCOL_RTMP},
		{"SRT", sharedpb.IngestProtocol_INGEST_PROTOCOL_SRT},
	} {
		t.Run(tc.input, func(t *testing.T) {
			calls := 0
			fake := &clientstest.FakeCommodore{ResolveIngestEndpointFn: func(_ context.Context, key, ip string, protocol sharedpb.IngestProtocol) (*sharedpb.IngestEndpointResponse, error) {
				calls++
				if key != "key" || ip != "192.0.2.1" || protocol != tc.want {
					t.Fatalf("request identity or protocol changed: %q %q %v", key, ip, protocol)
				}
				return &sharedpb.IngestEndpointResponse{}, nil
			}}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
			ip := "192.0.2.1"
			if _, err := r.DoResolveIngestEndpointForProtocol(t.Context(), "key", &ip, tc.input); err != nil || calls != 1 {
				t.Fatalf("forwarding: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestIngestUnknownProtocolDoesNotCallAuthority(t *testing.T) {
	for _, value := range []string{"whip", " WHIP", "HTTP", "WHIP\n"} {
		if _, err := (&Resolver{}).DoResolveIngestEndpointForProtocol(t.Context(), "key", nil, value); err == nil || err.Error() != "unsupported ingest protocol" {
			t.Fatalf("protocol %q = %v", value, err)
		}
	}
}
