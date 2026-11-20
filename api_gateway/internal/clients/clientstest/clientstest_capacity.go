package clientstest

import (
	"context"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func (f *FakeCommodore) SetNodeCapacity(ctx context.Context, req *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	f.incCalls()
	if f.SetNodeCapacityFn == nil {
		panic("FakeCommodore.SetNodeCapacity not stubbed")
	}
	return f.SetNodeCapacityFn(ctx, req)
}
func (f *FakeCommodore) GetNodeCapacity(ctx context.Context, req *quartermasterpb.GetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	f.incCalls()
	if f.GetNodeCapacityFn == nil {
		panic("FakeCommodore.GetNodeCapacity not stubbed")
	}
	return f.GetNodeCapacityFn(ctx, req)
}
func (f *FakeQuartermaster) ListNodeCapacities(ctx context.Context, req *quartermasterpb.ListNodeCapacitiesRequest) (*quartermasterpb.ListNodeCapacitiesResponse, error) {
	f.incCalls()
	if f.ListNodeCapacitiesFn == nil {
		panic("FakeQuartermaster.ListNodeCapacities not stubbed")
	}
	return f.ListNodeCapacitiesFn(ctx, req)
}
