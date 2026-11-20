package capacity_test

import (
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestNodeCapacityWireContract(t *testing.T) {
	for _, test := range []struct {
		message  protoreflect.MessageDescriptor
		name     protoreflect.Name
		number   protoreflect.FieldNumber
		presence bool
	}{
		{(&quartermasterpb.InfrastructureNode{}).ProtoReflect().Descriptor(), "capacity", 28, true},
		{(&ipcpb.ConfigSeed{}).ProtoReflect().Descriptor(), "capacity", 27, true},
		{(&ipcpb.NodeLifecycleUpdate{}).ProtoReflect().Descriptor(), "capacity_revision_applied", 56, true},
		{(&ipcpb.NodeLifecycleUpdate{}).ProtoReflect().Descriptor(), "observed_valid", 57, true},
		{(&ipcpb.NodeLifecycleUpdate{}).ProtoReflect().Descriptor(), "sample_window_start_ms", 58, false},
		{(&ipcpb.NodeLifecycleUpdate{}).ProtoReflect().Descriptor(), "sample_window_end_ms", 59, false},
		{(&ipcpb.NodeLifecycleUpdate{}).ProtoReflect().Descriptor(), "egress_budget_bytes_per_sec", 60, false},
		{(&ipcpb.NodeLifecycleUpdate{}).ProtoReflect().Descriptor(), "budget_source", 61, false},
		{(&ipcpb.NodeLifecycleUpdate{}).ProtoReflect().Descriptor(), "observed_source", 62, false},
	} {
		field := test.message.Fields().ByName(test.name)
		if field == nil {
			t.Errorf("%s.%s is missing", test.message.FullName(), test.name)
			continue
		}
		if field.Number() != test.number || field.HasPresence() != test.presence {
			t.Errorf("%s.%s number=%d presence=%v, want %d/%v", test.message.FullName(), test.name, field.Number(), field.HasPresence(), test.number, test.presence)
		}
	}
	for _, test := range []struct {
		file    protoreflect.FileDescriptor
		service protoreflect.Name
		methods []protoreflect.Name
	}{
		{quartermasterpb.File_quartermaster_proto, "NodeService", []protoreflect.Name{"SetNodeCapacity", "GetNodeCapacity", "ListNodeCapacities"}},
		{foghornpb.File_foghorn_proto, "NodeControlService", []protoreflect.Name{"PushNodeCapacity"}},
		{commodorepb.File_commodore_proto, "NodeManagementService", []protoreflect.Name{"SetNodeCapacity", "GetNodeCapacity"}},
	} {
		service := test.file.Services().ByName(test.service)
		for _, method := range test.methods {
			if service.Methods().ByName(method) == nil {
				t.Errorf("%s.%s is missing", service.FullName(), method)
			}
		}
	}
}
