package balancer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// PlacementMembershipReconcileInterval is how often each replica re-reads
	// membership beside its watch, so a dropped stream or a lost wake costs at
	// most this much freshness.
	PlacementMembershipReconcileInterval = 60 * time.Second
	placementMembershipWatchRetry        = 5 * time.Second
	placementMembershipReadTimeout       = 5 * time.Second
	placementMembershipMaxClusters       = 4096
	placementMembershipMaxNodes          = 4096
)

// ErrPlacementMembershipUnavailable means no membership snapshot can answer:
// none was ever received or stored, or Quartermaster predates the membership
// RPCs. Callers then read inventory from Quartermaster per request.
var ErrPlacementMembershipUnavailable = errors.New("placement membership snapshot is unavailable")

// PlacementMembershipSource is the Quartermaster side of membership.
type PlacementMembershipSource interface {
	GetPlacementMembership(ctx context.Context, controlCellID string) (*quartermasterpb.PlacementMembership, error)
	WatchPlacementMembership(ctx context.Context, controlCellID string) (quartermasterpb.ClusterService_WatchPlacementMembershipClient, error)
}

// StoredPlacementMembership is the cell's durable copy of one membership.
type StoredPlacementMembership struct {
	Revision    string
	Payload     []byte
	ObservedAt  time.Time
	ConfirmedAt time.Time
}

// PlacementMembershipStore persists the latest membership per cell. Load
// returns nil without error when the cell has none.
type PlacementMembershipStore interface {
	LoadPlacementMembership(ctx context.Context, cellID string) (*StoredPlacementMembership, error)
	StorePlacementMembership(ctx context.Context, cellID string, membership StoredPlacementMembership) error
}

type PlacementMembershipConfig struct {
	CellID string
	// Source returns the current Quartermaster client, or nil while none is
	// connected.
	Source func() PlacementMembershipSource
	Store  PlacementMembershipStore
	Now    func() time.Time
	Logger logging.Logger
}

// PlacementMembership holds this cell's Quartermaster placement membership:
// its clusters, their active flags and their edge nodes' admission. It is fed
// by a watch stream and a periodic read, persisted on change, and loaded from
// the cell database at start, so placement keeps answering while
// Quartermaster is unreachable. Quartermaster is the only writer of
// membership, so an outage freezes it rather than making it wrong; it has no
// hard expiry and reports its age instead.
type PlacementMembership struct {
	cellID  string
	source  func() PlacementMembershipSource
	store   PlacementMembershipStore
	now     func() time.Time
	logger  logging.Logger
	started time.Time

	mu          sync.RWMutex
	view        *placementMembershipView
	legacy      bool
	confirmedAt time.Time
	// persisted is the revision last written to the store, so a failed write
	// is retried by the next confirmation of the same revision.
	persisted string
}

type placementMembershipView struct {
	revision   string
	observedAt time.Time
	active     map[string]bool
	nodes      map[string][]*quartermasterpb.MediaPlacementInventoryNode
}

func NewPlacementMembership(cfg PlacementMembershipConfig) *PlacementMembership {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &PlacementMembership{cellID: cfg.CellID, source: cfg.Source, store: cfg.Store, now: now, logger: cfg.Logger, started: now()}
}

