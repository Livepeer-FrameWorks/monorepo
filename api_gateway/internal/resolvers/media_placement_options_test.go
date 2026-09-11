package resolvers

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPlacementAPIOptionsErrorsAreReadSpecific(t *testing.T) {
	for _, code := range []codes.Code{codes.Aborted, codes.InvalidArgument, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			fake := &clientstest.FakeCommodore{GetMediaPlacementOptionsFn: func(ctx context.Context, _ *placementpb.GetOptionsRequest) (*placementpb.Options, error) {
				deadline, bounded := ctx.Deadline()
				if !bounded || time.Until(deadline) > 4*time.Second {
					t.Fatal("options RPC has no bounded deadline")
				}
				return nil, status.Error(code, "private infrastructure diagnostic")
			}}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
			result, err := r.DoMediaPlacementOptions(placementAPITestContext(), model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindTenant}, nil, nil, nil)
			out, ok := result.(*model.MediaPlacementError)
			if err != nil || !ok || strings.Contains(out.Message, "private") || strings.Contains(out.Message, "write") || !strings.Contains(strings.ToLower(out.Message), "search") {
				t.Fatalf("read error leaked diagnostics or suggested write recovery: %+v %v", result, err)
			}
		})
	}
}

func TestPlacementAPIOptionsRejectInvalidFiltersBeforeRPC(t *testing.T) {
	for _, scenario := range []string{"zero", "negative", "too many", "wide integer", "cursor", "query", "kind", "class"} {
		t.Run(scenario, func(t *testing.T) {
			first, after := 50, ""
			filter := &model.MediaPlacementOptionsFilter{}
			switch scenario {
			case "zero":
				first = 0
			case "negative":
				first = -1
			case "too many":
				first = 101
			case "wide integer":
				first = 1<<32 + 50
			case "cursor":
				after = strings.Repeat("x", 2049)
			case "query":
				filter.Query = strPtr(strings.Repeat("x", 129))
			case "kind":
				kind := model.MediaPlacementOptionKind("UNKNOWN")
				filter.Kind = &kind
			case "class":
				filter.Classes = []model.MediaPlacementClass{"UNKNOWN"}
			}
			fake := &clientstest.FakeCommodore{}
			r := &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}
			result, err := r.DoMediaPlacementOptions(placementAPITestContext(), model.MediaPlacementScopeInput{Kind: model.MediaPlacementScopeKindTenant}, filter, &after, &first)
			out, ok := result.(*model.MediaPlacementError)
			if err != nil || !ok || out.Code != model.MediaPlacementErrorCodeInvalidInput || fake.Calls != 0 {
				t.Fatalf("invalid query reached the backend: %+v %v", result, err)
			}
		})
	}
}
