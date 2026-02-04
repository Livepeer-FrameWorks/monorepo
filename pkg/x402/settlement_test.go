package x402

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/pkg/globalid"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
)

type mockPurser struct {
	verifyFn func(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.VerifyX402PaymentResponse, error)
	settleFn func(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.SettleX402PaymentResponse, error)
}

func (m *mockPurser) VerifyX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.VerifyX402PaymentResponse, error) {
	if m.verifyFn == nil {
		return nil, nil
	}
	return m.verifyFn(ctx, tenantID, payment, clientIP)
}

func (m *mockPurser) SettleX402Payment(ctx context.Context, tenantID string, payment *pb.X402PaymentPayload, clientIP string) (*pb.SettleX402PaymentResponse, error) {
	if m.settleFn == nil {
		return nil, nil
	}
	return m.settleFn(ctx, tenantID, payment, clientIP)
}

type mockCommodore struct {
	resolvePlaybackIDFn         func(ctx context.Context, playbackID string) (*pb.ResolvePlaybackIDResponse, error)
	resolveArtifactPlaybackIDFn func(ctx context.Context, playbackID string) (*pb.ResolveArtifactPlaybackIDResponse, error)
	resolveClipHashFn           func(ctx context.Context, clipHash string) (*pb.ResolveClipHashResponse, error)
	resolveDVRHashFn            func(ctx context.Context, dvrHash string) (*pb.ResolveDVRHashResponse, error)
	resolveIdentifierFn         func(ctx context.Context, identifier string) (*pb.ResolveIdentifierResponse, error)
	resolveVodIDFn              func(ctx context.Context, vodID string) (*pb.ResolveVodIDResponse, error)
	validateStreamKeyFn         func(ctx context.Context, streamKey string) (*pb.ValidateStreamKeyResponse, error)
}

func (m *mockCommodore) ResolvePlaybackID(ctx context.Context, playbackID string) (*pb.ResolvePlaybackIDResponse, error) {
	if m.resolvePlaybackIDFn == nil {
		return nil, nil
	}
	return m.resolvePlaybackIDFn(ctx, playbackID)
}

func (m *mockCommodore) ResolveArtifactPlaybackID(ctx context.Context, playbackID string) (*pb.ResolveArtifactPlaybackIDResponse, error) {
	if m.resolveArtifactPlaybackIDFn == nil {
		return nil, nil
	}
	return m.resolveArtifactPlaybackIDFn(ctx, playbackID)
}

func (m *mockCommodore) ResolveClipHash(ctx context.Context, clipHash string) (*pb.ResolveClipHashResponse, error) {
	if m.resolveClipHashFn == nil {
		return nil, nil
	}
	return m.resolveClipHashFn(ctx, clipHash)
}

func (m *mockCommodore) ResolveDVRHash(ctx context.Context, dvrHash string) (*pb.ResolveDVRHashResponse, error) {
	if m.resolveDVRHashFn == nil {
		return nil, nil
	}
	return m.resolveDVRHashFn(ctx, dvrHash)
}

func (m *mockCommodore) ResolveIdentifier(ctx context.Context, identifier string) (*pb.ResolveIdentifierResponse, error) {
	if m.resolveIdentifierFn == nil {
		return nil, nil
	}
	return m.resolveIdentifierFn(ctx, identifier)
}

func (m *mockCommodore) ResolveVodID(ctx context.Context, vodID string) (*pb.ResolveVodIDResponse, error) {
	if m.resolveVodIDFn == nil {
		return nil, nil
	}
	return m.resolveVodIDFn(ctx, vodID)
}

func (m *mockCommodore) ValidateStreamKey(ctx context.Context, streamKey string) (*pb.ValidateStreamKeyResponse, error) {
	if m.validateStreamKeyFn == nil {
		return nil, nil
	}
	return m.validateStreamKeyFn(ctx, streamKey)
}

