package control

import (
	"os"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func managedRetractionFixture(command *ipcpb.ApplyManagedStream) *ipcpb.RetractManagedStream {
	a := command.GetPlacementAdmission()
	now := time.Now()
	return &ipcpb.RetractManagedStream{Name: command.GetName(), StreamId: command.GetStreamId(), PlacementRetraction: &ipcpb.ManagedStreamRetraction{
		TargetNodeId: a.GetTargetNodeId(), TargetClusterId: a.GetTargetClusterId(), TenantId: command.GetTenantId(), ObjectAuthorityVersion: a.GetObjectAuthorityVersion(), TenantAuthorityVersion: a.GetTenantAuthorityVersion(),
		PolicyRevision: a.GetPolicyRevision(), ParentRevision: a.GetParentRevision(), PolicyDigest: a.GetPolicyDigest(), CommandSha256: append([]byte(nil), a.GetCommandSha256()...),
		IssuedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(10 * time.Second)),
	}}
}

func TestManagedRetirementRejectsReplayInEitherArrivalOrder(t *testing.T) {
	for _, first := range []string{"apply", "retract"} {
		t.Run(first, func(t *testing.T) {
			directory := t.TempDir()
			command := managedCommandFixture(t)
			if first == "apply" {
				release, err := acquireManagedPlacement(directory, "node", command, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				release()
			}
			retire := managedRetractionFixture(command)
			for range 2 {
				release, err := acquireManagedRetraction(directory, "node", retire, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if overlapping, overlapErr := acquireManagedPlacement(directory, "node", command, time.Now()); overlapErr == nil {
					overlapping()
					t.Fatal("Apply bypassed retraction dispatch lock")
				}
				release()
			}
			retired, retireReadErr := lockManagedPlacementFence(directory, command.GetName())
			if retireReadErr != nil {
				t.Fatal(retireReadErr)
			}
			if retired.previous == nil || retired.previous.Schema != 2 || !retired.previous.Retired {
				retired.release()
				t.Fatal("retirement did not fence older-format readers")
			}
			retired.release()
			if release, err := acquireManagedPlacement(directory, "node", command, time.Now()); err == nil {
				release()
				t.Fatal("retired Apply replayed")
			}
			next := proto.CloneOf(command)
			next.PlacementAdmission.ObjectAuthorityVersion++
			next.Source = "file:/next.ts"
			bindManagedCommand(t, next)
			release, err := acquireManagedPlacement(directory, "node", next, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			release()
			if release, err := acquireManagedRetraction(directory, "node", retire, time.Now()); err == nil {
				release()
				t.Fatal("old retirement deleted newer authority")
			}
		})
	}
}

func TestManagedRetirementHandlerFencesDeleteAndAcknowledgesConfig(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	currentConfig.NodeID, currentConfig.StateDir = "node", t.TempDir()
	command := managedCommandFixture(t)
	handleApplyManagedStream(logging.NewLogger(), command)
	ack := snapshotAppliedManagedStreamsForRegister()
	if len(ack) != 1 || ack[0].GetTenantId() != command.GetTenantId() || !proto.Equal(ack[0].GetPlacementAdmission(), command.GetPlacementAdmission()) {
		t.Fatalf("configuration acknowledgement lost admission: %+v", ack)
	}
	retire := managedRetractionFixture(command)
	wrong := proto.CloneOf(retire)
	wrong.PlacementRetraction.ObjectAuthorityVersion++
	handleRetractManagedStream(logging.NewLogger(), wrong)
	if len(mock.callsContainingKey("deletestream")) != 0 {
		t.Fatal("wrong retraction reached Mist")
	}
	handleRetractManagedStream(logging.NewLogger(), retire)
	if len(mock.callsContainingKey("deletestream")) != 1 || len(snapshotAppliedManagedStreamsForRegister()) != 0 {
		t.Fatal("exact retirement failed")
	}
	handleApplyManagedStream(logging.NewLogger(), command)
	if len(mock.callsContainingKey("addstream")) != 1 {
		t.Fatal("retired command restarted media")
	}
	command.PlacementAdmission.ObjectAuthorityVersion++
	command.Source = "file:/next.ts"
	bindManagedCommand(t, command)
	handleApplyManagedStream(logging.NewLogger(), command)
	handleRetractManagedStream(logging.NewLogger(), retire)
	if len(mock.callsContainingKey("addstream")) != 2 || len(mock.callsContainingKey("deletestream")) != 1 {
		t.Fatal("late retirement damaged newer source")
	}
}

func TestManagedRetirementHandlerBeforeFirstApply(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	currentConfig.NodeID, currentConfig.StateDir = "node", t.TempDir()
	command := managedCommandFixture(t)
	handleRetractManagedStream(logging.NewLogger(), managedRetractionFixture(command))
	handleApplyManagedStream(logging.NewLogger(), command)
	if len(mock.callsContainingKey("addstream")) != 0 || len(mock.callsContainingKey("deletestream")) != 0 {
		t.Fatal("out-of-order retired source reached media mutation")
	}
}

func TestManagedRetirementRejectsMalformedAuthorityBeforePersistence(t *testing.T) {
	for _, scenario := range []string{"wrong node", "missing tenant", "zero version", "digest", "expired", "overlong"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			retire := managedRetractionFixture(managedCommandFixture(t))
			switch scenario {
			case "wrong node":
				retire.PlacementRetraction.TargetNodeId = "another"
			case "missing tenant":
				retire.PlacementRetraction.TenantId = ""
			case "zero version":
				retire.PlacementRetraction.ObjectAuthorityVersion = 0
			case "digest":
				retire.PlacementRetraction.PolicyDigest = "invalid"
			case "expired":
				retire.PlacementRetraction.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
			case "overlong":
				retire.PlacementRetraction.ExpiresAt = timestamppb.New(time.Now().Add(time.Minute))
			}
			if release, err := acquireManagedRetraction(directory, "node", retire, time.Now()); err == nil {
				release()
				t.Fatal("invalid retirement created a fence")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("invalid retirement touched persistent state")
			}
		})
	}
}
