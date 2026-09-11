package federation

import (
	"context"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PlacementMediaRuntime dispatches physical reconciliation by verb inside the
// common policy boundary. Missing handlers never fall through to another verb.
type PlacementMediaRuntime struct {
	Ingest PlacementPreparationRuntime
	Serve  PlacementPreparationRuntime
}

func (runtime *PlacementMediaRuntime) handler(req *placementpb.PreparePlacementRequest) (PlacementPreparationRuntime, error) {
	if runtime != nil {
		switch req.GetQuery().GetVerb() {
		case placementpb.Verb_VERB_INGEST:
			if runtime.Ingest != nil {
				return runtime.Ingest, nil
			}
		case placementpb.Verb_VERB_SERVE:
			if runtime.Serve != nil {
				return runtime.Serve, nil
			}
		}
	}
	return nil, status.Error(codes.Unavailable, "placement media handler is unavailable")
}

func (runtime *PlacementMediaRuntime) Revalidate(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	handler, err := runtime.handler(req)
	if err != nil {
		return err
	}
	return handler.Revalidate(ctx, req, receipt)
}

func (runtime *PlacementMediaRuntime) Reconcile(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
	handler, err := runtime.handler(req)
	if err != nil {
		return nil, err
	}
	return handler.Reconcile(ctx, req, receipt, bind)
}
