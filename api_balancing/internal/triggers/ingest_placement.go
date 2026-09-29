package triggers

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

// IngestPlacementConnection is the authenticated publisher connection PUSH_REWRITE
// admits: Mist's own connector name, the claimed cluster/node and the publisher
// address. The push URL is deliberately absent; the publisher controls it.
type IngestPlacementConnection struct {
	TenantID, InternalName, ClusterID, NodeID string
	Connector, PublisherAddress               string
}

type IngestPlacementAdmission func(context.Context, IngestPlacementConnection) (federation.PlacementAdmissionDecision, error)

// SetIngestPlacementAdmission requires final publisher admission before a claimed
// push is materialized. Clearing an installed adapter cannot restore URL-derived
// protocol admission.
func (p *Processor) SetIngestPlacementAdmission(admission IngestPlacementAdmission) {
	p.ingestPlacementRequired = true
	p.ingestPlacementAdmission = admission
}

// ConfigureLiveIngestPlacementAdmission installs final publisher admission from the
// same public runtime that resolves front-door ingest, so the policy gate, ownership
// fence and router that recommended a node also admit the publisher that arrived.
func (p *Processor) ConfigureLiveIngestPlacementAdmission(runtime *federation.LivePublicPlacementRuntime, geo *geoip.Reader, geoCache *cache.Cache) error {
	if p == nil || runtime == nil || runtime.Gate == nil || p.ingestPlacementAdmission != nil || p.ingestPlacementRequired {
		return errors.New("ingest placement admission dependencies are unavailable or already configured")
	}
	if runtime.Gate.CellID == "" || runtime.Gate.Authority == nil || runtime.Gate.IngestFence == nil || runtime.Gate.Router.Observe == nil {
		return errors.New("ingest placement admission requires a cell-bound policy gate with ownership fence")
	}
	authority, ok := runtime.Gate.Authority.(ViewerPlacementAuthorityReader)
	if !ok || authority == nil {
		return errors.New("ingest placement admission requires tenant-scoped authority lookup")
	}
	adapter := &IngestPlacementAdapter{Authority: authority, Gate: runtime.Gate, GeoIP: geo, GeoCache: geoCache}
	p.SetIngestPlacementAdmission(adapter.AdmitPublisher)
	return nil
}

// checkIngestPlacement runs after the claim so a policy denial releases it. The
// protocol comes from Mist's observed connector; a missing attestation denies.
func (p *Processor) checkIngestPlacement(ctx context.Context, tenantID, internalName, clusterID, nodeID, connector, publisherAddress string) (*federation.PlacementAdmissionDecision, error) {
	if p.ingestPlacementAdmission == nil {
		return nil, errors.New("ingest placement admission is unavailable")
	}
	for _, identity := range []string{tenantID, internalName, clusterID, nodeID} {
		if identity == "" || identity != strings.TrimSpace(identity) {
			return nil, errors.New("ingest placement identity is unavailable")
		}
	}
	protocol, err := mist.IngestProtocol(connector)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	decision, err := p.ingestPlacementAdmission(ctx, IngestPlacementConnection{TenantID: tenantID, InternalName: internalName, ClusterID: clusterID, NodeID: nodeID,
		Connector: connector, PublisherAddress: publisherAddress})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ingestPlacementDecisionBinding(decision, tenantID, internalName, clusterID, nodeID, protocol, time.Now()); err != nil {
		return nil, err
	}
	return &decision, nil
}

