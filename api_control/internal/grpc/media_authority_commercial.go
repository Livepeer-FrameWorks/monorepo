package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

type mediaAuthorityCommercialSource interface {
	GetMediaPlacementQuote(context.Context, *pb.CommercialQuoteRequest) (*pb.CommercialQuoteResponse, error)
}

type mediaObjectCommercialSnapshot struct {
	payload    *mediapb.MediaObjectAuthority
	tenant     *mediapb.TenantAuthority
	validUntil time.Time
	revision   string
}

// Quote RPCs run before the object write transaction; the captured tenant is
// compared again under its current-pointer lock before any version is published.
func (s *CommodoreServer) prepareMediaObjectCommercial(ctx context.Context, object *mediapb.MediaObjectAuthority, targets []string) (*mediaObjectCommercialSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tenant, currentTargets, _, tenantUntil, err := s.currentTenantAuthorityContext(ctx, object.GetTenantId())
	if err != nil {
		return nil, err
	}
	if !slices.Equal(sortedUnique(targets), sortedUnique(currentTargets)) {
		return nil, errors.New("commercial authority recipients changed")
	}
	if !sharedauthority.IsPlacementSchema(tenant.GetSchemaVersion()) || tenant.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || tenant.GetBillingDecision() != mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return nil, errors.New("commercial tenant authority is not active schema 2")
	}
	return collectMediaObjectCommercial(ctx, s.authorityCommercialSource, tenant, object, tenantUntil)
}

func collectMediaObjectCommercial(ctx context.Context, source mediaAuthorityCommercialSource, tenant *mediapb.TenantAuthority, object *mediapb.MediaObjectAuthority, tenantUntil time.Time) (*mediaObjectCommercialSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !sharedauthority.IsPlacementSchema(object.GetSchemaVersion()) || object.GetSchemaVersion() != tenant.GetSchemaVersion() || object.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || !tenantUntil.After(time.Now()) {
		return nil, errors.New("commercial object authority is not issuable")
	}
	capturedTenant, capturedObject := proto.CloneOf(tenant), proto.CloneOf(object)
	verbs := []placement.Verb{placement.Serve}
	if capturedObject.GetObjectKind() == mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM {
		verbs = []placement.Verb{placement.Ingest, placement.Serve}
	}
	var requests []*pb.CommercialQuoteRequest
	var quotedVerbs []placement.Verb
	for _, verb := range verbs {
		request, err := sharedauthority.PlacementCommercialRequest(capturedTenant, capturedObject, verb)
		if err != nil {
			return nil, err
		}
		policy, err := sharedauthority.EffectivePlacement(capturedTenant, capturedObject, verb)
		if err != nil {
			return nil, err
		}
		if placement.RequiresCommercialFacts(policy) {
			requests = append(requests, request)
			quotedVerbs = append(quotedVerbs, verb)
		}
	}
	// A policy without commercial predicates still captures and locks its parent,
	// but must not retain obsolete quotes or claim provenance from an uncalled owner.
	if len(requests) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		capturedObject.CommercialQuotes = nil
		return &mediaObjectCommercialSnapshot{payload: capturedObject, tenant: capturedTenant, validUntil: tenantUntil}, nil
	}
	if source == nil {
		return nil, errors.New("commercial authority source is unavailable")
	}
	quotes := make([]*pb.CommercialQuoteResponse, len(requests))
	group, callCtx := errgroup.WithContext(ctx)
	for index, request := range requests {
		group.Go(func() error {
			quoteCtx, cancel := context.WithTimeout(callCtx, 5*time.Second)
			defer cancel()
			response, err := source.GetMediaPlacementQuote(quoteCtx, proto.CloneOf(request))
			if err != nil {
				return fmt.Errorf("read commercial %s quote: %w", quotedVerbs[index], err)
			}
			if err := placement.ValidateCommercialQuoteResponse(request, response, time.Now()); err != nil {
				return err
			}
			quotes[index] = proto.CloneOf(response)
			return quoteCtx.Err()
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	capturedObject.CommercialQuotes = quotes
	until := tenantUntil
	for _, verb := range quotedVerbs {
		quote, err := sharedauthority.PlacementCommercialQuote(capturedTenant, capturedObject, verb, time.Now())
		if err != nil {
			return nil, err
		}
		if quote.GetExpiresAt().AsTime().Before(until) {
			until = quote.GetExpiresAt().AsTime()
		}
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(&mediapb.MediaObjectAuthority{CommercialQuotes: quotes})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(append([]byte("frameworks/placement/object-commercial/v1\x00"), encoded...))
	return &mediaObjectCommercialSnapshot{payload: capturedObject, tenant: capturedTenant, validUntil: until, revision: hex.EncodeToString(digest[:])}, nil
}

func mediaAuthorityRefreshAfter(issuedAt, validUntil time.Time) time.Time {
	interval := validUntil.Sub(issuedAt) / 2
	if interval > mediaAuthorityRefreshInterval {
		interval = mediaAuthorityRefreshInterval
	}
	return issuedAt.Add(interval)
}
