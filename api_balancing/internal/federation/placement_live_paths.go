package federation

import (
	"context"
	"errors"
	"fmt"
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
	// PeerLive reports the cluster a peer cell's lifecycle broadcast names as
	// currently live for the stream. It only classifies a viewer refusal as
	// starting; it never supplies a source generation.
	PeerLive func(ctx context.Context, tenantID, internalName string) (clusterID string, live bool)
	// SourceCellReachable reports whether a pull from the named source cell
	// can be arranged (ArrangeOriginPullDeps.CanArrangeFromCell). Without it
	// only a source in this cell is relayable.
	SourceCellReachable func(cellID string) bool
	// DestinationFence reads a destination's current control-connection fence;
	// nil reads control.DestinationConnectionFence.
	DestinationFence func(ctx context.Context, nodeID, clusterID string) (int64, error)
}

// PlacementDetailControlDisconnected marks a relay destination whose control
// connection is not current: a pull cannot be arranged on it.
const PlacementDetailControlDisconnected = "control_disconnected"

// destinationFence returns the destination's current control-connection
// fence. Arranging a relay on a destination needs that connection, so
// discovery and preparation both read it here.
func (reader *LivePushPlacementPaths) destinationFence(ctx context.Context, nodeID, clusterID string) (int64, error) {
	var read func(context.Context, string, string) (int64, error)
	if reader != nil {
		read = reader.DestinationFence
	}
	return readDestinationFence(ctx, read, nodeID, clusterID)
}

// readDestinationFence reads through read, or control.DestinationConnectionFence
// when read is nil. A non-positive fence is no connection.
func readDestinationFence(ctx context.Context, read func(context.Context, string, string) (int64, error), nodeID, clusterID string) (int64, error) {
	if read == nil {
		read = control.DestinationConnectionFence
	}
	fence, err := read(ctx, nodeID, clusterID)
	if err == nil && fence <= 0 {
		err = control.ErrDestinationNotConnected
	}
	return fence, err
}

// relayDestinationPath applies the relay destination's connection to its path:
// a destination without a current control connection cannot have a pull
// arranged on it and is unavailable. ok is false when the read failed, which
// leaves the destination's path unknown rather than refused.
func relayDestinationPath(ctx context.Context, read func(context.Context, string, string) (int64, error), node state.EnhancedBalancerNodeSnapshot, path balancer.PlacementNodePath) (balancer.PlacementNodePath, bool) {
	_, err := readDestinationFence(ctx, read, node.NodeID, node.ClusterID)
	switch {
	case errors.Is(err, control.ErrDestinationNotConnected):
		path.Unavailable = PlacementDetailControlDisconnected
	case err != nil:
		return path, false
	}
	return path, true
}

type placementPublisher struct {
	cellID, clusterID, nodeID, generation string
	revision                              int64
	present                               bool
	// starting marks a claim whose owner says it is live while the playable
	// evidence has not arrived yet.
	starting  bool
	dtscURL   string
	expiresAt time.Time
	// absence names the evidence that keeps a claim from being present.
	absence string
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
		} else if reader.relayRefusal(source, authority, node.ClusterID) == "" {
			path.SourceFeasible = true
			// Preparation refuses a relay destination without a current control
			// connection (an announced Helmsman restart holds node health while
			// the connection is gone), so discovery does not offer it.
			var known bool
			if path, known = relayDestinationPath(ctx, reader.DestinationFence, node, path); !known {
				delete(result.Paths, node.NodeID)
				continue
			}
		}
		// A replica's live buffer is not evidence of this publisher generation.
		// Preparation must bind its exact pull attempt before claiming presence.
		result.Paths[node.NodeID] = path
	}
	return result, nil
}

// relayRefusal names why a destination in clusterID cannot relay source, or
// returns "". Discovery and preparation both decide relay feasibility here, so
// discovery never offers a destination that preparation must then refuse.
func (reader *LivePushPlacementPaths) relayRefusal(source *placementPublisher, authority balancer.PlacementAuthority, clusterID string) string {
	switch {
	case source == nil || source.dtscURL == "":
		return "source has no playable DTSC output"
	case source.clusterID != clusterID && !authority.Clusters[clusterID].AllowExternalSource:
		return "destination cluster does not accept an external source"
	case !sourceCellReachable(reader.SourceCellReachable, reader.CellID, source.cellID):
		return "source cell " + source.cellID + " has no known control address"
	}
	return ""
}