func TestIsAuthOnlyPayment(t *testing.T) {
	tests := []struct {
		name    string
		payload *pb.X402PaymentPayload
		want    bool
	}{
		{
			name:    "nil payload",
			payload: nil,
			want:    false,
		},
		{
			name:    "nil inner payload",
			payload: &pb.X402PaymentPayload{},
			want:    false,
		},
		{
			name:    "nil authorization",
			payload: &pb.X402PaymentPayload{Payload: &pb.X402ExactPayload{}},
			want:    false,
		},
		{
			name:    "empty value",
			payload: &pb.X402PaymentPayload{Payload: &pb.X402ExactPayload{Authorization: &pb.X402Authorization{Value: ""}}},
			want:    false,
		},
		{
			name:    "non-numeric",
			payload: &pb.X402PaymentPayload{Payload: &pb.X402ExactPayload{Authorization: &pb.X402Authorization{Value: "abc"}}},
			want:    false,
		},
		{
			name:    "zero value",
			payload: &pb.X402PaymentPayload{Payload: &pb.X402ExactPayload{Authorization: &pb.X402Authorization{Value: "0"}}},
			want:    true,
		},
		{
			name:    "non-zero value",
			payload: &pb.X402PaymentPayload{Payload: &pb.X402ExactPayload{Authorization: &pb.X402Authorization{Value: "10"}}},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAuthOnlyPayment(tt.payload); got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestResolveResource(t *testing.T) {
	ctx := context.Background()

	t.Run("empty resource", func(t *testing.T) {
		_, err := ResolveResource(ctx, "", nil)
		if err == nil || err.Code != ErrInvalidResource {
			t.Fatalf("expected ErrInvalidResource, got %v", err)
		}
	})

	t.Run("graphql resource", func(t *testing.T) {
		resolution, err := ResolveResource(ctx, "graphql://viewer", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Kind != ResourceKindGraphQL || resolution.Resolved {
			t.Fatalf("unexpected resolution: %+v", resolution)
		}
	})

	t.Run("mcp resource normalizes", func(t *testing.T) {
		resolution, err := ResolveResource(ctx, "mcp://viewer", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Resource != "graphql://viewer" {
			t.Fatalf("unexpected resource: %s", resolution.Resource)
		}
	})

	t.Run("ingest missing key", func(t *testing.T) {
		_, err := ResolveResource(ctx, "ingest:", nil)
		if err == nil || err.Code != ErrInvalidResource {
			t.Fatalf("expected invalid resource error")
		}
	})

	t.Run("ingest resolver unavailable", func(t *testing.T) {
		_, err := ResolveResource(ctx, "ingest:stream-key", nil)
		if err == nil || err.Code != ErrResolverUnavailable {
			t.Fatalf("expected resolver unavailable")
		}
	})

	t.Run("ingest resolves stream key", func(t *testing.T) {
		commodore := &mockCommodore{
			validateStreamKeyFn: func(_ context.Context, key string) (*pb.ValidateStreamKeyResponse, error) {
				if key != "stream-key" {
					return nil, errors.New("unexpected key")
				}
				return &pb.ValidateStreamKeyResponse{Valid: true, TenantId: "tenant-1", StreamId: "stream-1"}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "ingest:stream-key", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Kind != ResourceKindStream || resolution.TenantID != "tenant-1" {
			t.Fatalf("unexpected resolution: %+v", resolution)
		}
		if !resolution.Resolved {
			t.Fatal("expected Resolved to be true when TenantID is set")
		}
	})

	t.Run("ingest stream key invalid", func(t *testing.T) {
		commodore := &mockCommodore{
			validateStreamKeyFn: func(_ context.Context, key string) (*pb.ValidateStreamKeyResponse, error) {
				return &pb.ValidateStreamKeyResponse{Valid: false}, nil
			},
		}
		_, err := ResolveResource(ctx, "ingest:bad-key", commodore)
		if err == nil || err.Code != ErrResourceNotFound {
			t.Fatalf("expected ErrResourceNotFound, got %v", err)
		}
	})

	t.Run("ingest stream key with empty tenant", func(t *testing.T) {
		commodore := &mockCommodore{
			validateStreamKeyFn: func(_ context.Context, key string) (*pb.ValidateStreamKeyResponse, error) {
				return &pb.ValidateStreamKeyResponse{Valid: true, TenantId: "", StreamId: "stream-1"}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "ingest:stream-key", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Resolved {
			t.Fatal("expected Resolved to be false when TenantID is empty")
		}
	})

	t.Run("viewer resolves artifact playback", func(t *testing.T) {
		commodore := &mockCommodore{
			resolveArtifactPlaybackIDFn: func(_ context.Context, playbackID string) (*pb.ResolveArtifactPlaybackIDResponse, error) {
				return &pb.ResolveArtifactPlaybackIDResponse{Found: true, TenantId: "tenant-1"}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "viewer://playback", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Kind != ResourceKindViewer || !resolution.Resolved {
			t.Fatalf("unexpected resolution: %+v", resolution)
		}
	})

	t.Run("viewer without commodore", func(t *testing.T) {
		_, err := ResolveResource(ctx, "viewer://playback", nil)
		if err == nil || err.Code != ErrResolverUnavailable {
			t.Fatalf("expected ErrResolverUnavailable, got %v", err)
		}
	})

	t.Run("clip relay id resolves", func(t *testing.T) {
		clipID := globalid.Encode(globalid.TypeClip, "clip-hash")
		commodore := &mockCommodore{
			resolveClipHashFn: func(_ context.Context, clipHash string) (*pb.ResolveClipHashResponse, error) {
				if clipHash != "clip-hash" {
					return nil, errors.New("unexpected clip hash")
				}
				return &pb.ResolveClipHashResponse{TenantId: "tenant-1"}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "clip://"+clipID, commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Kind != ResourceKindClip || !resolution.Resolved {
			t.Fatalf("unexpected resolution: %+v", resolution)
		}
	})

	t.Run("dvr resource", func(t *testing.T) {
		commodore := &mockCommodore{
			resolveDVRHashFn: func(_ context.Context, dvrHash string) (*pb.ResolveDVRHashResponse, error) {
				return &pb.ResolveDVRHashResponse{TenantId: "tenant-5"}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "dvr://dvr-hash", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Kind != ResourceKindDVR || resolution.TenantID != "tenant-5" {
			t.Fatalf("unexpected resolution: %+v", resolution)
		}
	})

	t.Run("stream identifier resolves", func(t *testing.T) {
		commodore := &mockCommodore{
			resolveIdentifierFn: func(_ context.Context, identifier string) (*pb.ResolveIdentifierResponse, error) {
				return &pb.ResolveIdentifierResponse{Found: true, IdentifierType: "stream_id", TenantId: "tenant-2"}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "stream://stream-id", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Kind != ResourceKindStream || resolution.TenantID != "tenant-2" {
			t.Fatalf("unexpected resolution: %+v", resolution)
		}
		if !resolution.Resolved {
			t.Fatal("expected Resolved to be true when TenantID is set")
		}
	})

	t.Run("stream identifier with empty tenant", func(t *testing.T) {
		commodore := &mockCommodore{
			resolveIdentifierFn: func(_ context.Context, identifier string) (*pb.ResolveIdentifierResponse, error) {
				return &pb.ResolveIdentifierResponse{Found: true, IdentifierType: "stream_id", TenantId: ""}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "stream://stream-id", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Resolved {
			t.Fatal("expected Resolved to be false when TenantID is empty")
		}
	})

	t.Run("vod id resolves", func(t *testing.T) {
		vodID := uuid.New().String()
		commodore := &mockCommodore{
			resolveVodIDFn: func(_ context.Context, id string) (*pb.ResolveVodIDResponse, error) {
				if id != vodID {
					return nil, errors.New("unexpected vod id")
				}
				return &pb.ResolveVodIDResponse{Found: true, TenantId: "tenant-3"}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "vod://"+vodID, commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Kind != ResourceKindVOD || resolution.TenantID != "tenant-3" {
			t.Fatalf("unexpected resolution: %+v", resolution)
		}
		if !resolution.Resolved {
			t.Fatal("expected Resolved to be true when TenantID is set")
		}
	})

	t.Run("vod id with empty tenant", func(t *testing.T) {
		vodID := uuid.New().String()
		commodore := &mockCommodore{
			resolveVodIDFn: func(_ context.Context, id string) (*pb.ResolveVodIDResponse, error) {
				return &pb.ResolveVodIDResponse{Found: true, TenantId: ""}, nil
			},
		}
		resolution, err := ResolveResource(ctx, "vod://"+vodID, commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolution.Resolved {
			t.Fatal("expected Resolved to be false when TenantID is empty")
		}
	})

	t.Run("internal routing identifier rejected", func(t *testing.T) {
		commodore := &mockCommodore{
			resolveIdentifierFn: func(_ context.Context, identifier string) (*pb.ResolveIdentifierResponse, error) {
				return &pb.ResolveIdentifierResponse{Found: true, IdentifierType: "stream"}, nil
			},
		}
		_, err := ResolveResource(ctx, "internal-name", commodore)
		if err == nil || err.Code != ErrInvalidResource {
			t.Fatalf("expected invalid resource error")
		}
	})
}

func TestSettleX402Payment(t *testing.T) {
	ctx := context.Background()

	t.Run("missing purser", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{})
		if err == nil || err.Code != ErrSettlementFailed {
			t.Fatalf("expected settlement failed")
		}
	})

	t.Run("missing payment", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{Purser: &mockPurser{}})
		if err == nil || err.Code != ErrInvalidPayment {
			t.Fatalf("expected invalid payment")
		}
	})

	t.Run("invalid header", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:        &mockPurser{},
			PaymentHeader: "not-base64",
		})
		if err == nil || err.Code != ErrInvalidPayment {
			t.Fatalf("expected invalid payment")
		}
	})

	t.Run("valid header parses successfully", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, p *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				if p == nil {
					return nil, errors.New("payload not parsed from header")
				}
				return &pb.VerifyX402PaymentResponse{Valid: true}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return &pb.SettleX402PaymentResponse{Success: true}, nil
			},
		}
		header := validPaymentHeader()
		result, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:        purser,
			PaymentHeader: header,
			AuthTenantID:  "tenant-1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == nil {
			t.Fatal("expected result")
		}
	})

	t.Run("auth-only payload", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:  &mockPurser{},
			Payload: paymentPayload("0"),
		})
		if err == nil || err.Code != ErrAuthOnly {
			t.Fatalf("expected auth-only error")
		}
	})

	t.Run("resource required without auth", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:  &mockPurser{},
			Payload: paymentPayload("10"),
		})
		if err == nil || err.Code != ErrAuthRequired {
			t.Fatalf("expected auth required")
		}
	})

	t.Run("viewer unresolved", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       &mockPurser{},
			Payload:      paymentPayload("10"),
			Resource:     "viewer://playback",
			AuthTenantID: "tenant-1",
			Resolution: &ResourceResolution{
				Resource: "viewer://playback",
				Kind:     ResourceKindViewer,
				Resolved: false,
			},
		})
		if err == nil || err.Code != ErrResourceNotFound {
			t.Fatalf("expected resource not found")
		}
	})

	t.Run("viewer resolved but empty tenant", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       &mockPurser{},
			Payload:      paymentPayload("10"),
			Resource:     "viewer://playback",
			AuthTenantID: "tenant-1",
			Resolution: &ResourceResolution{
				Resource: "viewer://playback",
				Kind:     ResourceKindViewer,
				TenantID: "",
				Resolved: true,
			},
		})
		if err == nil || err.Code != ErrResourceNotFound {
			t.Fatalf("expected resource not found for viewer with empty tenant")
		}
	})

	t.Run("viewer nil resolution", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       &mockPurser{},
			Payload:      paymentPayload("10"),
			Resource:     "viewer://playback",
			AuthTenantID: "tenant-1",
			Resolution:   nil,
		})
		if err == nil || err.Code != ErrResolverUnavailable {
			t.Fatalf("expected resolver unavailable for nil resolution")
		}
	})

	t.Run("viewer fully resolved succeeds", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, tenantID string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				if tenantID != "viewer-tenant" {
					t.Errorf("expected tenant viewer-tenant, got %s", tenantID)
				}
				return &pb.VerifyX402PaymentResponse{Valid: true}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return &pb.SettleX402PaymentResponse{Success: true}, nil
			},
		}
		result, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:   purser,
			Payload:  paymentPayload("10"),
			Resource: "viewer://playback",
			Resolution: &ResourceResolution{
				Resource: "viewer://playback",
				Kind:     ResourceKindViewer,
				TenantID: "viewer-tenant",
				Resolved: true,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.TargetTenantID != "viewer-tenant" {
			t.Fatalf("expected viewer-tenant, got %s", result.TargetTenantID)
		}
	})

	t.Run("target mismatch", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       &mockPurser{},
			Payload:      paymentPayload("10"),
			Resource:     "stream://stream-id",
			AuthTenantID: "tenant-1",
			Resolution: &ResourceResolution{
				Resource: "stream://stream-id",
				Kind:     ResourceKindStream,
				TenantID: "tenant-2",
				Resolved: true,
			},
		})
		if err == nil || err.Code != ErrTargetMismatch {
			t.Fatalf("expected target mismatch")
		}
	})

	t.Run("unresolved creator not allowed", func(t *testing.T) {
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       &mockPurser{},
			Payload:      paymentPayload("10"),
			Resource:     "stream://stream-id",
			AuthTenantID: "tenant-1",
			Resolution: &ResourceResolution{
				Resource: "stream://stream-id",
				Kind:     ResourceKindStream,
				Resolved: false,
			},
		})
		if err == nil || err.Code != ErrResourceNotFound {
			t.Fatalf("expected resource not found")
		}
	})

	t.Run("verification error", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return nil, errors.New("bad verify")
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrVerificationFailed {
			t.Fatalf("expected verification failed")
		}
	})

	t.Run("verification invalid response", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: false, Error: "custom verify error"}, nil
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrVerificationFailed {
			t.Fatalf("expected verification failed")
		}
		if err.Message != "custom verify error" {
			t.Fatalf("expected custom error message, got %q", err.Message)
		}
	})

	t.Run("verification nil response", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return nil, nil
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrVerificationFailed {
			t.Fatalf("expected verification failed")
		}
	})

	t.Run("billing details required", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true, RequiresBillingDetails: true}, nil
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrBillingDetailsRequired {
			t.Fatalf("expected billing details required")
		}
	})

	t.Run("verify auth-only", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true, IsAuthOnly: true}, nil
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrAuthOnly {
			t.Fatalf("expected auth-only error")
		}
	})

	t.Run("settle error", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return nil, errors.New("bad settle")
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrSettlementFailed {
			t.Fatalf("expected settlement failed")
		}
	})

	t.Run("settle invalid response", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return &pb.SettleX402PaymentResponse{Success: false, Error: "custom settle error"}, nil
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrSettlementFailed {
			t.Fatalf("expected settlement failed")
		}
		if err.Message != "custom settle error" {
			t.Fatalf("expected custom error message, got %q", err.Message)
		}
	})

	t.Run("settle nil response", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return nil, nil
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrSettlementFailed {
			t.Fatalf("expected settlement failed")
		}
	})

	t.Run("settle auth-only", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return &pb.SettleX402PaymentResponse{Success: true, IsAuthOnly: true}, nil
			},
		}
		_, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err == nil || err.Code != ErrAuthOnly {
			t.Fatalf("expected auth-only error")
		}
	})

	t.Run("allow unresolved creator with auth", func(t *testing.T) {
		commodore := &mockCommodore{
			resolveIdentifierFn: func(_ context.Context, _ string) (*pb.ResolveIdentifierResponse, error) {
				return &pb.ResolveIdentifierResponse{Found: false}, nil
			},
		}
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return &pb.SettleX402PaymentResponse{Success: true}, nil
			},
		}
		result, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:                 purser,
			Commodore:              commodore,
			Payload:                paymentPayload("10"),
			Resource:               "stream://new-stream",
			AuthTenantID:           "tenant-1",
			AllowUnresolvedCreator: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == nil {
			t.Fatal("expected result")
		}
	})

	t.Run("payer address from verify response", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true, PayerAddress: "0xverify-addr"}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return &pb.SettleX402PaymentResponse{Success: true, PayerAddress: ""}, nil
			},
		}
		result, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.PayerAddress != "0xverify-addr" {
			t.Fatalf("expected payer from verify, got %q", result.PayerAddress)
		}
	})

	t.Run("payer address from payload when responses empty", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				return &pb.VerifyX402PaymentResponse{Valid: true, PayerAddress: ""}, nil
			},
			settleFn: func(_ context.Context, _ string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				return &pb.SettleX402PaymentResponse{Success: true, PayerAddress: ""}, nil
			},
		}
		result, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.PayerAddress != "0xfrom" {
			t.Fatalf("expected payer from payload, got %q", result.PayerAddress)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		purser := &mockPurser{
			verifyFn: func(_ context.Context, tenantID string, payment *pb.X402PaymentPayload, _ string) (*pb.VerifyX402PaymentResponse, error) {
				if tenantID != "tenant-1" {
					return nil, errors.New("unexpected tenant")
				}
				if payment == nil {
					return nil, errors.New("missing payment")
				}
				return &pb.VerifyX402PaymentResponse{Valid: true, PayerAddress: "0xverify"}, nil
			},
			settleFn: func(_ context.Context, tenantID string, _ *pb.X402PaymentPayload, _ string) (*pb.SettleX402PaymentResponse, error) {
				if tenantID != "tenant-1" {
					return nil, errors.New("unexpected tenant")
				}
				return &pb.SettleX402PaymentResponse{Success: true, PayerAddress: "0xsettle"}, nil
			},
		}
		result, err := SettleX402Payment(ctx, SettlementOptions{
			Purser:       purser,
			Payload:      paymentPayload("10"),
			AuthTenantID: "tenant-1",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.TargetTenantID != "tenant-1" {
			t.Fatalf("unexpected tenant: %s", result.TargetTenantID)
		}
		if result.PayerAddress != "0xsettle" {
			t.Fatalf("unexpected payer: %s", result.PayerAddress)
		}
	})

	t.Run("stream resource with empty tenant returns unresolved", func(t *testing.T) {
		commodore := &MockCommodoreClient{
			IdentifierResponse: &pb.ResolveIdentifierResponse{Found: true, IdentifierType: "stream_id", TenantId: ""},
		}
		res, err := ResolveResource(context.Background(), "stream://stream-id", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Resolved {
			t.Fatal("expected unresolved when tenant is empty")
		}
	})

	t.Run("ingest key with empty tenant returns unresolved", func(t *testing.T) {
		commodore := &MockCommodoreClient{
			StreamKeyResponse: &pb.ValidateStreamKeyResponse{Valid: true, StreamId: "stream-id", TenantId: ""},
		}
		res, err := ResolveResource(context.Background(), "ingest:stream-key", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Resolved {
			t.Fatal("expected unresolved when tenant is empty")
		}
	})

	t.Run("vod resource with empty tenant returns unresolved", func(t *testing.T) {
		commodore := &MockCommodoreClient{
			VodResponse: &pb.ResolveVodIDResponse{Found: true, TenantId: ""},
		}
		res, err := ResolveResource(context.Background(), "vod://4c0883a6-9102-4937-9d62-31471b5dbb62", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Resolved {
			t.Fatal("expected unresolved when tenant is empty")
		}
	})

	t.Run("vod hash with empty tenant returns unresolved", func(t *testing.T) {
		commodore := &MockCommodoreClient{
			IdentifierResponse: &pb.ResolveIdentifierResponse{Found: true, IdentifierType: "vod_hash", TenantId: ""},
		}
		res, err := ResolveResource(context.Background(), "vod://some-hash", commodore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Resolved {
			t.Fatal("expected unresolved when tenant is empty")
		}
	})
}

func paymentPayload(value string) *pb.X402PaymentPayload {
	return &pb.X402PaymentPayload{
		X402Version: 1,
		Scheme:      "exact",
		Network:     "base",
		Payload: &pb.X402ExactPayload{
			Signature: "0xsignature",
			Authorization: &pb.X402Authorization{
				From:        "0xfrom",
				To:          "0xto",
				Value:       value,
				ValidAfter:  fmt.Sprintf("%d", time.Now().Add(-time.Minute).Unix()),
				ValidBefore: fmt.Sprintf("%d", time.Now().Add(time.Minute).Unix()),
				Nonce:       "0x01",
			},
		},
	}
}

func validPaymentHeader() string {
	payload := paymentPayload("10")
	jsonBytes, _ := json.Marshal(payload)
	return base64.StdEncoding.EncodeToString(jsonBytes)
}

func makePaymentHeader(value string) string {
	payload := map[string]interface{}{
		"x402Version": 1,
		"scheme":      "exact",
		"network":     "base-sepolia",
		"payload": map[string]interface{}{
			"authorization": map[string]interface{}{
				"from":  "0xfrom",
				"to":    "0xto",
				"value": value,
			},
		},
	}
	data, _ := json.Marshal(payload)
	return base64.StdEncoding.EncodeToString(data)
}
