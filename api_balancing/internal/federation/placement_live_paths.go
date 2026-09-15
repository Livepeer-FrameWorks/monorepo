package federation

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

type PlacementSourceRegistry interface {
	SourceSnapshot(context.Context, string, string) (control.StreamEntry, bool, error)
}

// LivePushPlacementPaths observes push publishers without starting media or
// distributing credentials. Snapshot includes ingest-only nodes, independently
// of the discovery request's serving inventory. Managed and artifact sources
// require their own authority-specific adapters and cannot invent a push generation.
type LivePushPlacementPaths struct {
	CellID string
	// RegistryCellID is the persisted local namespace when it differs from control-cell identity.
	RegistryCellID string
	Registry       PlacementSourceRegistry
	Snapshot       func() *state.BalancerSnapshot
	Now            func() time.Time
}

type placementPublisher struct {
	cellID, clusterID, nodeID, generation string
	revision                              int64
	present                               bool
	dtscURL                               string
	expiresAt                             time.Time
}

type placementSourceContext struct {
	tenantID, internalName string
	grants                 map[string]balancer.PlacementSourceGrant
}

func placementSourceContextFor(authority balancer.PlacementAuthority) placementSourceContext {
	return placementSourceContext{tenantID: authority.TenantID, internalName: authority.InternalName, grants: authority.SourceGrants}
}

func (reader *LivePushPlacementPaths) now() time.Time {
	if reader != nil && reader.Now != nil {
		return reader.Now().UTC()
	}
	return time.Now().UTC()
}

func (reader *LivePushPlacementPaths) ObservePlacementPaths(ctx context.Context, authority balancer.PlacementAuthority, req *placementpb.CandidateQuery, destinations *state.BalancerSnapshot) (PlacementPathObservation, error) {
	if err := ctx.Err(); err != nil {
		return PlacementPathObservation{}, err
	}
	if reader == nil || reader.CellID == "" || reader.Registry == nil || reader.Snapshot == nil || destinations == nil ||
		authority.ObjectKind != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM || authority.IngestMode != "push" || !authority.MatchesQuery(req, reader.CellID) {
		return PlacementPathObservation{}, errors.New("push source authority or reader is unavailable")
	}
	now := reader.now()
	if now.IsZero() || !now.Before(authority.ExpiresAt) || len(destinations.Nodes) > 4096 {
		return PlacementPathObservation{}, errors.New("push source authority or inventory is invalid")
	}
	result := PlacementPathObservation{SourceGeneration: req.GetSourceGeneration(), ObservedAt: now,
		ExpiresAt: now.Add(30 * time.Second), Paths: make(map[string]balancer.PlacementNodePath, len(destinations.Nodes))}
	if authority.ExpiresAt.Before(result.ExpiresAt) {
		result.ExpiresAt = authority.ExpiresAt
	}
	for _, node := range destinations.Nodes {
		if _, duplicate := result.Paths[node.NodeID]; duplicate || node.NodeID == "" {
			return PlacementPathObservation{}, errors.New("push destination identity is ambiguous")
		}
		grant, found := authority.SourceGrants[node.ClusterID]
		_, destinationAllowed := authority.Clusters[node.ClusterID]
		if !found || !destinationAllowed || grant.CellID != reader.CellID {
			return PlacementPathObservation{}, errors.New("push destination is outside authority")
		}
		result.Paths[node.NodeID] = balancer.PlacementNodePath{Presence: placement.Absent}
	}
	// New publisher admission needs no upstream media. Active ownership is a
	// separate admission fence, never inferred from source feasibility.
	if authority.Verb == placement.Ingest {
		return result, nil
	}
	if req.GetSourceGeneration() == "" {
		return PlacementPathObservation{}, errors.New("push serving requires a source generation")
	}
	source, err := reader.publisher(ctx, placementSourceContextFor(authority))
	if err != nil {
		return PlacementPathObservation{}, err
	}
	if source == nil || source.generation != req.GetSourceGeneration() {
		return result, nil
	}
	result.ExpiresAt = minPlacementExpiry(result.ExpiresAt, source.expiresAt)
	for _, node := range destinations.Nodes {
		path := result.Paths[node.NodeID]
		if source.cellID == reader.CellID && source.nodeID == node.NodeID && source.clusterID == node.ClusterID {
			path.Presence = placement.Present
		} else if source.dtscURL != "" && (source.clusterID == node.ClusterID || authority.Clusters[node.ClusterID].AllowExternalSource) {
			path.SourceFeasible = true
		}
		// A replica's live buffer is not evidence of this publisher generation.
		// Preparation must bind its exact pull attempt before claiming presence.
		result.Paths[node.NodeID] = path
	}
	return result, nil
}

