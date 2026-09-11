package placement

import (
	"strings"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestManagedCommandBindsPayloadIdentityAndLifetime(t *testing.T) {
	for _, scenario := range []string{"valid", "source", "tenant", "stream", "name", "flags", "tags", "node", "expired", "future", "overlong", "missing", "version", "digest", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			command := &ipcpb.ApplyManagedStream{Name: "stream", Source: "file:/input.ts", StreamId: "id", TenantId: "tenant", IngestMode: "mist_native", Tags: []string{"owner"},
				PlacementAdmission: &ipcpb.ManagedStreamAdmission{SchemaVersion: 1, TargetNodeId: "node", TargetClusterId: "cluster", ObjectAuthorityVersion: 3, TenantAuthorityVersion: 4,
					PolicyDigest: strings.Repeat("a", 64), IssuedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(now.Add(time.Second))}}
			digest, err := ManagedCommandDigest(command)
			if err != nil {
				t.Fatal(err)
			}
			command.PlacementAdmission.CommandSha256 = digest
			switch scenario {
			case "source":
				command.Source = "file:/another.ts"
			case "tenant":
				command.TenantId = "another"
			case "stream":
				command.StreamId = "another"
			case "name":
				command.Name = "another"
			case "flags":
				command.AlwaysOn = true
			case "tags":
				command.Tags = append(command.Tags, "another")
			case "node":
				command.PlacementAdmission.TargetNodeId = "another"
			case "expired":
				command.PlacementAdmission.ExpiresAt = timestamppb.New(now)
			case "future":
				command.PlacementAdmission.IssuedAt = timestamppb.New(now.Add(time.Second))
			case "overlong":
				command.PlacementAdmission.ExpiresAt = timestamppb.New(now.Add(time.Minute))
			case "missing":
				command.PlacementAdmission = nil
			case "version":
				command.PlacementAdmission.ObjectAuthorityVersion = 0
			case "digest":
				command.PlacementAdmission.PolicyDigest = strings.Repeat("A", 64)
			case "oversized":
				command.Source = strings.Repeat("x", 1<<20)
			}
			if err := ValidateManagedCommand(command, "node", now); (err == nil) != (scenario == "valid") {
				t.Fatalf("incorrect command validation: %v", err)
			}
		})
	}
}