// sourceCellReachable applies the arrangement precondition for a pull from
// sourceCellID. A missing resolver can only arrange pulls inside this cell.
func sourceCellReachable(reachable func(string) bool, localCellID, sourceCellID string) bool {
	if reachable == nil {
		return sourceCellID == localCellID
	}
	return reachable(sourceCellID)
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
	sourceContext := placementSourceContextFor(authority)
	source, starting, reason, err := reader.publisherState(ctx, sourceContext)
	if err != nil {
		return "", time.Time{}, err
	}
	if source == nil {
		if starting {
			return "", time.Time{}, fmt.Errorf("%w: %s", control.ErrLiveSourceStarting, reason)
		}
		if reader.peerStarting(ctx, sourceContext) {
			return "", time.Time{}, fmt.Errorf("%w: a peer cell announced the stream live before its first advertisement arrived (%s)", control.ErrLiveSourceStarting, reason)
		}
		return "", time.Time{}, fmt.Errorf("%w: %s", control.ErrLiveSourceOffline, reason)
	}
	return source.generation, minPlacementExpiry(authority.ExpiresAt, source.expiresAt), nil
}

// peerStarting covers the window in which another cell has announced the
// stream live but its first advertisement has not reached this cell yet. The
// announcing cluster must be an ingest grant of another cell.
func (reader *LivePushPlacementPaths) peerStarting(ctx context.Context, sourceContext placementSourceContext) bool {
	if reader.PeerLive == nil || ctx.Err() != nil {
		return false
	}
	clusterID, live := reader.PeerLive(ctx, sourceContext.tenantID, sourceContext.internalName)
	grant, granted := sourceContext.grants[clusterID]
	return live && clusterID != "" && granted && grant.AllowIngest && grant.CellID != "" && grant.CellID != reader.CellID
}

// publisher returns the current playable publisher, or nil.
func (reader *LivePushPlacementPaths) publisher(ctx context.Context, sourceContext placementSourceContext) (*placementPublisher, error) {
	source, _, _, err := reader.publisherState(ctx, sourceContext)
	return source, err
}

// publisherState also reports whether the current owner claim is starting:
// the owner says the publisher is live and nothing withdrew it, but the
// playable evidence that makes it a source is not there yet.
//
// It reads its own clock rather than accepting the caller's. Freshness is
// judged against a reading taken after the registry and inventory reads, because
// a heartbeat that lands during those reads carries a timestamp later than any
// reading taken before them, and freshPlacementEvidence refuses evidence stamped
// after its reference instant. An earlier reading would therefore report a
// maximally live publisher as absent whenever a heartbeat interleaves.
// With no present publisher, the returned reason says which evidence is
// missing.
func (reader *LivePushPlacementPaths) publisherState(ctx context.Context, sourceContext placementSourceContext) (*placementPublisher, bool, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, "", err
	}
	entry, found, err := reader.Registry.SourceSnapshot(ctx, sourceContext.tenantID, sourceContext.internalName)
	if err != nil {
		return nil, false, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, "", err
	}
	if !found {
		return nil, false, "the stream has no source entry in this cell's registry", nil
	}
	if entry.TenantID != sourceContext.tenantID || entry.InternalName != sourceContext.internalName ||
		(entry.IngestMode != 0 && entry.IngestMode != control.IngestPush) {
		return nil, false, "", errors.New("push source identity is inconsistent")
	}
	snapshot := reader.Snapshot()
	if err := ctx.Err(); err != nil {
		return nil, false, "", err
	}
	if snapshot == nil || len(snapshot.Nodes) > 4096 {
		return nil, false, "", errors.New("push source inventory is unavailable")
	}
	now := reader.now()
	localNodes := make(map[string]state.EnhancedBalancerNodeSnapshot, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if _, duplicate := localNodes[node.NodeID]; duplicate || node.NodeID == "" {
			return nil, false, "", errors.New("push source inventory is ambiguous")
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
				generation: loc.SourceGeneration, revision: loc.SourceRevision, starting: true}
			claim.absence = localClaimAbsence(loc.OwnerNodeID, known, grant.CellID == reader.CellID && grant.AllowIngest, entry.IngestMode == control.IngestPush,
				node, node.Streams[sourceContext.internalName], sourceContext.tenantID, now)
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
		return nil, false, "", errors.New("push source census exceeds bound")
	}
	totalEdges := 0
	for cellID, loc := range entry.Locations {
		if cellID == reader.CellID || cellID == registryCell || !freshPlacementEvidence(time.Unix(loc.AdTimestamp, 0), now) {
			continue
		}
		totalEdges += len(loc.EdgeCandidates)
		if totalEdges > 4096 {
			return nil, false, "", errors.New("push source advertisements exceed bound")
		}
		for _, edge := range loc.EdgeCandidates {
			grant := sourceContext.grants[edge.ClusterID]
			if grant.CellID != cellID || !grant.AllowIngest || !edge.IsOrigin || edge.NodeID == "" || edge.SourceGeneration == "" || !validPullGeneration(edge.SourceGeneration, edge.SourceRevision) {
				continue
			}
			claim := placementPublisher{cellID: cellID, clusterID: edge.ClusterID, nodeID: edge.NodeID,
				generation: edge.SourceGeneration, revision: edge.SourceRevision, expiresAt: time.Unix(loc.AdTimestamp, 0).Add(30 * time.Second)}
			claim.present = loc.IsLiveNow && edge.Playable
			claim.starting = loc.IsLiveNow
			claim.present = claim.present && freshPlacementEvidence(time.Unix(edge.SourceObservedAt, 0), now)
			switch {
			case !loc.IsLiveNow:
				claim.absence = "cell " + cellID + " advertises the stream not live"
			case !edge.Playable:
				claim.absence = "cell " + cellID + " advertises the origin buffer not playable"
			case !claim.present:
				claim.absence = "cell " + cellID + " advertised source evidence is stale"
			}
			claim.expiresAt = minPlacementExpiry(claim.expiresAt, time.Unix(edge.SourceObservedAt, 0).Add(30*time.Second))
			if claim.present && freshPlacementEvidence(time.Unix(edge.DTSCObservedAt, 0), now) && validPlacementDTSC(edge.DTSCURL, runtimeName) {
				claim.dtscURL = edge.DTSCURL
				claim.expiresAt = minPlacementExpiry(claim.expiresAt, time.Unix(edge.DTSCObservedAt, 0).Add(30*time.Second))
			}
			claims = append(claims, claim)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, "", err
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
			return nil, false, "", errors.New("push source ownership is ambiguous")
		}
		source = claim
	}
	if source == nil {
		return nil, false, "no current publisher claim in this cell or a fresh peer advertisement", nil
	}
	if source.revision <= withdrawnRevision {
		return nil, false, fmt.Sprintf("the publisher session (revision %d) ended", withdrawnRevision), nil
	}
	if !source.present {
		return nil, source.starting, source.absence, nil
	}
	if !now.Before(source.expiresAt) {
		return nil, source.starting, "the publisher's evidence expired at " + source.expiresAt.Format(time.RFC3339Nano), nil
	}
	return source, false, "", nil
}