// ResolveSourceGeneration uses the same owner, withdrawal and freshness checks
// as destination discovery. It does not accept a generation from a viewer URL,
// expose a source credential, or treat a replica's live buffer as a publisher.
func (reader *LivePushPlacementPaths) ResolveSourceGeneration(ctx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
	if reader == nil || reader.CellID == "" || reader.Registry == nil || reader.Snapshot == nil ||
		authority.ObjectKind != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM || authority.IngestMode != "push" || authority.Verb != placement.Serve {
		return "", time.Time{}, errors.New("push source generation resolver is unavailable")
	}
	now := reader.now()
	if now.IsZero() || !now.Before(authority.ExpiresAt) {
		return "", time.Time{}, errors.New("push source authority is expired")
	}
	source, err := reader.publisher(ctx, placementSourceContextFor(authority))
	if err != nil {
		return "", time.Time{}, err
	}
	if source == nil {
		return "", time.Time{}, errors.New("current push source generation is unavailable")
	}
	return source.generation, minPlacementExpiry(authority.ExpiresAt, source.expiresAt), nil
}

// publisher reads its own clock rather than accepting the caller's. Freshness is
// judged against a reading taken after the registry and inventory reads, because
// a heartbeat that lands during those reads carries a timestamp later than any
// reading taken before them, and freshPlacementEvidence refuses evidence stamped
// after its reference instant. An earlier reading would therefore report a
// maximally live publisher as absent whenever a heartbeat interleaves.
func (reader *LivePushPlacementPaths) publisher(ctx context.Context, sourceContext placementSourceContext) (*placementPublisher, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, found, err := reader.Registry.SourceSnapshot(ctx, sourceContext.tenantID, sourceContext.internalName)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if entry.TenantID != sourceContext.tenantID || entry.InternalName != sourceContext.internalName ||
		(entry.IngestMode != 0 && entry.IngestMode != control.IngestPush) {
		return nil, errors.New("push source identity is inconsistent")
	}
	snapshot := reader.Snapshot()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshot == nil || len(snapshot.Nodes) > 4096 {
		return nil, errors.New("push source inventory is unavailable")
	}
	now := reader.now()
	localNodes := make(map[string]state.EnhancedBalancerNodeSnapshot, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if _, duplicate := localNodes[node.NodeID]; duplicate || node.NodeID == "" {
			return nil, errors.New("push source inventory is ambiguous")
		}
		localNodes[node.NodeID] = node
	}
	runtimeName := control.RuntimeNameFor(control.IngestPush, sourceContext.internalName)
	var claims []placementPublisher
	var fence int64
	var withdrawnRevision int64
	registryCell := reader.RegistryCellID
	if registryCell == "" {
		registryCell = reader.CellID
	}
	if loc, ok := entry.LocalLocation(registryCell); ok {
		// The ownership revision is durable, not a periodically refreshed metric.
		// A newer local withdrawal also fences an older peer advertisement.
		fence = loc.SourceRevision
		if !loc.SourceActive {
			withdrawnRevision = loc.SourceRevision
		}
		if loc.SourceActive && validPullGeneration(loc.SourceGeneration, loc.SourceRevision) && loc.SourceGeneration != "" {
			node, known := localNodes[loc.OwnerNodeID]
			grant := sourceContext.grants[node.ClusterID]
			claim := placementPublisher{cellID: reader.CellID, clusterID: node.ClusterID, nodeID: loc.OwnerNodeID,
				generation: loc.SourceGeneration, revision: loc.SourceRevision}
			if known && grant.CellID == reader.CellID && grant.AllowIngest && entry.IngestMode == control.IngestPush {
				stream := node.Streams[sourceContext.internalName]
				if node.IsActive && stream.TenantID == sourceContext.tenantID && stream.Status == "live" && stream.Playable && stream.Inputs > 0 && !stream.Replicated &&
					freshPlacementEvidence(node.LastHeartbeat, now) && freshPlacementEvidence(stream.ObservedAt, now) {
					claim.present = true
					claim.expiresAt = minPlacementExpiry(node.LastHeartbeat.Add(30*time.Second), stream.ObservedAt.Add(30*time.Second))
					if freshPlacementEvidence(node.OutputsObservedAt, now) {
						claim.dtscURL = mist.ResolvePlaybackURL(node.Outputs, node.Host, "dtsc", runtimeName)
						if claim.dtscURL != "" {
							claim.expiresAt = minPlacementExpiry(claim.expiresAt, node.OutputsObservedAt.Add(30*time.Second))
						}
					}
				}
			}
			claims = append(claims, claim)
		}
	}
	if len(entry.Locations) > 64 {
		return nil, errors.New("push source census exceeds bound")
	}
	totalEdges := 0
	for cellID, loc := range entry.Locations {
		if cellID == reader.CellID || cellID == registryCell || !freshPlacementEvidence(time.Unix(loc.AdTimestamp, 0), now) {
			continue
		}
		totalEdges += len(loc.EdgeCandidates)
		if totalEdges > 4096 {
			return nil, errors.New("push source advertisements exceed bound")
		}
		for _, edge := range loc.EdgeCandidates {
			grant := sourceContext.grants[edge.ClusterID]
			if grant.CellID != cellID || !grant.AllowIngest || !edge.IsOrigin || edge.NodeID == "" || edge.SourceGeneration == "" || !validPullGeneration(edge.SourceGeneration, edge.SourceRevision) {
				continue
			}
			claim := placementPublisher{cellID: cellID, clusterID: edge.ClusterID, nodeID: edge.NodeID,
				generation: edge.SourceGeneration, revision: edge.SourceRevision, expiresAt: time.Unix(loc.AdTimestamp, 0).Add(30 * time.Second)}
			claim.present = loc.IsLiveNow && edge.Playable
			claim.present = claim.present && freshPlacementEvidence(time.Unix(edge.SourceObservedAt, 0), now)
			claim.expiresAt = minPlacementExpiry(claim.expiresAt, time.Unix(edge.SourceObservedAt, 0).Add(30*time.Second))
			if claim.present && freshPlacementEvidence(time.Unix(edge.DTSCObservedAt, 0), now) && validPlacementDTSC(edge.DTSCURL, runtimeName) {
				claim.dtscURL = edge.DTSCURL
				claim.expiresAt = minPlacementExpiry(claim.expiresAt, time.Unix(edge.DTSCObservedAt, 0).Add(30*time.Second))
			}
			claims = append(claims, claim)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, claim := range claims {
		if claim.revision > fence {
			fence = claim.revision
		}
	}
	var source *placementPublisher
	for i := range claims {
		claim := &claims[i]
		if claim.revision != fence {
			continue
		}
		if source != nil {
			return nil, errors.New("push source ownership is ambiguous")
		}
		source = claim
	}
	if source == nil || source.revision <= withdrawnRevision || !source.present || !now.Before(source.expiresAt) {
		return nil, nil
	}
	return source, nil
}

func freshPlacementEvidence(observed, now time.Time) bool {
	return !observed.IsZero() && !observed.After(now) && now.Before(observed.Add(30*time.Second))
}

func minPlacementExpiry(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func validPlacementDTSC(raw, runtimeName string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if port := u.Port(); port != "" {
		n, portErr := strconv.Atoi(port)
		if portErr != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return u.Scheme == "dtsc" && u.Hostname() != "" && u.Hostname() != "HOST" &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" &&
		!strings.ContainsAny(u.Host, "$\\ \t\r\n") && strings.HasSuffix(u.Path, "/"+runtimeName)
}
