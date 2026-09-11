package federation

import (
	"context"
	"errors"
	"reflect"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

type ViewerPlacementSourceReader interface {
	ResolveSourceGeneration(context.Context, balancer.PlacementAuthority) (string, time.Time, error)
}

// ViewerPlacementNameReader resolves the signed pair for stored media, whose
// authority identifier is a control-plane row id this cell never receives.
type ViewerPlacementNameReader interface {
	PlacementForInternalName(context.Context, string, string) (localauthority.PlacementPair, error)
}

type ViewerPlacementResolver struct {
	Authority PlacementAuthorityReader
	// StoredMedia resolves artifacts by internal name. Without it this resolver
	// serves live streams only, and stored-media requests fail closed.
	StoredMedia ViewerPlacementNameReader
	Source      ViewerPlacementSourceReader
	Router      balancer.PlacementRouter
}

var (
	_ control.ViewerPlacementPreparer  = (*ViewerPlacementResolver)(nil)
	_ control.ViewerPlacementPermitter = (*ViewerPlacementResolver)(nil)
)

// viewerPlacementSubject is one consistent read of signed authority plus the
// owner-resolved generation it was read with. Both front-door entry points bind
// their answer to this pair so policy and source can never come from different
// authority versions.
type viewerPlacementSubject struct {
	authority   balancer.PlacementAuthority
	objectID    string
	generation  string
	sourceUntil time.Time
	read        func() (balancer.PlacementAuthority, error)
}

func (resolver *ViewerPlacementResolver) subject(ctx context.Context, request control.ViewerPlacementRequest) (viewerPlacementSubject, error) {
	if resolver == nil || resolver.Authority == nil || resolver.Source == nil {
		return viewerPlacementSubject{}, errors.New("viewer placement resolver is unavailable")
	}
	for _, id := range []string{request.TenantID, request.InternalName, request.PlaybackID, request.Protocol} {
		if !validPullIdentity(id) {
			return viewerPlacementSubject{}, errors.New("viewer placement identity is incomplete")
		}
	}
	stored, err := control.ViewerPlacementStoredMedia(request)
	if err != nil {
		return viewerPlacementSubject{}, err
	}
	var objectID string
	if !stored {
		if objectID, err = control.ViewerPlacementLiveObjectID(request); err != nil {
			return viewerPlacementSubject{}, err
		}
		if !validPullIdentity(objectID) {
			return viewerPlacementSubject{}, errors.New("viewer placement identity is incomplete")
		}
	} else if resolver.StoredMedia == nil || !validPullIdentity(request.ArtifactHash) {
		return viewerPlacementSubject{}, errors.New("stored media placement is unavailable")
	}
	read := func() (balancer.PlacementAuthority, error) {
		readCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		var pair localauthority.PlacementPair
		var readErr error
		if stored {
			pair, readErr = resolver.StoredMedia.PlacementForInternalName(readCtx, request.TenantID, request.InternalName)
		} else {
			pair, readErr = resolver.Authority.Placement(readCtx, request.TenantID, objectID, request.InternalName)
		}
		if readErr != nil {
			return balancer.PlacementAuthority{}, readErr
		}
		if readErr = readCtx.Err(); readErr != nil {
			return balancer.PlacementAuthority{}, readErr
		}
		if pair.Object.Authority.GetPlaybackId() != request.PlaybackID {
			return balancer.PlacementAuthority{}, errors.New("viewer playback identity differs from signed object")
		}
		// Stored media is named by hash, so the resolved object must be an
		// artifact carrying that exact hash before its policy may be used.
		if stored && (pair.Object.Authority.GetObjectKind() != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT ||
			pair.Object.Authority.GetArtifact().GetArtifactHash() != request.ArtifactHash) {
			return balancer.PlacementAuthority{}, errors.New("stored media identity differs from signed object")
		}
		return balancer.CompilePlacementAuthority(pair, placement.Serve, time.Now())
	}
	authority, err := read()
	if err != nil {
		return viewerPlacementSubject{}, err
	}
	// Kind support is the source reader's decision, not a string check here: an
	// object whose kind this cell cannot resolve fails closed below.
	if authority.TenantID != request.TenantID || authority.InternalName != request.InternalName || (!stored && authority.ObjectID != objectID) {
		return viewerPlacementSubject{}, errors.New("viewer placement requires matching signed authority")
	}
	objectID = authority.ObjectID
	generation, sourceUntil, err := resolver.Source.ResolveSourceGeneration(ctx, authority)
	if err != nil {
		return viewerPlacementSubject{}, err
	}
	if generation == "" || !time.Now().Before(sourceUntil) || sourceUntil.After(authority.ExpiresAt) {
		return viewerPlacementSubject{}, errors.New("viewer source generation is unavailable")
	}
	return viewerPlacementSubject{authority: authority, objectID: objectID, generation: generation, sourceUntil: sourceUntil, read: read}, nil
}

func (resolver *ViewerPlacementResolver) routeRequest(subject viewerPlacementSubject, request control.ViewerPlacementRequest) balancer.PlacementRouteRequest {
	authority := subject.authority
	return balancer.PlacementRouteRequest{TenantID: authority.TenantID, ObjectID: authority.ObjectID, InternalName: authority.InternalName,
		Verb: placement.Serve, Protocol: request.Protocol, SourceGeneration: subject.generation, Policy: authority.Policy, PolicyDigest: authority.PolicyDigest,
		PolicyRevision: authority.PolicyRevision, ParentRevision: authority.ParentRevision, Cells: authority.Cells, Location: request.Location,
		TenantAuthorityVersion: authority.TenantAuthorityVersion, ObjectAuthorityVersion: authority.ObjectAuthorityVersion}
}

func (resolver *ViewerPlacementResolver) PrepareViewer(ctx context.Context, request control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	subject, err := resolver.subject(ctx, request)
	if err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	result, err := resolver.Router.Route(ctx, resolver.routeRequest(subject, request))
	if err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	current, err := subject.read()
	if err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	if !reflect.DeepEqual(subject.authority, current) {
		return balancer.PlacementPreparationResult{}, errors.New("viewer authority changed during routing")
	}
	currentGeneration, currentUntil, err := resolver.Source.ResolveSourceGeneration(ctx, current)
	if err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	if currentGeneration != subject.generation || !time.Now().Before(subject.sourceUntil) || !time.Now().Before(currentUntil) ||
		currentUntil.After(current.ExpiresAt) || !time.Now().Before(result.Preparation.ExpiresAt) {
		return balancer.PlacementPreparationResult{}, errors.New("viewer source changed during routing")
	}
	if err = ctx.Err(); err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	// Both source observations bound the acknowledgement, even when the
	// destination's own preparation has a longer remaining lifetime.
	for _, until := range []time.Time{subject.sourceUntil, currentUntil} {
		if until.Before(result.Preparation.ExpiresAt) {
			result.Preparation.ExpiresAt = until
		}
	}
	return result.Preparation, nil
}

// PermitViewer evaluates serve policy and returns the permitted destinations
// without preparing one. Stored media ranks its own candidates by where the
// bytes already are, so it needs the policy answer rather than a chosen node;
// final admission still re-evaluates the connection that actually arrives.
func (resolver *ViewerPlacementResolver) PermitViewer(ctx context.Context, request control.ViewerPlacementRequest) (control.ViewerPlacementPermission, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	subject, err := resolver.subject(ctx, request)
	if err != nil {
		return control.ViewerPlacementPermission{}, err
	}
	router := resolver.Router
	evaluated, err := router.Evaluate(ctx, resolver.routeRequest(subject, request))
	if err != nil && !errors.Is(err, balancer.ErrPlacementUnavailable) {
		return control.ViewerPlacementPermission{}, err
	}
	if err = ctx.Err(); err != nil {
		return control.ViewerPlacementPermission{}, err
	}
	permission := control.ViewerPlacementPermission{ObjectID: subject.objectID, SourceGeneration: subject.generation, ExpiresAt: subject.sourceUntil}
	if evaluated.ExpiresAt.Before(permission.ExpiresAt) {
		permission.ExpiresAt = evaluated.ExpiresAt
	}
	for _, choice := range evaluated.Decision.Choices {
		permission.Destinations = append(permission.Destinations, control.ViewerPlacementDestination{ClusterID: choice.ClusterID, NodeID: choice.NodeID})
	}
	if len(permission.Destinations) == 0 || !time.Now().Before(permission.ExpiresAt) {
		return control.ViewerPlacementPermission{}, balancer.ErrPlacementUnavailable
	}
	return permission, nil
}
