package triggers

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

type ViewerPlacementAuthorityReader interface {
	PlacementForInternalName(context.Context, string, string) (localauthority.PlacementPair, error)
}

type ViewerPlacementSourceReader interface {
	ResolveSourceGeneration(context.Context, balancer.PlacementAuthority) (string, time.Time, error)
}

// ViewerPlacementAdapter derives admission inputs from signed object identity,
// owner source evidence and a trusted Mist client address. Its source reader must
// support the object's kind; an unsupported managed/artifact source fails closed.
type ViewerPlacementAdapter struct {
	Authority ViewerPlacementAuthorityReader
	Source    ViewerPlacementSourceReader
	Gate      *federation.PlacementPolicyGate
	GeoIP     *geoip.Reader
	GeoCache  *cache.Cache
}

func (adapter *ViewerPlacementAdapter) AdmitViewer(ctx context.Context, connection ViewerPlacementConnection) (federation.PlacementAdmissionDecision, error) {
	if adapter == nil || adapter.Authority == nil || adapter.Source == nil || adapter.Gate == nil {
		return federation.PlacementAdmissionDecision{}, errors.New("viewer placement runtime is unavailable")
	}
	protocol, err := connection.protocol()
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	address, err := netip.ParseAddr(connection.ClientAddress)
	if err != nil || address.Zone() != "" {
		return federation.PlacementAdmissionDecision{}, errors.New("viewer client address is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return federation.PlacementAdmissionDecision{}, ctxErr
	}
	readCtx, stopRead := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	pair, err := adapter.Authority.PlacementForInternalName(readCtx, connection.TenantID, connection.InternalName)
	if err == nil {
		err = readCtx.Err()
	}
	stopRead()
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Serve, time.Now())
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	if authority.TenantID != connection.TenantID || authority.ObjectID == "" || authority.InternalName != connection.InternalName {
		return federation.PlacementAdmissionDecision{}, errors.New("viewer placement pair identity differs")
	}
	generation, sourceUntil, err := adapter.Source.ResolveSourceGeneration(ctx, authority)
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	if generation == "" || !time.Now().Before(sourceUntil) || sourceUntil.After(authority.ExpiresAt) {
		return federation.PlacementAdmissionDecision{}, errors.New("viewer source generation is unavailable or expired")
	}
	input := federation.PlacementAdmissionInput{TenantID: connection.TenantID, ObjectID: authority.ObjectID, InternalName: connection.InternalName,
		ClusterID: connection.ClusterID, NodeID: connection.NodeID, Protocol: protocol, Verb: placement.Serve, SourceGeneration: generation,
		TenantAuthorityVersion: authority.TenantAuthorityVersion, ObjectAuthorityVersion: authority.ObjectAuthorityVersion}
	if adapter.GeoIP != nil {
		if found := geoip.LookupCached(ctx, adapter.GeoIP, adapter.GeoCache, address.Unmap().String()); found != nil {
			input.Location = &placement.Coordinates{Latitude: found.Latitude, Longitude: found.Longitude}
		}
	}
	decision, err := adapter.Gate.Admit(ctx, input)
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	// Global observation can span a publisher replacement or withdrawal. Recheck
	// the owner-derived generation without repeating the placement census.
	currentGeneration, currentSourceUntil, err := adapter.Source.ResolveSourceGeneration(ctx, authority)
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	if currentGeneration != generation || !time.Now().Before(currentSourceUntil) || currentSourceUntil.After(authority.ExpiresAt) {
		return federation.PlacementAdmissionDecision{}, errors.New("viewer source changed during placement admission")
	}
	if currentSourceUntil.Before(sourceUntil) {
		sourceUntil = currentSourceUntil
	}
	if sourceUntil.Before(decision.ExpiresAt) {
		decision.ExpiresAt = sourceUntil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return federation.PlacementAdmissionDecision{}, ctxErr
	}
	if !time.Now().Before(decision.ExpiresAt) {
		return federation.PlacementAdmissionDecision{}, errors.New("viewer placement expired during revalidation")
	}
	return decision, nil
}
