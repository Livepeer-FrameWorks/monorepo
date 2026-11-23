package control

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

type PublisherRuntimeBinding struct {
	RuntimeName   string
	LayoutVersion int32
	LeaseEpoch    int64
	ClusterID     string
	TenantID      string
	Generation    string
	ConnectorPID  int64
}

// PublisherRuntimeBindingForGeneration addresses a historical generation, never
// the current stream alias. Ended rows retain the binding for delayed cleanup.
func PublisherRuntimeBindingForGeneration(ctx context.Context, tenant, internal, node, generation string) (PublisherRuntimeBinding, error) {
	if db == nil {
		return PublisherRuntimeBinding{}, errors.New("publisher runtime store unavailable")
	}
	binding, err := foghorndb.New(db).GetPublisherRuntimeBinding(ctx, foghorndb.GetPublisherRuntimeBindingParams{TenantID: tenant, Generation: generation, InternalName: internal, NodeID: node})
	if err != nil {
		return PublisherRuntimeBinding{}, err
	}
	if binding.RuntimeLayoutVersion == 1 {
		parsed, ok := streamident.ParseRuntimeBinding(binding.RuntimeName)
		if !ok || parsed.Role != streamident.RuntimePublisher || parsed.InternalName != internal || parsed.ExecutionID != generation {
			return PublisherRuntimeBinding{}, errors.New("publisher runtime binding differs")
		}
	} else if binding.RuntimeLayoutVersion != 0 || binding.RuntimeName != "live+"+internal {
		return PublisherRuntimeBinding{}, errors.New("unsupported publisher runtime binding")
	}
	return PublisherRuntimeBinding{RuntimeName: binding.RuntimeName, LayoutVersion: int32(binding.RuntimeLayoutVersion), LeaseEpoch: binding.PublisherLeaseEpoch, ClusterID: binding.ClusterID, TenantID: tenant, Generation: generation, ConnectorPID: binding.ConnectorPid}, nil
}

func PublisherRuntimeForGeneration(ctx context.Context, tenant, internal, node, generation string) (string, error) {
	binding, err := PublisherRuntimeBindingForGeneration(ctx, tenant, internal, node, generation)
	return binding.RuntimeName, err
}

// NodeRuntimeName reads the exact local source or accepted relay binding. A
// fenced publisher on another node requires a prepared destination pull.
func (r *StreamRegistry) NodeRuntimeName(ctx context.Context, tenant, internal, node string) (string, error) {
	entry, found, err := r.SourceSnapshot(ctx, tenant, internal)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrUnknownStream
	}
	loc, _ := entry.LocalLocation(r.clusterID)
	if loc.SourceActive && loc.OwnerNodeID == node && loc.SourceRuntimeName != "" {
		return loc.SourceRuntimeName, nil
	}
	if pull, ok := inboundPulls(loc)[node]; ok && !pull.Cleared && pull.DestRuntimeName != "" {
		return pull.DestRuntimeName, nil
	}
	for _, peer := range entry.Locations {
		if binding, ok := streamident.ParseRuntimeBinding(peer.SourceRuntimeName); ok && binding.Role == streamident.RuntimePublisher {
			return "", errors.New("isolated publisher requires an exact node runtime binding")
		}
	}
	if entry.RuntimeName != "" {
		return entry.RuntimeName, nil
	}
	return RuntimeNameFor(entry.IngestMode, internal), nil
}

func PublisherRuntimeBindingForClaim(ctx context.Context, tenant, internal, node, execution string) (PublisherRuntimeBinding, error) {
	if db == nil {
		return PublisherRuntimeBinding{}, errors.New("publisher runtime store unavailable")
	}
	session, err := foghorndb.New(db).GetPublisherAdmission(ctx, foghorndb.GetPublisherAdmissionParams{TenantID: tenant, NodeID: node, TriggerUuid: execution})
	if err != nil {
		return PublisherRuntimeBinding{}, err
	}
	if session.StreamInternalName != internal {
		return PublisherRuntimeBinding{}, errors.New("publisher execution names a different stream")
	}
	return PublisherRuntimeBindingForGeneration(ctx, tenant, internal, node, session.Generation)
}

// EndBoundPublisherRuntime records the exact physical buffer's unload. Its
// historical event does not give a relay authority to end any publisher.
func EndBoundPublisherRuntime(ctx context.Context, tenant, node, runtime string, eventMillis int64, logger logging.Logger) (int, error) {
	return endBoundPublisherRuntime(ctx, tenant, node, runtime, eventMillis, logger, true)
}

// RetireAbsentPublisherRuntime ends an absent publisher without manufacturing
// a STREAM_END observation; a later unload owns stream.ended.
func RetireAbsentPublisherRuntime(ctx context.Context, tenant, node, runtime string, eventMillis int64, logger logging.Logger) (int, error) {
	return endBoundPublisherRuntime(ctx, tenant, node, runtime, eventMillis, logger, false)
}

