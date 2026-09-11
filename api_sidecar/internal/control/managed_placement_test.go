package control

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func managedCommandFixture(t *testing.T) *ipcpb.ApplyManagedStream {
	t.Helper()
	now := time.Now()
	command := &ipcpb.ApplyManagedStream{Name: "internal", Source: "file:/input.ts", TenantId: "tenant", StreamId: "stream", IngestMode: "mist_native", PlacementAdmission: &ipcpb.ManagedStreamAdmission{
		SchemaVersion: 1, TargetNodeId: "node", TargetClusterId: "source", ObjectAuthorityVersion: 3, TenantAuthorityVersion: 4, PolicyRevision: 1, ParentRevision: 1,
		PolicyDigest: strings.Repeat("a", 64), IssuedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(20 * time.Second)),
	}}
	bindManagedCommand(t, command)
	return command
}

func bindManagedCommand(t *testing.T, command *ipcpb.ApplyManagedStream) {
	t.Helper()
	digest, err := placement.ManagedCommandDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PlacementAdmission.CommandSha256 = digest
}

func TestManagedPlacementFencePersistsAndRejectsRegression(t *testing.T) {
	for _, scenario := range []string{"retry", "new version", "object rollback", "tenant rollback", "policy rollback", "same version change", "tenant", "cluster", "downgrade", "expired", "corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			command := managedCommandFixture(t)
			release, err := acquireManagedPlacement(directory, "node", command, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if overlapping, overlapErr := acquireManagedPlacement(directory, "node", command, time.Now()); overlapErr == nil {
				overlapping()
				t.Fatal("overlapping command acquired dispatch lock")
			}
			release()
			files, err := filepath.Glob(filepath.Join(directory, "managed-placement", "*.json"))
			if err != nil || len(files) != 1 {
				t.Fatalf("missing durable fence: %v %v", files, err)
			}
			before, err := os.ReadFile(files[0])
			if err != nil || bytes.Contains(before, []byte(command.Source)) {
				t.Fatal("fence unavailable or contains source secret")
			}
			next := proto.CloneOf(command)
			switch scenario {
			case "new version":
				next.PlacementAdmission.ObjectAuthorityVersion++
				next.Source = "file:/next.ts"
			case "object rollback":
				next.PlacementAdmission.ObjectAuthorityVersion--
			case "tenant rollback":
				next.PlacementAdmission.TenantAuthorityVersion--
			case "policy rollback":
				next.PlacementAdmission.PolicyRevision--
			case "same version change":
				next.Source = "file:/next.ts"
			case "tenant":
				next.TenantId = "another"
			case "cluster":
				next.PlacementAdmission.TargetClusterId = "another"
			case "downgrade":
				next.PlacementAdmission = nil
			case "expired":
				next.PlacementAdmission.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
			case "corrupt":
				if writeErr := os.WriteFile(files[0], []byte("broken"), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
				before = []byte("broken")
			}
			if next.PlacementAdmission != nil {
				bindManagedCommand(t, next)
			}
			unlock, err := acquireManagedPlacement(directory, "node", next, time.Now())
			want := scenario == "retry" || scenario == "new version"
			if (err == nil) != want {
				t.Fatalf("incorrect persisted fence decision: %v", err)
			}
			if unlock != nil {
				unlock()
			}
			if !want {
				after, readErr := os.ReadFile(files[0])
				if readErr != nil || !bytes.Equal(before, after) {
					t.Fatal("refused command mutated authority fence")
				}
			}
		})
	}
}

func TestManagedPlacementHandlerRejectsExpiredAndChangedCommands(t *testing.T) {
	for _, scenario := range []string{"valid", "expired", "wrong node", "changed source", "missing state", "stream tag", "ingest tag"} {
		t.Run(scenario, func(t *testing.T) {
			mock := withMockMistAndCleanState(t)
			currentConfig.NodeID, currentConfig.StateDir = "node", t.TempDir()
			command := managedCommandFixture(t)
			switch scenario {
			case "expired":
				command.PlacementAdmission.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
			case "wrong node":
				command.PlacementAdmission.TargetNodeId = "another"
			case "changed source":
				command.Source = "file:/another.ts"
			case "missing state":
				currentConfig.StateDir = ""
			case "stream tag":
				command.Tags = []string{managedStreamIDTagPrefix + "another"}
				bindManagedCommand(t, command)
			case "ingest tag":
				command.Tags = []string{"ingest:pull"}
				bindManagedCommand(t, command)
			}
			handleApplyManagedStream(logging.NewLogger(), command)
			want := 0
			if scenario == "valid" {
				want = 1
			}
			if len(mock.callsContainingKey("addstream")) != want || len(mock.callsContainingKey("save")) != want {
				t.Fatal("handler violated placement admission")
			}
			if scenario == "valid" {
				command.PlacementAdmission = nil
				command.Source = "file:/unbound.ts"
				handleApplyManagedStream(logging.NewLogger(), command)
				if len(mock.callsContainingKey("addstream")) != 1 {
					t.Fatal("handler downgraded to unbound authority")
				}
			}
		})
	}
}