// Load installs the stored membership when no live one has arrived yet.
func (m *PlacementMembership) Load(ctx context.Context) error {
	if m == nil || m.store == nil {
		return nil
	}
	stored, err := m.store.LoadPlacementMembership(ctx, m.cellID)
	if err != nil || stored == nil {
		return err
	}
	var membership quartermasterpb.PlacementMembership
	if decodeErr := proto.Unmarshal(stored.Payload, &membership); decodeErr != nil {
		return fmt.Errorf("decode stored placement membership: %w", decodeErr)
	}
	view, err := m.compile(&membership)
	if err != nil {
		return fmt.Errorf("stored placement membership: %w", err)
	}
	if view.revision != stored.Revision {
		return fmt.Errorf("stored placement membership revision %q does not match its payload %q", stored.Revision, view.revision)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.view == nil {
		m.view = view
		m.confirmedAt = stored.ConfirmedAt
		m.persisted = stored.Revision
	}
	return nil
}

// Apply installs a membership received from Quartermaster and persists it
// when its revision differs from the stored one. A different revision read
// before the held one is ignored, so a slow periodic read cannot replace what
// the watch already delivered.
func (m *PlacementMembership) Apply(ctx context.Context, membership *quartermasterpb.PlacementMembership) error {
	view, err := m.compile(membership)
	if err != nil {
		return err
	}
	m.mu.Lock()
	changed := m.view == nil || m.view.revision != view.revision
	if changed && m.view != nil && view.observedAt.Before(m.view.observedAt) {
		m.mu.Unlock()
		return nil
	}
	now := m.now()
	m.legacy = false
	m.confirmedAt = now
	if changed {
		m.view = view
	}
	persist := m.store != nil && m.persisted != view.revision
	m.mu.Unlock()
	if !persist {
		return nil
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(membership)
	if err != nil {
		return fmt.Errorf("encode placement membership: %w", err)
	}
	if err := m.store.StorePlacementMembership(ctx, m.cellID, StoredPlacementMembership{
		Revision: view.revision, Payload: payload, ObservedAt: view.observedAt, ConfirmedAt: now,
	}); err != nil {
		return fmt.Errorf("persist placement membership: %w", err)
	}
	m.mu.Lock()
	if m.view != nil && m.view.revision == view.revision {
		m.persisted = view.revision
	}
	m.mu.Unlock()
	return nil
}

// Inventory builds the membership half of a placement inventory for clusters
// the caller already took from signed tenant authority. A cluster the
// membership does not list as active is refused exactly as Quartermaster
// refuses it. The inventory is stamped readAt: its freshness is the caller's
// request instant, and the snapshot's own age is reported, not enforced.
func (m *PlacementMembership) Inventory(tenantID string, clusterIDs []string, readAt time.Time) (*quartermasterpb.MediaPlacementInventory, error) {
	if m == nil {
		return nil, ErrPlacementMembershipUnavailable
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.legacy || m.view == nil {
		return nil, ErrPlacementMembershipUnavailable
	}
	out := &quartermasterpb.MediaPlacementInventory{
		TenantId: tenantID, ControlCellId: m.cellID, ClusterIds: slices.Clone(clusterIDs),
		ObservedAt: timestamppb.New(readAt), Complete: true,
	}
	for _, clusterID := range clusterIDs {
		if !m.view.active[clusterID] {
			// Missing, inactive and moved clusters are deliberately indistinguishable.
			return nil, status.Error(codes.PermissionDenied, "requested placement inventory is not authorized")
		}
		for _, node := range m.view.nodes[clusterID] {
			out.Nodes = append(out.Nodes, &quartermasterpb.MediaPlacementInventoryNode{
				ClusterId: node.GetClusterId(), NodeId: node.GetNodeId(), AdmissionEnabled: node.GetAdmissionEnabled(),
			})
		}
	}
	return out, nil
}

// AgeSeconds is how long ago Quartermaster last confirmed the held
// membership, or how long this process has waited for a first one. It is zero
// while Quartermaster predates membership and inventory is read per request.
func (m *PlacementMembership) AgeSeconds() float64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.legacy {
		return 0
	}
	since := m.confirmedAt
	if since.IsZero() {
		since = m.started
	}
	return m.now().Sub(since).Seconds()
}

// Run keeps membership current until ctx ends: one watch stream plus a
// periodic read. Every replica runs its own, since each serves placement from
// its own memory; the stream is per cell, not per request.
func (m *PlacementMembership) Run(ctx context.Context) {
	if m == nil || m.source == nil {
		return
	}
	go m.runReconcile(ctx)
	m.runWatch(ctx)
}

func (m *PlacementMembership) runReconcile(ctx context.Context) {
	for {
		m.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(PlacementMembershipReconcileInterval):
		}
	}
}

// Reconcile reads membership once. An Unimplemented answer marks
// Quartermaster as predating membership until a later read succeeds.
func (m *PlacementMembership) Reconcile(ctx context.Context) {
	source := m.source()
	if source == nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, placementMembershipReadTimeout)
	defer cancel()
	membership, err := source.GetPlacementMembership(readCtx, m.cellID)
	if status.Code(err) == codes.Unimplemented {
		m.markLegacy()
		return
	}
	if err != nil {
		m.warn(err, "Placement membership read failed; the held snapshot stays in use")
		return
	}
	if err := m.Apply(ctx, membership); err != nil {
		m.warn(err, "Placement membership read was not applied")
	}
}

