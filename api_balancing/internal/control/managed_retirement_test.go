package control

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestManagedRetractionUsesRecoveredExactAdmission(t *testing.T) {
	holdControlConnection(t, "node")
	stream := &mockStream{}
	registry.conns["node"].stream = stream
	r := NewStreamRegistry(nil, "cell", time.Minute)
	old := time.Now().Add(-time.Minute)
	a := &ipcpb.ManagedStreamAdmission{SchemaVersion: 1, TargetNodeId: "node", TargetClusterId: "source", ObjectAuthorityVersion: 3, TenantAuthorityVersion: 4,
		PolicyRevision: 1, ParentRevision: 2, PolicyDigest: strings.Repeat("a", 64), CommandSha256: bytes.Repeat([]byte{1}, 32), IssuedAt: timestamppb.New(old), ExpiresAt: timestamppb.New(old.Add(10 * time.Second))}
	ack := &ipcpb.AppliedManagedStream{Name: "internal", StreamId: "stream", TenantId: "tenant", Source: "file:/input.ts", IngestMode: "mist_native", PlacementAdmission: a}
	r.ManagedHydrateForNode("node", []*ipcpb.AppliedManagedStream{ack})
	r.ManagedAdoptPending("source", []string{"node"})
	snapshot, ok := r.ManagedGetLastSent("source", "node", "stream")
	if !ok || snapshot.tenantID != "tenant" || !proto.Equal(snapshot.admission, a) {
		t.Fatal("reconnect lost exact admission")
	}
	r.ManagedSetVerifiedFromHeartbeat("node", []*ipcpb.AppliedManagedStream{ack})
	if !r.ManagedVerifiedMatches("node", "stream", snapshot) {
		t.Fatal("matching configuration acknowledgement rejected")
	}
	ack.PlacementRetired = true
	r.ManagedSetVerifiedFromHeartbeat("node", []*ipcpb.AppliedManagedStream{ack})
	if r.ManagedVerifiedMatches("node", "stream", snapshot) {
		t.Fatal("retired configuration verified an active Apply")
	}
	restarted := NewStreamRegistry(nil, "cell", time.Minute)
	restarted.ManagedHydrateForNode("node", []*ipcpb.AppliedManagedStream{ack})
	restarted.ManagedAdoptPending("source", []string{"node"})
	recovered, recoveredOK := restarted.ManagedGetLastSent("source", "node", "stream")
	if !recoveredOK || !recovered.retired || !proto.Equal(recovered.admission, a) {
		t.Fatal("retired reconnect lost cleanup identity")
	}
	ack.PlacementRetired = false
	a.ObjectAuthorityVersion++
	if snapshot.admission.GetObjectAuthorityVersion() != 3 {
		t.Fatal("reconnect retained caller-owned admission pointer")
	}
	r.ManagedSetVerifiedFromHeartbeat("node", []*ipcpb.AppliedManagedStream{ack})
	if r.ManagedVerifiedMatches("node", "stream", snapshot) {
		t.Fatal("different admitted version claimed configuration match")
	}
	if err := sendRetractManagedStream(logging.NewLogger(), "node", "stream", snapshot); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 {
		t.Fatal("retraction was not sent")
	}
	retract := stream.sent[0].GetRetractManagedStream()
	if err := placement.ValidateManagedRetraction(retract, "node", time.Now()); err != nil {
		t.Fatal(err)
	}
	binding := retract.GetPlacementRetraction()
	if binding.GetObjectAuthorityVersion() != 3 || binding.GetTenantAuthorityVersion() != 4 || binding.GetTenantId() != "tenant" || binding.GetTargetClusterId() != "source" || !bytes.Equal(binding.GetCommandSha256(), snapshot.admission.GetCommandSha256()) {
		t.Fatalf("retraction changed the observed command: %+v", binding)
	}
	snapshot.tenantID = ""
	if err := sendRetractManagedStream(logging.NewLogger(), "node", "stream", snapshot); err == nil || len(stream.sent) != 1 {
		t.Fatal("missing bound identity fell back to legacy cleanup")
	}
	previousStore := LocalMediaAuthorityStore()
	SetLocalMediaAuthorityStore(nil)
	t.Cleanup(func() { SetLocalMediaAuthorityStore(previousStore) })
	if _, err := sendApplyManagedStreamWithAdmission(context.Background(), logging.NewLogger(), "source", "node",
		&commodorepb.ManagedStreamRow{StreamId: "stream", SourceSpec: "file:/input.ts", IngestMode: "mist_native"},
		&commodorepb.ResolveStreamContextResponse{InternalName: "internal", TenantId: "tenant"}, true); err == nil || len(stream.sent) != 1 {
		t.Fatal("recorded placement authority downgraded when its reader disappeared")
	}
}
