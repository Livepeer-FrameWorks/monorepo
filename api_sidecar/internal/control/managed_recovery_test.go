package control

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/protobuf/proto"
)

func TestManagedPlacementRecoveryAfterSidecarRestart(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "retired"}[retired], func(t *testing.T) {
			mock := withMockMistAndCleanState(t)
			currentConfig.NodeID, currentConfig.StateDir = "node", t.TempDir()
			command := managedCommandFixture(t)
			command.AlwaysOn, command.Realtime, command.StopSessions = true, true, true
			bindManagedCommand(t, command)
			handleApplyManagedStream(logging.NewLogger(), command)
			calls := mock.callsContainingKey("addstream")
			if len(calls) != 1 {
				t.Fatal("initial configuration was not applied")
			}
			entry := calls[0]["addstream"].(map[string]any)[command.GetName()].(map[string]any)
			restartedMist := newMockMistServerWithStreams(t, map[string]map[string]any{command.GetName(): entry})
			currentConfig.MistServerURL = restartedMist.srv.URL
			if retired {
				release, err := acquireManagedRetraction(currentConfig.StateDir, "node", managedRetractionFixture(command), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				release()
			}
			appliedManagedStreams.Lock()
			appliedManagedStreams.m = make(map[string]managedStreamLocalSnapshot)
			appliedManagedStreams.Unlock()
			HydrateAppliedManagedStreamsFromMist(logging.NewLogger())
			ack := snapshotAppliedManagedStreamsForRegister()
			if len(ack) != 1 || ack[0].GetTenantId() != command.GetTenantId() || !proto.Equal(ack[0].GetPlacementAdmission(), command.GetPlacementAdmission()) || ack[0].GetPlacementRetired() != retired {
				t.Fatalf("restart lost exact configuration or cleanup identity: %+v", ack)
			}
			if len(restartedMist.callsContainingKey("addstream")) != 0 {
				t.Fatal("recovery dispatched a positive media command")
			}
			handleRetractManagedStream(logging.NewLogger(), managedRetractionFixture(command))
			if len(restartedMist.callsContainingKey("deletestream")) != 1 || len(snapshotAppliedManagedStreamsForRegister()) != 0 {
				t.Fatal("recovered exact cleanup failed")
			}
		})
	}
}

func TestManagedPlacementRecoveryRefusesMismatchAndMissingState(t *testing.T) {
	for _, scenario := range []string{"valid", "expired", "source", "flags", "tags", "node", "name", "stream", "missing state", "missing fence", "missing admission", "wrong admission", "invalid lifetime", "overlong lifetime", "corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			directory, nodeID, name := t.TempDir(), "node", "internal"
			command := managedCommandFixture(t)
			release, err := acquireManagedPlacement(directory, nodeID, command, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			release()
			snapshot := managedSnapshotFromCommand(command)
			snapshot.admission, snapshot.tenantID = nil, ""
			switch scenario {
			case "source":
				snapshot.source = "file:/another.ts"
			case "flags":
				snapshot.alwaysOn = !snapshot.alwaysOn
			case "tags":
				snapshot.tags = append(snapshot.tags, "unexpected")
			case "node":
				nodeID = "another"
			case "name":
				name = "another"
			case "stream":
				snapshot.streamID = "another"
			case "missing state":
				directory = ""
			case "missing fence":
				directory = t.TempDir()
			case "missing admission", "wrong admission", "corrupt", "expired", "invalid lifetime", "overlong lifetime":
				locked, lockErr := lockManagedPlacementFence(directory, name)
				if lockErr != nil {
					t.Fatal(lockErr)
				}
				defer locked.release()
				fence := locked.previous
				if scenario == "expired" {
					command.PlacementAdmission.IssuedAt.Seconds -= 60
					command.PlacementAdmission.ExpiresAt.Seconds -= 60
				}
				if scenario == "wrong admission" {
					command.PlacementAdmission.TargetNodeId = "another"
				}
				if scenario == "invalid lifetime" {
					command.PlacementAdmission.IssuedAt = nil
				}
				if scenario == "overlong lifetime" {
					command.PlacementAdmission.ExpiresAt.Seconds += 60
				}
				fence.Admission, err = proto.Marshal(command.PlacementAdmission)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "missing admission" {
					fence.Admission = nil
				}
				body, marshalErr := json.Marshal(fence)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if scenario == "corrupt" {
					body = []byte("invalid")
				}
				if writeErr := os.WriteFile(locked.path, body, 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
				locked.release()
			}
			got, recoverErr := recoverManagedPlacement(directory, nodeID, name, snapshot)
			want := scenario == "valid" || scenario == "expired"
			if (recoverErr == nil) != want {
				t.Fatalf("incorrect recovery decision: %v", recoverErr)
			}
			if want && (got.tenantID != command.GetTenantId() || !proto.Equal(got.admission, command.GetPlacementAdmission())) {
				t.Fatal("recovery changed admission identity")
			}
		})
	}
}
