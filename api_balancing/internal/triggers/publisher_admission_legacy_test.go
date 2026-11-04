package triggers

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/ingesterrors"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type settlementCompatibilityCommodore struct {
	placementCapturingCommodore
	leases       atomic.Int32
	fencedClaims atomic.Int32
	legacyClaims atomic.Int32
}

func (c *settlementCompatibilityCommodore) AcquirePublisherLease(context.Context, *commodorepb.PublisherLeaseRequest) (*commodorepb.PublisherLeaseResponse, error) {
	c.leases.Add(1)
	return &commodorepb.PublisherLeaseResponse{Outcome: commodorepb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED}, nil
}

func (c *settlementCompatibilityCommodore) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	if req.GetClaimToken() != "" {
		if req.GetNodeId() != "" && req.GetControlCellId() != "" {
			c.fencedClaims.Add(1)
		} else {
			c.legacyClaims.Add(1)
		}
	}
	response, err := c.placementCapturingCommodore.ValidateStreamKey(ctx, req)
	response.PublisherExecutionFenced = true
	return response, err
}
func (c *settlementCompatibilityCommodore) CheckStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	return c.ValidateStreamKey(ctx, req)
}

func settlementCompatibilityClient(t *testing.T) (*commodore.GRPCClient, *settlementCompatibilityCommodore) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	fake := &settlementCompatibilityCommodore{}
	server := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(server, fake)
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	client, err := commodore.NewGRPCClient(commodore.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, ServiceToken: "test-token", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, fake
}

