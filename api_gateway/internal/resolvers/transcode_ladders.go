package resolvers

import (
	"context"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (r *Resolver) DoTranscodeLadderPolicy(ctx context.Context) (*model.TranscodeLadderPolicy, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demoTranscodeLadderPolicy(), nil
	}
	policy, err := r.Clients.Commodore.GetTranscodeLadderPolicy(ctx, &commodorepb.GetTranscodeLadderPolicyRequest{})
	if err != nil {
		return nil, fmt.Errorf("load transcode ladder policy: %w", err)
	}
	return transcodeLadderPolicyFromProto(policy), nil
}

func (r *Resolver) DoSetTranscodeLadderPolicy(ctx context.Context, input model.SetTranscodeLadderPolicyInput) (model.SetTranscodeLadderPolicyResult, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	choice := ""
	if input.Ladder != nil {
		choice = *input.Ladder
	}
	if middleware.IsDemoMode(ctx) {
		policy := demoTranscodeLadderPolicy()
		if choice != "" {
			allowed := false
			for _, ladder := range policy.Ladders {
				allowed = allowed || ladder.Name == choice
			}
			if !allowed {
				return &model.ValidationError{Message: "Choose standard, qhd, or uhd", Field: strPtr("ladder")}, nil
			}
			policy.TenantLadder = &choice
			policy.EffectiveLadder = choice
		}
		return policy, nil
	}
	policy, err := r.Clients.Commodore.SetTenantTranscodeLadder(ctx, &commodorepb.SetTenantTranscodeLadderRequest{TranscodeLadder: choice})
	if err != nil {
		if status.Code(err) == codes.InvalidArgument {
			return &model.ValidationError{Message: status.Convert(err).Message(), Field: strPtr("ladder")}, nil
		}
		return nil, fmt.Errorf("set transcode ladder policy: %w", err)
	}
	return transcodeLadderPolicyFromProto(policy), nil
}

func transcodeLadderPolicyFromProto(policy *commodorepb.TranscodeLadderPolicy) *model.TranscodeLadderPolicy {
	out := &model.TranscodeLadderPolicy{Ladders: []*model.TranscodeLadder{}, PlanDefault: policy.GetTierDefault(), EffectiveLadder: policy.GetEffectiveLadder()}
	if policy.GetTenantLadder() != "" {
		out.TenantLadder = strPtr(policy.GetTenantLadder())
	}
	for _, ladder := range policy.GetLadders() {
		out.Ladders = append(out.Ladders, &model.TranscodeLadder{Name: ladder.GetName(), DisplayName: ladder.GetDisplayName(), Description: ladder.GetDescription(), RenditionNames: append([]string{}, ladder.GetRenditionNames()...)})
	}
	return out
}

func demoTranscodeLadderPolicy() *model.TranscodeLadderPolicy {
	standard := []string{"360p", "480p", "720p", "1080p"}
	return &model.TranscodeLadderPolicy{PlanDefault: "standard", EffectiveLadder: "standard", Ladders: []*model.TranscodeLadder{
		{Name: "standard", DisplayName: "Standard", Description: "Up to 1080p", RenditionNames: standard},
		{Name: "qhd", DisplayName: "1440p", Description: "Up to 1440p", RenditionNames: append(append([]string{}, standard...), "1440p")},
		{Name: "uhd", DisplayName: "4K", Description: "Up to 4K", RenditionNames: append(append([]string{}, standard...), "1440p", "2160p")},
	}}
}
