package triggers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/identity"
	"frameworks/api_balancing/internal/state"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestPolledEndReportsKeepRetainedOwnerAfterDenial(t *testing.T) {
	for _, family := range []string{"offline", "disconnect"} {
		for _, denial := range []string{"tombstone", "suspended"} {
			t.Run(family+"/"+denial, func(t *testing.T) {
				p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
				defer closeDB()
				previousDB, previousRegistry := control.GetDB(), control.StreamRegistryInstance
				control.SetDB(nil)
				control.StreamRegistryInstance = nil
				t.Cleanup(func() { control.SetDB(previousDB); control.StreamRegistryInstance = previousRegistry })
				state.ResetDefaultManagerForTests()
				t.Cleanup(func() { state.ResetDefaultManagerForTests() })
				object := &mediaauthoritypb.MediaObjectAuthority{}
				tenant := &mediaauthoritypb.TenantAuthority{}
				if err := proto.Unmarshal(objectBytes, object); err != nil {
					t.Fatal(err)
				}
				if err := proto.Unmarshal(tenantBytes, tenant); err != nil {
					t.Fatal(err)
				}
				if denial == "tombstone" {
					object.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
				} else {
					tenant.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
				}
				objectBytes, err := proto.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				tenantBytes, err = proto.Marshal(tenant)
				if err != nil {
					t.Fatal(err)
				}
				expectLocalPair(mock, objectBytes, tenantBytes, time.Now().Add(time.Hour), time.Now().Add(time.Hour), true)
				owner := object.GetTenantId()
				trigger := &ipcpb.MistTrigger{NodeId: "edge", TriggerUnixMillis: time.Now().UnixMilli()}
				if family == "offline" {
					trigger.TriggerType, trigger.Blocking = "STREAM_LIFECYCLE_UPDATE", true
					trigger.TriggerPayload = &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{NodeId: "edge", TenantId: &owner, InternalName: "stream-internal", Status: "offline"}}
				} else {
					trigger.TriggerType = "CLIENT_LIFECYCLE_UPDATE"
					trigger.TriggerPayload = &ipcpb.MistTrigger_ClientLifecycleUpdate{ClientLifecycleUpdate: &ipcpb.ClientLifecycleUpdate{NodeId: "edge", TenantId: &owner, InternalName: "live+stream-internal", Action: "disconnect"}}
					sender := &recordingSender{}
					p.clientBatcher = newClientLifecycleBatcher(sender.send, testLogger(), nil)
					t.Cleanup(func() {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						if err := p.Shutdown(ctx); err != nil {
							t.Error(err)
						}
					})
					if _, _, err := p.ProcessTypedTrigger(trigger); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
					defer cancel()
					if err := p.Shutdown(ctx); err != nil {
						t.Fatal(err)
					}
					batches := sender.snapshot()
					if len(batches) != 1 || batches[0].GetTenantId() != owner || batches[0].GetStreamId() != object.GetLiveStream().GetStreamId() {
						t.Fatalf("verified disconnect identity discarded: %+v", batches)
					}
				}
				if family == "offline" {
					if _, _, err := p.ProcessTypedTrigger(trigger); err != nil {
						t.Fatalf("verified offline report pins its acknowledged queue: %v", err)
					}
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestTerminalResourceReportsAcknowledgeOnlyDefinitiveAbsence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lookupErr error
		wantErr   bool
	}{
		{"absent response", nil, false},
		{"not found status", status.Error(codes.NotFound, "stream not found"), false},
		{"temporarily unavailable", status.Error(codes.Unavailable, "database unavailable"), true},
		{"access denied", status.Error(codes.PermissionDenied, "policy denied"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := setupCommodoreResolveIdentifierClient(t, &commodorepb.ResolveIdentifierResponse{}, tc.lookupErr)
			defer cleanup()
			p := newTestProcessor(t)
			p.commodoreClient = client
			absent := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_absent_resource_reports"})
			p.metrics = &ProcessorMetrics{AbsentResourceReports: absent}
			for _, trigger := range []*ipcpb.MistTrigger{
				{TriggerType: "STREAM_END", NodeId: "node", TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+deleted-resource"}}},
				{TriggerType: "PUSH_INPUT_CLOSE", NodeId: "node", TriggerPayload: &ipcpb.MistTrigger_PushInputClose{PushInputClose: &ipcpb.PushInputCloseTrigger{StreamName: "live+deleted-resource", Pid: 123}}},
				{TriggerType: "USER_END", NodeId: "node", TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{StreamName: "live+deleted-resource", Connector: "HLS"}}},
				{TriggerType: "RECORDING_END", NodeId: "node", TriggerPayload: &ipcpb.MistTrigger_RecordingComplete{RecordingComplete: &ipcpb.RecordingCompleteTrigger{StreamName: "live+deleted-resource"}}},
				{TriggerType: "RECORDING_SEGMENT", NodeId: "node", TriggerPayload: &ipcpb.MistTrigger_RecordingSegment{RecordingSegment: &ipcpb.RecordingSegmentTrigger{StreamName: "live+deleted-resource"}}},
				{TriggerType: "PUSH_END", NodeId: "node", TriggerPayload: &ipcpb.MistTrigger_PushEnd{PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+deleted-resource"}}},
			} {
				_, _, err := p.ProcessTypedTrigger(trigger)
				if (err != nil) != tc.wantErr {
					t.Fatalf("%s result=%v wantErr=%v", trigger.TriggerType, err, tc.wantErr)
				}
			}
			wantCount := float64(6)
			if tc.wantErr {
				wantCount = 0
			}
			if got := testutil.ToFloat64(absent); got != wantCount {
				t.Fatalf("terminal discard count=%v want=%v", got, wantCount)
			}
		})
	}
}

func TestFinalLifecycleReportsKeepRetainedOwnerAfterAdmissionDenied(t *testing.T) {
	for _, lifecycle := range []mediaauthoritypb.AuthorityLifecycle{mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE} {
		for _, kind := range []string{"STREAM_END", "PUSH_INPUT_CLOSE"} {
			p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
			object := &mediaauthoritypb.MediaObjectAuthority{}
			if err := proto.Unmarshal(objectBytes, object); err != nil {
				t.Fatal(err)
			}
			object.Lifecycle = lifecycle
			encoded, err := proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			expectLocalPair(mock, encoded, tenantBytes, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour), false)
			absent := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_final_lifecycle_absent"})
			p.metrics = &ProcessorMetrics{AbsentResourceReports: absent}
			trigger := &ipcpb.MistTrigger{TriggerType: kind, NodeId: "node"}
			if kind == "STREAM_END" {
				trigger.TriggerPayload = &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+stream-internal"}}
			} else {
				// A malformed PID must reach the finalizer validation, not be discarded as absent.
				trigger.TriggerPayload = &ipcpb.MistTrigger_PushInputClose{PushInputClose: &ipcpb.PushInputCloseTrigger{StreamName: "live+stream-internal"}}
			}
			_, _, reportErr := p.ProcessTypedTrigger(trigger)
			if trigger.GetTenantId() != object.GetTenantId() || trigger.GetStreamId() != object.GetLiveStream().GetStreamId() || testutil.ToFloat64(absent) != 0 {
				t.Fatalf("retained finalizer identity discarded: %s %+v %v", kind, trigger, reportErr)
			}
			if kind == "PUSH_INPUT_CLOSE" && (reportErr == nil || !strings.Contains(reportErr.Error(), "missing connector PID")) {
				t.Fatalf("publisher close never reached finalization: %v", reportErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			closeDB()
		}
	}
}

func TestProcessingBillingArtifactLookupOutageWinsOverCentralAbsence(t *testing.T) {
	previous := identity.Default()
	t.Cleanup(func() { identity.SetDefault(previous) })
	identity.SetDefault(identity.NewResolver(identity.Config{RegistryArtifact: func(context.Context, string) (identity.ArtifactIdentity, error) {
		return identity.ArtifactIdentity{}, errors.New("temporary database failure")
	}}))
	client, cleanup := setupCommodoreResolveIdentifierClient(t, &commodorepb.ResolveIdentifierResponse{}, nil)
	defer cleanup()
	p := newTestProcessor(t)
	p.commodoreClient = client
	trigger := &ipcpb.MistTrigger{TriggerType: "PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{ProcessBilling: &ipcpb.ProcessBillingEvent{StreamName: "processing+deleted-artifact"}}}
	if _, _, err := p.handleProcessBilling(trigger); err == nil {
		t.Fatal("temporary artifact lookup failure acknowledged and lost final billing")
	}
}

func TestProcessingBillingDeletedArtifactDoesNotPinDurableQueue(t *testing.T) {
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	object := &mediaauthoritypb.MediaObjectAuthority{}
	if err := proto.Unmarshal(objectBytes, object); err != nil {
		t.Fatal(err)
	}
	object.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	object.ObjectKind = mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
	object.Object = &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{ArtifactHash: "deleted-artifact", ParentStreamId: "30000000-0000-0000-0000-000000000001"}}
	tombstone, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	expectLocalPair(mock, tombstone, tenantBytes, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
	capture, client := startDecklogCapture(t)
	t.Cleanup(func() { _ = client.Close() })
	p.decklogClient = client
	trigger := &ipcpb.MistTrigger{TriggerType: "PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE", TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{ProcessBilling: &ipcpb.ProcessBillingEvent{StreamName: "processing+deleted-artifact"}}}
	if _, _, err := p.handleProcessBilling(trigger); err != nil {
		t.Fatalf("deleted artifact retries forever: %v", err)
	}
	received := capture.received()
	if len(received) != 1 || received[0].GetTenantId() != object.GetTenantId() || received[0].GetProcessBilling().GetTenantId() != object.GetTenantId() {
		t.Fatalf("delivered processing lost its retained owner: %+v", received)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
