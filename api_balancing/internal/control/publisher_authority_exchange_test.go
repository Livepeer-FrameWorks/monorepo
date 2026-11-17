package control

import (
	"bytes"
	"context"
	"errors"
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/protobuf/proto"
)

type authorityExchangeClient struct {
	PublisherLeaseClient
	acquire func(context.Context, *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error)
}

func (c authorityExchangeClient) AcquirePublisherLease(ctx context.Context, req *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
	return c.acquire(ctx, req)
}

func TestPublishedLeaseExchangesOnlyVerifiedNewerPairWithinSameExecution(t *testing.T) {
	request := &pb.PublisherLeaseRequest{TenantId: "tenant", StreamId: "stream", InternalName: "internal", NodeId: "node", ClusterId: "cluster", ControlCellId: "cell", ExecutionToken: "execution", TenantAuthorityVersion: 1, ObjectAuthorityVersion: 1, TenantPayloadSha256: bytes.Repeat([]byte{1}, 32), ObjectPayloadSha256: bytes.Repeat([]byte{2}, 32)}
	identity := &pb.ValidateStreamKeyResponse{Valid: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", ProcessesJson: "old-config"}
	for _, scenario := range []string{"current", "churn", "old-server", "credential-callback-denied", "verification-callback-denied", "wrong-node", "same-version", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls, refreshes := 0, 0
			client := authorityExchangeClient{acquire: func(_ context.Context, got *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
				calls++
				if got.GetExecutionToken() != "execution" || got.GetNodeId() != "node" || got.GetControlCellId() != "cell" {
					t.Fatal("authority exchange changed publisher execution identity")
				}
				if calls == 2 && scenario != "churn" {
					if got.GetObjectAuthorityVersion() != 2 {
						t.Fatal("lease CAS used old authority after refresh")
					}
					return &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED}, nil
				}
				result := &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE}
				if scenario != "old-server" {
					result.SignedAuthorities = [][]byte{[]byte("tenant"), []byte("object")}
				}
				return result, nil
			}}
			adapter := &publishedClaimClient{PublisherLeaseClient: client, request: proto.CloneOf(request), identity: proto.CloneOf(identity)}
			adapter.refresh = func(_ context.Context, pair [][]byte) (*pb.PublisherLeaseRequest, *pb.ValidateStreamKeyResponse, error) {
				refreshes++
				if len(pair) != 2 {
					t.Fatal("current signed pair missing")
				}
				if scenario == "verification-callback-denied" {
					return nil, nil, errors.New("signature rejected")
				}
				if scenario == "credential-callback-denied" {
					return nil, &pb.ValidateStreamKeyResponse{Valid: false, ClaimAcquired: true, RejectionReason: pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY}, nil
				}
				next, validated := proto.CloneOf(request), proto.CloneOf(identity)
				next.ObjectAuthorityVersion = 2
				next.ObjectPayloadSha256 = bytes.Repeat([]byte{3}, 32)
				validated.ProcessesJson = "current-config"
				switch scenario {
				case "wrong-node":
					next.NodeId = "other-node"
				case "same-version":
					next.ObjectAuthorityVersion = 1
				case "canceled":
					cancel()
				}
				return next, validated, nil
			}
			got, err := adapter.ValidateStreamKeyForClaim(ctx, "", "cluster", "execution")
			switch scenario {
			case "current":
				if err != nil || !got.GetValid() || !got.GetClaimAcquired() || got.GetProcessesJson() != "current-config" || calls != 2 || refreshes != 1 {
					t.Fatalf("current-pair exchange failed: got=%v err=%v calls=%d refreshes=%d", got, err, calls, refreshes)
				}
			case "credential-callback-denied":
				if err != nil || got.GetValid() || got.GetClaimAcquired() || calls != 1 {
					t.Fatalf("revoked credential reached another lease CAS: calls=%d got=%v err=%v", calls, got, err)
				}
			default:
				if err == nil || got != nil {
					t.Fatalf("unsafe authority exchange succeeded: got=%v err=%v", got, err)
				}
				wantCalls := 1
				if scenario == "churn" {
					wantCalls = 2
				}
				if calls != wantCalls || refreshes > 1 || (scenario == "old-server" && refreshes != 0) {
					t.Fatalf("unbounded or unsupported exchange: calls=%d refreshes=%d", calls, refreshes)
				}
			}
		})
	}
}

func TestPublishedLeaseExchangeBudgetSurvivesEndedOwnerRecovery(t *testing.T) {
	request := &pb.PublisherLeaseRequest{TenantId: "tenant", StreamId: "stream", InternalName: "internal", NodeId: "node", ClusterId: "cluster", ControlCellId: "cell", ExecutionToken: "execution", TenantAuthorityVersion: 1, ObjectAuthorityVersion: 1, TenantPayloadSha256: bytes.Repeat([]byte{1}, 32), ObjectPayloadSha256: bytes.Repeat([]byte{2}, 32)}
	identity := &pb.ValidateStreamKeyResponse{Valid: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal"}
	calls, refreshes := 0, 0
	client := authorityExchangeClient{acquire: func(_ context.Context, _ *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
		calls++
		if calls == 2 {
			return &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER, HeldExecutionToken: "ended-owner"}, nil
		}
		return &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE, SignedAuthorities: [][]byte{[]byte("tenant"), []byte("object")}}, nil
	}}
	adapter := &publishedClaimClient{PublisherLeaseClient: client, request: request, identity: identity}
	adapter.refresh = func(_ context.Context, _ [][]byte) (*pb.PublisherLeaseRequest, *pb.ValidateStreamKeyResponse, error) {
		refreshes++
		next := proto.CloneOf(request)
		next.ObjectAuthorityVersion = int64(1 + refreshes)
		next.ObjectPayloadSha256 = bytes.Repeat([]byte{byte(2 + refreshes)}, 32)
		return next, proto.CloneOf(identity), nil
	}
	first, err := adapter.ValidateStreamKeyForClaim(t.Context(), "", "cluster", "execution")
	if err != nil || first.GetHeldClaimToken() != "ended-owner" {
		t.Fatalf("first claim = %v, %v", first, err)
	}
	// ClaimIngestPlacement re-enters the same adapter after proving the held owner ended.
	_, err = adapter.ValidateStreamKeyForClaim(t.Context(), "", "cluster", "execution")
	if !errors.Is(err, ErrPublisherAuthorityStale) || calls != 3 || refreshes != 1 {
		t.Fatalf("ended-owner retry reset the operation authority budget: calls=%d refreshes=%d err=%v", calls, refreshes, err)
	}
}
