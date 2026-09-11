package federation

import (
	"context"
	"errors"
	"slices"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// ConfiguredSourcePlacementPaths observes pull and Mist-native live inputs. Their
// generation is the signed configuration, not a publisher session: no encoder owns
// them, so an entitled cluster may start the input on demand instead of waiting for
// an origin. Feasibility therefore has two independent grounds — signed consent to
// dial the configured input, or an existing live copy this destination may relay —
// and neither is inferred from the viewer's request.
type ConfiguredSourcePlacementPaths struct {
	CellID string
	// RegistryCellID is the persisted local namespace when it differs from control-cell identity.
	RegistryCellID string
	Authority      PlacementAuthorityReader
	Secrets        control.MediaSourceSecretReader
	Registry       PlacementSourceRegistry
	Snapshot       func() *state.BalancerSnapshot
	Now            func() time.Time
}

// configuredSource is credential-free evidence that one node currently serves the
// configured input, with the DTSC path a destination could relay from. It is not
// an ownership claim: several entitled clusters may run the same input.
type configuredSource struct {
	cellID, clusterID, nodeID string
	dtscURL                   string
	expiresAt                 time.Time
}

func (reader *ConfiguredSourcePlacementPaths) now() time.Time {
	if reader != nil && reader.Now != nil {
		return reader.Now().UTC()
	}
	return time.Now().UTC()
}

func (reader *ConfiguredSourcePlacementPaths) registryCell() string {
	if reader.RegistryCellID != "" {
		return reader.RegistryCellID
	}
	return reader.CellID
}

// ConfiguredIngestMode reports whether a signed live authority is a managed input
// whose source is configured rather than pushed by an encoder.
func ConfiguredIngestMode(mode string) bool {
	return mode == "pull" || mode == "mist_native"
}

// describe re-reads the signed pair behind an already compiled authority and opens
// the sealed input. The re-read is fenced on both authority versions so a refresh
// between policy compilation and source observation cannot substitute a different
// configuration under the same generation.
func (reader *ConfiguredSourcePlacementPaths) describe(ctx context.Context, authority balancer.PlacementAuthority) (control.MediaSourceDescriptor, error) {
	if reader == nil || reader.CellID == "" || reader.Authority == nil || reader.Secrets == nil || reader.Snapshot == nil {
		return control.MediaSourceDescriptor{}, errors.New("configured source reader is unavailable")
	}
	if authority.ObjectKind != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM || !ConfiguredIngestMode(authority.IngestMode) ||
		authority.TenantAuthorityVersion <= 0 || authority.ObjectAuthorityVersion <= 0 {
		return control.MediaSourceDescriptor{}, errors.New("configured source authority is not a managed live input")
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pair, err := reader.Authority.Placement(readCtx, authority.TenantID, authority.ObjectID, authority.InternalName)
	if err != nil {
		return control.MediaSourceDescriptor{}, err
	}
	if err = readCtx.Err(); err != nil {
		return control.MediaSourceDescriptor{}, err
	}
	if pair.Tenant.Version != authority.TenantAuthorityVersion || pair.Object.Version != authority.ObjectAuthorityVersion {
		return control.MediaSourceDescriptor{}, errors.New("configured source authority changed during observation")
	}
	descriptor, err := control.DescribeMediaSource(pair, reader.Secrets)
	if err != nil {
		return control.MediaSourceDescriptor{}, err
	}
	if descriptor.TenantID != authority.TenantID || descriptor.ObjectID != authority.ObjectID || descriptor.InternalName != authority.InternalName ||
		descriptor.Kind != authority.IngestMode || descriptor.Generation == "" || descriptor.RuntimeName == "" {
		return control.MediaSourceDescriptor{}, errors.New("configured source identity differs from authority")
	}
	// A Mist-native input is elected to exactly one cluster, so an empty list
	// there is a broken election rather than an unpinned source.
	if authority.IngestMode == "mist_native" && len(descriptor.AllowedClusters) != 1 {
		return control.MediaSourceDescriptor{}, errors.New("native source election is invalid")
	}
	return descriptor, nil
}

// ResolveSourceGeneration returns the configuration generation. A configured input
// exists by signed configuration, so it does not require a live copy first; media
// readiness stays a separate observation that presence and feasibility carry.
func (reader *ConfiguredSourcePlacementPaths) ResolveSourceGeneration(ctx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
	if authority.Verb != placement.Serve {
		return "", time.Time{}, errors.New("configured source generation resolver is unavailable")
	}
	now := reader.now()
	if now.IsZero() || !now.Before(authority.ExpiresAt) {
		return "", time.Time{}, errors.New("configured source authority is expired")
	}
	descriptor, err := reader.describe(ctx, authority)
	if err != nil {
		return "", time.Time{}, err
	}
	return descriptor.Generation, authority.ExpiresAt, nil
}

func (reader *ConfiguredSourcePlacementPaths) ObservePlacementPaths(ctx context.Context, authority balancer.PlacementAuthority, req *placementpb.CandidateQuery, destinations *state.BalancerSnapshot) (PlacementPathObservation, error) {
	if err := ctx.Err(); err != nil {
		return PlacementPathObservation{}, err
	}
	if reader == nil || reader.CellID == "" || destinations == nil || len(destinations.Nodes) > 4096 || !authority.MatchesQuery(req, reader.CellID) {
		return PlacementPathObservation{}, errors.New("configured source authority or inventory is unavailable")
	}
	// Publisher admission belongs to the push path. A configured input has no
	// encoder to admit, and its cluster election is signed, not placed here.
	if authority.Verb == placement.Ingest {
		return PlacementPathObservation{}, errors.New("configured sources have no publisher admission")
	}
	descriptor, err := reader.describe(ctx, authority)
	if err != nil {
		return PlacementPathObservation{}, err
	}
	now := reader.now()
	if now.IsZero() || !now.Before(authority.ExpiresAt) {
		return PlacementPathObservation{}, errors.New("configured source authority is expired")
	}
	result := PlacementPathObservation{SourceGeneration: req.GetSourceGeneration(), ObservedAt: now,
		ExpiresAt: now.Add(30 * time.Second), Paths: make(map[string]balancer.PlacementNodePath, len(destinations.Nodes))}
	if authority.ExpiresAt.Before(result.ExpiresAt) {
		result.ExpiresAt = authority.ExpiresAt
	}
	for _, node := range destinations.Nodes {
		if _, duplicate := result.Paths[node.NodeID]; duplicate || node.NodeID == "" {
			return PlacementPathObservation{}, errors.New("configured destination identity is ambiguous")
		}
		grant, found := authority.SourceGrants[node.ClusterID]
		_, destinationAllowed := authority.Clusters[node.ClusterID]
		if !found || !destinationAllowed || grant.CellID != reader.CellID {
			return PlacementPathObservation{}, errors.New("configured destination is outside authority")
		}
		result.Paths[node.NodeID] = balancer.PlacementNodePath{Presence: placement.Absent}
	}
	if req.GetSourceGeneration() == "" {
		return PlacementPathObservation{}, errors.New("configured serving requires a source generation")
	}
	if req.GetSourceGeneration() != descriptor.Generation {
		return result, nil
	}
	source, err := reader.liveSource(ctx, descriptor, authority)
	if err != nil {
		return PlacementPathObservation{}, err
	}
	if source != nil {
		result.ExpiresAt = minPlacementExpiry(result.ExpiresAt, source.expiresAt)
	}
	for _, node := range destinations.Nodes {
		result.Paths[node.NodeID] = reader.nodePath(descriptor, authority, node, source, now)
	}
	return result, nil
}

// nodePath decides one destination's evidence. Presence is this node's own live
// copy; feasibility is either signed consent to dial the configured input or an
// existing copy the destination may relay under its external-source consent.
func (reader *ConfiguredSourcePlacementPaths) nodePath(descriptor control.MediaSourceDescriptor, authority balancer.PlacementAuthority, node state.EnhancedBalancerNodeSnapshot, source *configuredSource, now time.Time) balancer.PlacementNodePath {
	path := balancer.PlacementNodePath{Presence: placement.Absent}
	if reader.livesOn(node, descriptor, authority, now) {
		path.Presence = placement.Present
		return path
	}
	if reader.mayOriginate(descriptor, authority, node.ClusterID) {
		path.SourceFeasible = true
		return path
	}
	if source == nil || source.dtscURL == "" || (source.cellID == reader.CellID && source.nodeID == node.NodeID) {
		return path
	}
	if source.clusterID == node.ClusterID || authority.Clusters[node.ClusterID].AllowExternalSource {
		path.SourceFeasible = true
	}
	return path
}

// mayOriginate reports signed consent for a cluster to dial the configured input
// itself: the input's own cluster pin, the cluster's ingest consent and, for a
// private upstream, that cluster's private-source consent. An empty pin is an
// unpinned public source, which any entitled cluster may dial; the control plane
// refuses to create a private or multicast source without a pin, and the private
// consent check below still applies if one ever arrives unpinned.
func (reader *ConfiguredSourcePlacementPaths) mayOriginate(descriptor control.MediaSourceDescriptor, authority balancer.PlacementAuthority, clusterID string) bool {
	if clusterID == "" || (len(descriptor.AllowedClusters) > 0 && !slices.Contains(descriptor.AllowedClusters, clusterID)) {
		return false
	}
	grant, found := authority.SourceGrants[clusterID]
	if !found || grant.CellID != reader.CellID || !grant.AllowIngest {
		return false
	}
	return !descriptor.PrivateSource || grant.AllowPrivatePullSources
}

// livesOn reports this node's own ready copy of the configured input. A relayed
// copy still serves viewers, so replication does not disqualify presence.
func (reader *ConfiguredSourcePlacementPaths) livesOn(node state.EnhancedBalancerNodeSnapshot, descriptor control.MediaSourceDescriptor, authority balancer.PlacementAuthority, now time.Time) bool {
	if !node.IsActive || !freshPlacementEvidence(node.LastHeartbeat, now) {
		return false
	}
	stream, running := node.Streams[descriptor.InternalName]
	return running && stream.TenantID == authority.TenantID && stream.Status == "live" && stream.BufferState == "FULL" &&
		stream.Inputs > 0 && freshPlacementEvidence(stream.ObservedAt, now)
}

// liveSource picks one existing copy a destination could relay from: a local
// origin copy first, otherwise a federated origin advertised by another cell.
// A relayed copy is never offered as a relay source, so pull chains cannot form.
// It reads its own clock after fetching the inventory rather than accepting the
// caller's: a node that goes live during that fetch is stamped later than any
// earlier reading, and freshPlacementEvidence refuses evidence stamped after its
// reference instant, so an earlier reading would hide a usable relay source.
func (reader *ConfiguredSourcePlacementPaths) liveSource(ctx context.Context, descriptor control.MediaSourceDescriptor, authority balancer.PlacementAuthority) (*configuredSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := reader.Snapshot()
	if snapshot == nil || len(snapshot.Nodes) > 4096 {
		return nil, errors.New("configured source inventory is unavailable")
	}
	now := reader.now()
	var best *configuredSource
	for _, node := range snapshot.Nodes {
		stream, running := node.Streams[descriptor.InternalName]
		if !running || stream.Replicated || !reader.livesOn(node, descriptor, authority, now) ||
			!reader.mayOriginate(descriptor, authority, node.ClusterID) || !freshPlacementEvidence(node.OutputsObservedAt, now) {
			continue
		}
		candidate := &configuredSource{cellID: reader.CellID, clusterID: node.ClusterID, nodeID: node.NodeID,
			dtscURL:   mist.ResolvePlaybackURL(node.Outputs, node.Host, "dtsc", descriptor.RuntimeName),
			expiresAt: minPlacementExpiry(node.LastHeartbeat.Add(30*time.Second), stream.ObservedAt.Add(30*time.Second))}
		candidate.expiresAt = minPlacementExpiry(candidate.expiresAt, node.OutputsObservedAt.Add(30*time.Second))
		if candidate.dtscURL == "" || !validPlacementDTSC(candidate.dtscURL, descriptor.RuntimeName) {
			continue
		}
		if best == nil || best.expiresAt.Before(candidate.expiresAt) {
			best = candidate
		}
	}
	if best != nil {
		return best, nil
	}
	return reader.federatedSource(ctx, descriptor, authority)
}

// federatedSource reads its own clock after the registry read for the same
// reason liveSource does: an advertisement that lands during the read carries a
// timestamp later than any reading taken before it.
func (reader *ConfiguredSourcePlacementPaths) federatedSource(ctx context.Context, descriptor control.MediaSourceDescriptor, authority balancer.PlacementAuthority) (*configuredSource, error) {
	if reader.Registry == nil {
		return nil, nil
	}
	entry, found, err := reader.Registry.SourceSnapshot(ctx, descriptor.TenantID, descriptor.InternalName)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	now := reader.now()
	if entry.TenantID != descriptor.TenantID || entry.InternalName != descriptor.InternalName {
		return nil, errors.New("configured source identity is inconsistent")
	}
	if len(entry.Locations) > 64 {
		return nil, errors.New("configured source census exceeds bound")
	}
	registryCell := reader.registryCell()
	var best *configuredSource
	totalEdges := 0
	for cellID, location := range entry.Locations {
		if cellID == reader.CellID || cellID == registryCell || !location.IsLiveNow || !freshPlacementEvidence(time.Unix(location.AdTimestamp, 0), now) {
			continue
		}
		totalEdges += len(location.EdgeCandidates)
		if totalEdges > 4096 {
			return nil, errors.New("configured source advertisements exceed bound")
		}
		for _, edge := range location.EdgeCandidates {
			grant, granted := authority.SourceGrants[edge.ClusterID]
			if !granted || grant.CellID != cellID || !grant.AllowIngest || !edge.IsOrigin || edge.NodeID == "" || edge.BufferState != "FULL" ||
				!freshPlacementEvidence(time.Unix(edge.DTSCObservedAt, 0), now) || !validPlacementDTSC(edge.DTSCURL, descriptor.RuntimeName) {
				continue
			}
			candidate := &configuredSource{cellID: cellID, clusterID: edge.ClusterID, nodeID: edge.NodeID, dtscURL: edge.DTSCURL,
				expiresAt: minPlacementExpiry(time.Unix(location.AdTimestamp, 0).Add(30*time.Second), time.Unix(edge.DTSCObservedAt, 0).Add(30*time.Second))}
			if best == nil || best.expiresAt.Before(candidate.expiresAt) {
				best = candidate
			}
		}
	}
	return best, nil
}