// ingestPlacementDecisionBinding checks that an admission decision is for this
// publisher and still valid at returned, the moment admission handed it back.
// Admission anchors the decision's lifetime to its own clock reading, which
// can be later than any instant taken before the call (it may refresh a stale
// observation first), so the lifetime bound is measured from returned. Each
// refusal names the binding that failed.
func ingestPlacementDecisionBinding(decision federation.PlacementAdmissionDecision, tenantID, internalName, clusterID, nodeID, protocol string, returned time.Time) error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("ingest placement decision is not bound to this publisher: "+format, args...)
	}
	switch {
	case decision.TenantID != tenantID || decision.InternalName != internalName:
		return refuse("decision is for %s/%s", decision.TenantID, decision.InternalName)
	case decision.ClusterID != clusterID || decision.NodeID != nodeID:
		return refuse("decision places on %s/%s, publisher arrived on %s/%s", decision.ClusterID, decision.NodeID, clusterID, nodeID)
	case decision.Verb != placement.Ingest || decision.ObjectID == "" || decision.Protocol != protocol:
		return refuse("decision verb %q object %q protocol %q, publisher protocol %q", decision.Verb, decision.ObjectID, decision.Protocol, protocol)
	case decision.PolicyRevision > math.MaxInt64 || decision.ParentRevision > math.MaxInt64:
		return refuse("policy revision %d parent %d out of range", decision.PolicyRevision, decision.ParentRevision)
	}
	if digest, err := hex.DecodeString(decision.PolicyDigest); err != nil || len(digest) != 32 || hex.EncodeToString(digest) != decision.PolicyDigest {
		return refuse("policy digest %q is not a canonical sha256", decision.PolicyDigest)
	}
	if decision.TenantAuthorityVersion <= 0 || decision.ObjectAuthorityVersion <= 0 {
		return refuse("authority versions tenant=%d object=%d are unset", decision.TenantAuthorityVersion, decision.ObjectAuthorityVersion)
	}
	if !returned.Before(decision.ExpiresAt) {
		return fmt.Errorf("ingest placement decision expired at %s, admission returned at %s", decision.ExpiresAt.UTC().Format(time.RFC3339Nano), returned.UTC().Format(time.RFC3339Nano))
	}
	if decision.ExpiresAt.After(returned.Add(placement.PreparationLifetime)) {
		return refuse("expiry %s is beyond the %s preparation lifetime from %s", decision.ExpiresAt.UTC().Format(time.RFC3339Nano), placement.PreparationLifetime, returned.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

// IngestPlacementAdapter derives the publisher's policy inputs from signed object
// identity, Mist's connector and the publisher address. It never reads the push URL.
type IngestPlacementAdapter struct {
	Authority ViewerPlacementAuthorityReader
	Gate      *federation.PlacementPolicyGate
	GeoIP     *geoip.Reader
	GeoCache  *cache.Cache
}

func (adapter *IngestPlacementAdapter) AdmitPublisher(ctx context.Context, connection IngestPlacementConnection) (federation.PlacementAdmissionDecision, error) {
	if adapter == nil || adapter.Authority == nil || adapter.Gate == nil {
		return federation.PlacementAdmissionDecision{}, errors.New("ingest placement runtime is unavailable")
	}
	protocol, err := mist.IngestProtocol(connection.Connector)
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	readCtx, stopRead := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	pair, err := adapter.Authority.PlacementForInternalName(readCtx, connection.TenantID, connection.InternalName)
	if err == nil {
		err = readCtx.Err()
	}
	stopRead()
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Ingest, time.Now())
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	if authority.TenantID != connection.TenantID || authority.ObjectID == "" || authority.InternalName != connection.InternalName || authority.IngestMode != "push" {
		return federation.PlacementAdmissionDecision{}, errors.New("ingest placement pair identity differs or is not a push stream")
	}
	// A publisher brings no owner-resolved source evidence, so the admission is
	// not bound to the versions of this lookup: the authority advance that the
	// publisher's own ownership claim causes must not refuse it.
	input := federation.PlacementAdmissionInput{TenantID: connection.TenantID, ObjectID: authority.ObjectID, InternalName: connection.InternalName,
		ClusterID: connection.ClusterID, NodeID: connection.NodeID, Protocol: protocol, Verb: placement.Ingest}
	if address, parseErr := netip.ParseAddr(connection.PublisherAddress); parseErr == nil && adapter.GeoIP != nil {
		if found := geoip.LookupCached(ctx, adapter.GeoIP, adapter.GeoCache, address.Unmap().String()); found != nil {
			input.Location = &placement.Coordinates{Latitude: found.Latitude, Longitude: found.Longitude}
		}
	}
	decision, err := adapter.Gate.Admit(ctx, input)
	if err != nil {
		return federation.PlacementAdmissionDecision{}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return federation.PlacementAdmissionDecision{}, ctxErr
	}
	return decision, nil
}
