package triggers

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// errNoPlaybackGrant: this cell holds no ready signed authority the grant
// could be drawn from, so the edge keeps asking Foghorn per request.
var errNoPlaybackGrant = errors.New("no ready signed media authority for this stream in this cell")

// playbackGrantPushTimeout bounds the grant pushes one authority apply starts.
const playbackGrantPushTimeout = 10 * time.Second

// PlaybackGrantDelivery reaches the edges holding grants. The default is the
// control package's connection-scoped delivery.
type PlaybackGrantDelivery interface {
	Offer(nodeID string, grant *ipcpb.PlaybackGrant) error
	Holders(internalName string) []string
	// HeldStreams returns the Mist names of granted streams with the bare
	// internal name, or of the tenant when internalName is empty.
	HeldStreams(internalName, tenantID string) []string
}

type controlPlaybackGrantDelivery struct{}

func (controlPlaybackGrantDelivery) Offer(nodeID string, grant *ipcpb.PlaybackGrant) error {
	return control.OfferPlaybackGrant(nodeID, grant)
}

func (controlPlaybackGrantDelivery) Holders(internalName string) []string {
	return control.PlaybackGrantHolders(internalName)
}

func (controlPlaybackGrantDelivery) HeldStreams(internalName, tenantID string) []string {
	return control.PlaybackGrantStreams(internalName, tenantID)
}

// SetPlaybackGrantDelivery replaces how grants reach edges.
func (p *Processor) SetPlaybackGrantDelivery(delivery PlaybackGrantDelivery) {
	p.playbackGrants = delivery
}

func (p *Processor) playbackGrantDelivery() PlaybackGrantDelivery {
	if p.playbackGrants != nil {
		return p.playbackGrants
	}
	return controlPlaybackGrantDelivery{}
}

// playbackGrantFromLocal composes an edge grant from the ready object and
// tenant authority a playback decision was just made on. Its validity is the
// authority's own hard validity, so the edge never answers past the point
// where this cell would stop answering from the same authority.
func playbackGrantFromLocal(local localPlaybackAuthority, requested string) *ipcpb.PlaybackGrant {
	object := local.object.Authority
	grant := &ipcpb.PlaybackGrant{
		InternalName:           local.target.InternalName,
		TenantId:               object.GetTenantId(),
		Policy:                 playbackGrantPolicy(object.GetPlaybackPolicy()),
		ObjectAuthorityVersion: local.object.Version,
		TenantAuthorityVersion: local.tenant.Version,
	}
	validUntil := local.object.ValidUntil
	if !local.tenant.ValidUntil.IsZero() && local.tenant.ValidUntil.Before(validUntil) {
		validUntil = local.tenant.ValidUntil
	}
	grant.ValidUntil = timestamppb.New(validUntil)
	for _, name := range []string{object.GetPlaybackId(), requested} {
		name = strings.TrimSpace(name)
		if name != "" && name != grant.InternalName && !slices.Contains(grant.RequestedNames, name) {
			grant.RequestedNames = append(grant.RequestedNames, name)
		}
	}
	slices.Sort(grant.RequestedNames)
	return grant
}

// playbackGrantPolicy carries the edge-checkable part of a playback policy.
// A webhook policy's secret never leaves the cell: the edge sends every new
// session of such a stream to Foghorn, as it does for an authority that
// requires connected evaluation or a kind it does not know.
func playbackGrantPolicy(policy *mediaauthoritypb.PlaybackPolicy) *ipcpb.PlaybackGrantPolicy {
	out := &ipcpb.PlaybackGrantPolicy{
		Kind:           ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED,
		AllowedOrigins: slices.Clone(policy.GetAllowedOrigins()),
	}
	if policy.GetConnectedOnly() {
		return out
	}
	switch policy.GetKind() {
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC:
		out.Kind = ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT:
		jwt := policy.GetJwt()
		out.Kind = ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_JWT
		out.AllowedKids = slices.Clone(jwt.GetAllowedKeyIds())
		out.RequiredAudiences = slices.Clone(jwt.GetRequiredAudiences())
		out.RequiredClaimsJson = cloneLocalClaims(jwt.GetRequiredClaimsJson())
		for _, key := range jwt.GetActiveKeys() {
			out.ActiveKeys = append(out.ActiveKeys, &ipcpb.PlaybackGrantKey{Kid: key.GetKeyId(), PublicKeyPem: key.GetPublicKeyPem()})
		}
	}
	return out
}

// offerPlaybackGrant sends the stream's grant to the edge that just admitted a
// viewer, once per stream per control connection. It runs before the
// PLAY_REWRITE answer is sent, on the same stream, so the edge holds the grant
// when the viewer's next request arrives.
func (p *Processor) offerPlaybackGrant(nodeID, requested string, local localPlaybackAuthority) {
	if nodeID == "" || local.target == nil || local.object.Authority == nil {
		return
	}
	grant := playbackGrantFromLocal(local, requested)
	if err := p.playbackGrantDelivery().Offer(nodeID, grant); err != nil {
		p.logger.WithError(err).WithFields(logging.Fields{
			"node_id": nodeID, "internal_name": grant.GetInternalName(),
		}).Warn("Playback grant not delivered; the edge fetches it itself")
	}
}

// PlaybackGrantForStream answers an edge's grant fetch for one Mist stream.
// A denial by the signed authority is returned as a revoked grant; a cell
// holding no current authority returns an error and issues nothing.
func (p *Processor) PlaybackGrantForStream(ctx context.Context, _ string, internalName string) (*ipcpb.PlaybackGrant, error) {
	local, found, err := p.resolveReadyLocalPlayback(ctx, internalName, false)
	if IsLocalAuthorityDenied(err) {
		return &ipcpb.PlaybackGrant{InternalName: internalName, Revoked: true, RevokedReason: "signed authority denies playback"}, nil
	}
	if err != nil {
		return nil, err
	}
	if !found || local.target == nil {
		return nil, errNoPlaybackGrant
	}
	return playbackGrantFromLocal(local, ""), nil
}

// pushPlaybackGrantUpdates sends the new grant of every stream an applied
// authority touches to the edges holding one: an object apply for that
// stream, a tenant apply for all of the tenant's granted streams. One push
// per stream per edge carries a renewal or a policy change to all of the
// stream's sessions there.
func (p *Processor) pushPlaybackGrantUpdates(result localauthority.ApplyResult) {
	if p.mediaAuthorityStore == nil {
		return
	}
	delivery := p.playbackGrantDelivery()
	var streams []string
	switch result.Kind {
	case "media_object":
		if name := strings.TrimSpace(result.InternalName); name != "" {
			streams = delivery.HeldStreams(name, "")
		}
	case "tenant":
		if result.TenantID != "" {
			streams = delivery.HeldStreams("", result.TenantID)
		}
	}
	if len(streams) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), playbackGrantPushTimeout)
		defer cancel()
		for _, name := range streams {
			holders := delivery.Holders(name)
			if len(holders) == 0 {
				continue
			}
			grant, err := p.PlaybackGrantForStream(ctx, "", name)
			if err != nil {
				p.logger.WithError(err).WithField("internal_name", name).
					Info("Applied authority yields no playback grant; edges keep their grant until it expires")
				continue
			}
			for _, nodeID := range holders {
				if err := delivery.Offer(nodeID, grant); err != nil {
					p.logger.WithError(err).WithFields(logging.Fields{
						"node_id": nodeID, "internal_name": grant.GetInternalName(),
					}).Warn("Playback grant update not delivered; the edge fetches it on its next reconnect")
				}
			}
		}
	}()
}