// localClaimAbsence names the first condition that keeps this cell's owner
// claim from being a present publisher, or "" when none does.
func localClaimAbsence(ownerNodeID string, known, granted, push bool, node state.EnhancedBalancerNodeSnapshot, stream state.BalancerStreamSummary, tenantID string, now time.Time) string {
	switch {
	case !known:
		return "owner node " + ownerNodeID + " is not in this replica's healthy inventory"
	case !granted:
		return "owner node's cluster " + node.ClusterID + " is not an ingest grant of this cell"
	case !push:
		return "the stream is not a push source"
	case !node.IsActive:
		return "owner node is not active"
	case !freshPlacementEvidence(node.LastHeartbeat, now):
		return "owner node heartbeat is not fresh (" + node.LastHeartbeat.Format(time.RFC3339Nano) + ")"
	case stream.Status != "live" || stream.Inputs == 0:
		return fmt.Sprintf("owner node reports status %q with %d inputs", stream.Status, stream.Inputs)
	case !stream.Playable:
		return "owner node's buffer is not playable"
	case stream.TenantID != tenantID:
		return "owner node's stream instance has no verified tenant"
	case stream.Replicated:
		return "owner node's instance is a replica"
	case !freshPlacementEvidence(stream.ObservedAt, now):
		return "owner node's stream report is not fresh (" + stream.ObservedAt.Format(time.RFC3339Nano) + ")"
	}
	return ""
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

// destinationUnavailableReason names the first check a selected destination
// fails, with the evidence age, so a refusal says why. Report freshness is
// balancer.PlacementNodeEvidence, the predicate discovery used to offer it.
func destinationUnavailableReason(node *state.EnhancedBalancerNodeSnapshot, verb placement.Verb, now time.Time) string {
	switch {
	case node == nil:
		return "not in this cell's telemetry"
	case !node.IsActive:
		return "not active"
	case verb == placement.Serve && !node.CapEdge:
		return "not an edge"
	case verb == placement.Ingest && !node.CapIngest:
		return "not an ingest node"
	}
	_, gap := balancer.PlacementNodeEvidence(*node, now)
	return gap
}

// destinationEvidenceUntil is when the destination's own reports stop
// supporting a preparation.
func destinationEvidenceUntil(node state.EnhancedBalancerNodeSnapshot, now time.Time) time.Time {
	until, _ := balancer.PlacementNodeEvidence(node, now)
	return until
}
