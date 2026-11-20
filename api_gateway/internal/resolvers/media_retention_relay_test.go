package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestStreamRetentionOverridesNormalizesRelayID(t *testing.T) {
	const streamID = "11111111-2222-4333-8444-555555555555"
	for _, tc := range []struct {
		name, input string
		invalid     bool
	}{
		{"raw stream", streamID, false},
		{"stream relay ID", globalid.Encode(globalid.TypeStream, streamID), false},
		{"wrong node type", globalid.Encode(globalid.TypeClip, streamID), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *commodorepb.SetStreamRetentionOverridesRequest
			client := &clientstest.FakeCommodore{SetStreamRetentionOverridesFn: func(_ context.Context, request *commodorepb.SetStreamRetentionOverridesRequest) (*commodorepb.SetStreamRetentionOverridesResponse, error) {
				got = request
				return &commodorepb.SetStreamRetentionOverridesResponse{StreamId: request.GetStreamId()}, nil
			}}
			days := 14
			result, err := commoW2(client).DoSetStreamRetentionOverrides(clientstest.AuthedCtx("t1"), model.SetStreamRetentionOverridesInput{
				StreamID: tc.input, DvrRetentionDaysOverride: &days,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.invalid {
				validation, ok := result.(*model.ValidationError)
				if !ok || validation.Field == nil || *validation.Field != "streamId" || got != nil {
					t.Fatalf("wrong relay type reached retention backend: result=%v request=%v", result, got)
				}
				return
			}
			if got == nil || got.GetStreamId() != streamID {
				t.Fatalf("retention backend received a relay ID instead of stream UUID: %v", got)
			}
		})
	}
}