func endBoundPublisherRuntime(ctx context.Context, tenant, node, runtime string, eventMillis int64, logger logging.Logger, bufferUnloaded bool) (int, error) {
	parsed, ok := streamident.ParseRuntimeBinding(runtime)
	if !ok {
		return 0, errors.New("terminal report has no isolated runtime binding")
	}
	if parsed.Role != streamident.RuntimePublisher {
		return 0, nil
	}
	binding, err := PublisherRuntimeBindingForGeneration(ctx, tenant, parsed.InternalName, node, parsed.ExecutionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if binding.LayoutVersion != 1 || binding.RuntimeName != runtime {
		return 0, errors.New("terminal runtime differs from persisted publisher binding")
	}
	ended, err := EndExactMissingIngestSession(ctx, tenant, node, parsed.InternalName, parsed.ExecutionID, binding.ConnectorPID, eventMillis, logger)
	if err == nil && bufferUnloaded {
		err = markBoundPublisherBufferEnded(ctx, tenant, node, parsed.InternalName, parsed.ExecutionID, runtime, eventMillis)
	}
	if ended {
		return 1, err
	}
	return 0, err
}

// AcceptsNodeRuntime checks the physical process a node may project as this
// stream. Buffer names describe identity only; the registry supplies ownership.
func (r *StreamRegistry) AcceptsNodeRuntime(ctx context.Context, internal, node, runtime string, terminal ...bool) (bool, error) {
	binding, bound := streamident.ParseRuntimeBinding(runtime)
	terminalReport := len(terminal) == 1 && terminal[0]
	if r == nil {
		return !bound, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.RLock()
	cached, found := r.byInt[internal]
	var entry StreamEntry
	if found {
		entry = cached.entry
		entry.Locations = cloneLocations(entry.Locations)
	}
	r.mu.RUnlock()
	if bound {
		// Another replica may have accepted or retired the pull since this
		// cache entry arrived. Physical admission consumes shared evidence.
		var err error
		entry, found, err = r.currentSourceEntry(ctx, internal)
		if err != nil {
			return false, err
		}
	}
	if !found {
		return !bound, nil
	}
	loc, _ := entry.LocalLocation(r.clusterID)
	if terminalReport && binding.Role == streamident.RuntimePublisher {
		for _, peer := range entry.Locations {
			if peer.SourceLeaseEpoch > loc.SourceLeaseEpoch {
				return false, nil
			}
		}
	}
	expected := ""
	if (loc.SourceActive || terminalReport) && loc.OwnerNodeID == node && loc.SourceRuntimeName != "" {
		expected = loc.SourceRuntimeName
	}
	if expected == "" {
		if pull, ok := inboundPulls(loc)[node]; ok && (!pull.Cleared || terminalReport) {
			expected = pull.DestRuntimeName
		}
	}
	if expected != "" {
		return runtime == expected, nil
	}
	return !bound, nil
}

// BeginNodeRuntime orders readiness reset with local source/pull mutations. A
// delayed preparation cannot replace the successor's physical buffer identity.
func (r *StreamRegistry) BeginNodeRuntime(internal, node, tenant, runtime string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	cached, found := r.byInt[internal]
	if !found || cached.entry.TenantID != tenant {
		return false
	}
	loc, _ := cached.entry.LocalLocation(r.clusterID)
	expected := ""
	if loc.SourceActive && loc.OwnerNodeID == node {
		expected = loc.SourceRuntimeName
	}
	if expected == "" {
		if pull, ok := inboundPulls(loc)[node]; ok && !pull.Cleared {
			expected = pull.DestRuntimeName
		}
	}
	if expected == "" || expected != runtime {
		return false
	}
	state.DefaultManager().BeginIsolatedRuntime(internal, node, tenant, runtime)
	return true
}

// RuntimeNameForEntryNode is used with a verified source snapshot to build that
// node's transport URLs. It never substitutes one node's pull for another's.
func RuntimeNameForEntryNode(entry StreamEntry, cell, node string) string {
	loc, _ := entry.LocalLocation(cell)
	if loc.SourceActive && loc.OwnerNodeID == node && loc.SourceRuntimeName != "" {
		return loc.SourceRuntimeName
	}
	if pull, ok := inboundPulls(loc)[node]; ok && !pull.Cleared && pull.DestRuntimeName != "" {
		return pull.DestRuntimeName
	}
	return RuntimeNameFor(entry.IngestMode, entry.InternalName)
}

func ValidPublisherPullRuntimes(internal, generation, attempt, source, destination string, epoch int64) bool {
	if source == "" && destination == "" {
		return true
	}
	origin, ok := streamident.ParseRuntimeBinding(source)
	if !ok || origin.Role != streamident.RuntimePublisher || origin.InternalName != internal || origin.ExecutionID != generation || epoch <= 0 {
		return false
	}
	relay, ok := streamident.ParseRuntimeBinding(destination)
	return ok && relay.Role == streamident.RuntimeRelay && relay.InternalName == internal && relay.ExecutionID == attempt
}