func TestPublisherAdmissionLegacyDomainDenialReturnsLeaseSynchronously(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		deadline *timestamppb.Timestamp
		legacy   bool
	}{
		{"missing deadline", nil, true},
		{"invalid deadline", &timestamppb.Timestamp{Seconds: 253402300800}, true},
		{"capable deadline", nil, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
			t.Cleanup(closeDB)
			tenant, object := &mediapb.TenantAuthority{}, &mediapb.MediaObjectAuthority{}
			if err := proto.Unmarshal(tenantBytes, tenant); err != nil {
				t.Fatal(err)
			}
			if err := proto.Unmarshal(objectBytes, object); err != nil {
				t.Fatal(err)
			}
			tenant.SchemaVersion, object.SchemaVersion = 4, 4
			tenant.MediaPlacement = &placementpb.PolicySet{Revision: 1}
			object.MediaPlacement = &placementpb.PolicySet{}
			object.PlacementTenantRevision = 1
			object.GetLiveStream().PublisherConfigurationVersion = 1
			tenantBytes, _ = proto.MarshalOptions{Deterministic: true}.Marshal(tenant)
			objectBytes, _ = proto.MarshalOptions{Deterministic: true}.Marshal(object)
			previousEnforcement := localauthority.PlacementEnforced()
			localauthority.SetPlacementEnforced(true)
			t.Cleanup(func() { localauthority.SetPlacementEnforced(previousEnforcement) })
			columns := []string{"session_cluster_id", "object_payload", "object_payload_sha256", "object_refresh_after", "object_valid_until", "object_authority_id", "object_authority_version", "object_read_ready", "object_ingest_ready", "object_source_ready", "tenant_payload", "tenant_payload_sha256", "tenant_refresh_after", "tenant_valid_until", "tenant_authority_version", "tenant_read_ready", "tenant_ingest_ready", "tenant_source_ready", "tenant_previous_placement_policy", "tenant_placement_superseded_at", "object_withheld_by_tenant_revival"}
			mock.ExpectQuery(`GetPublisherAuthorityPreflight`).WillReturnRows(sqlmock.NewRows(columns).AddRow("", objectBytes, localPayloadDigest(objectBytes), time.Now().Add(time.Minute), time.Now().Add(time.Hour), sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId()), int64(1), true, true, true, tenantBytes, localPayloadDigest(tenantBytes), time.Now().Add(time.Minute), time.Now().Add(time.Hour), int64(1), true, true, true, nil, nil, false))
			if scenario.legacy {
				mock.ExpectQuery(`FROM foghorn\.media_object_authority_projection`).WillReturnError(sql.ErrNoRows)
			}
			client, fake := settlementCompatibilityClient(t)
			p := NewProcessor(logging.NewLogger(), client, nil, nil, nil)
			p.SetMediaAuthorityStore(fixture.mediaAuthorityStore)
			p.SetPublisherAdmissionRepository(control.NewPublisherAdmissionRepository(nil))
			p.SetClusterID("cluster-a")
			installRegistryForTest(t)
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(func() { state.ResetDefaultManagerForTests() })
			sm.SetNodeConnectionInfo(t.Context(), "node-A", "node-A:18090", "", "cluster-a", nil)
			claimAcquired.Store(true)
			t.Cleanup(func() { claimAcquired.Store(false) })
			placementCalled := false
			p.SetIngestPlacementAdmission(func(context.Context, IngestPlacementConnection) (federation.PlacementAdmissionDecision, error) {
				placementCalled = true
				return federation.PlacementAdmissionDecision{}, status.Error(codes.PermissionDenied, "placement denied")
			})
			trigger := &ipcpb.MistTrigger{NodeId: "node-A", PublisherDeadline: scenario.deadline, TriggerType: "PUSH_REWRITE", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "legacy-settlement", TriggerUnixMillis: 1, StreamName: "sk_local", ObservedConnector: "RTMP"}}}
			if !scenario.legacy {
				trigger.PublisherDeadline = timestamppb.New(time.Now().Add(3 * time.Second))
			}
			ctx, cancel := control.LegacyPublisherContext(t.Context(), trigger)
			defer cancel()
			_, _, publishErr := p.ProcessPublisher(ctx, trigger)
			ingestErr, ok := errors.AsType[*ingesterrors.IngestError](publishErr)
			if !placementCalled || !ok || ingestErr.Code != ipcpb.IngestErrorCode_INGEST_ERROR_PLACEMENT_DENIED {
				t.Fatalf("expected post-lease placement denial: called=%v err=%v", placementCalled, publishErr)
			}
			releases := fake.requests()
			if scenario.legacy {
				if len(releases) != 1 || len(releases[0].GetRelease()) != 1 {
					t.Fatalf("older edge will skip abandonment WAL, but denial did not release its lease synchronously: %+v", releases)
				}
				if fake.leases.Load() != 0 {
					t.Fatal("older edge used the new atomic lease contract")
				}
			} else if len(releases) != 0 || fake.leases.Load() != 1 {
				t.Fatalf("capable edge lost atomic admission: releases=%d leases=%d", len(releases), fake.leases.Load())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublisherAdmissionColdClaimRetainsCapableExecutionFencing(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "old edge", false: "capable edge"}[legacy], func(t *testing.T) {
			mock := installControlDBForTest(t)
			mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnError(sql.ErrNoRows)
			client, fake := settlementCompatibilityClient(t)
			p := NewProcessor(logging.NewLogger(), client, nil, nil, nil)
			p.SetPublisherAdmissionRepository(control.NewPublisherAdmissionRepository(nil))
			p.SetClusterID("cluster-a")
			installRegistryForTest(t)
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(func() { state.ResetDefaultManagerForTests() })
			sm.SetNodeConnectionInfo(t.Context(), "node-A", "node-A:18090", "", "cluster-a", nil)
			claimAcquired.Store(true)
			t.Cleanup(func() { claimAcquired.Store(false) })
			placementCalled := false
			p.SetIngestPlacementAdmission(func(context.Context, IngestPlacementConnection) (federation.PlacementAdmissionDecision, error) {
				placementCalled = true
				return federation.PlacementAdmissionDecision{}, status.Error(codes.PermissionDenied, "placement denied")
			})
			trigger := &ipcpb.MistTrigger{NodeId: "node-A", ControlCellId: proto.String("cell-a"), TriggerType: "PUSH_REWRITE", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "cold-settlement", TriggerUnixMillis: 1, StreamName: "sk_cold", ObservedConnector: "RTMP"}}}
			if !legacy {
				trigger.PublisherDeadline = timestamppb.New(time.Now().Add(3 * time.Second))
			}
			ctx, cancel := control.LegacyPublisherContext(t.Context(), trigger)
			defer cancel()
			_, _, publishErr := p.ProcessPublisher(ctx, trigger)
			if !placementCalled || publishErr == nil {
				t.Fatalf("cold claim failed before expected placement refusal: called=%v err=%v", placementCalled, publishErr)
			}
			if legacy && (fake.legacyClaims.Load() != 1 || fake.fencedClaims.Load() != 0) {
				t.Fatalf("older edge used execution-fenced cold claim: old=%d fenced=%d", fake.legacyClaims.Load(), fake.fencedClaims.Load())
			}
			if !legacy && (fake.legacyClaims.Load() != 0 || fake.fencedClaims.Load() != 1) {
				t.Fatalf("capable cold claim lost execution fencing: old=%d fenced=%d", fake.legacyClaims.Load(), fake.fencedClaims.Load())
			}
			if len(fake.requests()) != 1 {
				t.Fatal("known cold-claim denial did not release its own lease")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