func (m *PlacementMembership) runWatch(ctx context.Context) {
	for ctx.Err() == nil {
		retry := placementMembershipWatchRetry
		if source := m.source(); source != nil {
			if m.watchOnce(ctx, source) {
				retry = PlacementMembershipReconcileInterval
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

// watchOnce consumes one stream and reports whether Quartermaster predates it.
func (m *PlacementMembership) watchOnce(ctx context.Context, source PlacementMembershipSource) bool {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := source.WatchPlacementMembership(streamCtx, m.cellID)
	for err == nil {
		var membership *quartermasterpb.PlacementMembership
		membership, err = stream.Recv()
		if err != nil {
			break
		}
		if applyErr := m.Apply(ctx, membership); applyErr != nil {
			m.warn(applyErr, "Placement membership update was not applied")
		}
	}
	if status.Code(err) == codes.Unimplemented {
		m.markLegacy()
		return true
	}
	if ctx.Err() == nil {
		m.warn(err, "Placement membership watch ended; periodic reads continue")
	}
	return false
}

func (m *PlacementMembership) markLegacy() {
	m.mu.Lock()
	wasLegacy := m.legacy
	m.legacy = true
	m.mu.Unlock()
	if !wasLegacy && m.logger != nil {
		m.logger.WithField("cell_id", m.cellID).Warn("Quartermaster does not serve placement membership; placement reads inventory per request")
	}
}

func (m *PlacementMembership) warn(err error, message string) {
	if m.logger != nil {
		m.logger.WithError(err).WithField("cell_id", m.cellID).Warn(message)
	}
}

func (m *PlacementMembership) compile(membership *quartermasterpb.PlacementMembership) (*placementMembershipView, error) {
	switch {
	case membership == nil || !membership.GetComplete():
		return nil, errors.New("placement membership is missing or incomplete")
	case membership.GetControlCellId() != m.cellID:
		return nil, fmt.Errorf("placement membership is for cell %q, not %q", membership.GetControlCellId(), m.cellID)
	case membership.GetRevision() == "":
		return nil, errors.New("placement membership has no revision")
	case membership.GetObservedAt() == nil || !membership.GetObservedAt().IsValid():
		return nil, errors.New("placement membership has no valid read instant")
	case len(membership.GetClusters()) > placementMembershipMaxClusters || len(membership.GetNodes()) > placementMembershipMaxNodes:
		return nil, fmt.Errorf("placement membership exceeds bounds: %d clusters, %d nodes", len(membership.GetClusters()), len(membership.GetNodes()))
	}
	view := &placementMembershipView{
		revision: membership.GetRevision(), observedAt: membership.GetObservedAt().AsTime(),
		active: make(map[string]bool, len(membership.GetClusters())), nodes: make(map[string][]*quartermasterpb.MediaPlacementInventoryNode),
	}
	for _, cluster := range membership.GetClusters() {
		id := cluster.GetClusterId()
		if id == "" {
			return nil, errors.New("placement membership lists a cluster without id")
		}
		if _, seen := view.active[id]; seen {
			return nil, fmt.Errorf("placement membership lists cluster %q twice", id)
		}
		view.active[id] = cluster.GetIsActive()
	}
	seenNodes := make(map[string]bool, len(membership.GetNodes()))
	for _, node := range membership.GetNodes() {
		if node.GetNodeId() == "" || seenNodes[node.GetNodeId()] {
			return nil, fmt.Errorf("placement membership node %q is empty or duplicated", node.GetNodeId())
		}
		if _, listed := view.active[node.GetClusterId()]; !listed {
			return nil, fmt.Errorf("placement membership node %q is in unlisted cluster %q", node.GetNodeId(), node.GetClusterId())
		}
		seenNodes[node.GetNodeId()] = true
		view.nodes[node.GetClusterId()] = append(view.nodes[node.GetClusterId()], node)
	}
	return view, nil
}
