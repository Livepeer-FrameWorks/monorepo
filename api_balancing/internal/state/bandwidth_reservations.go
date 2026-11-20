package state

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	goredis "github.com/redis/go-redis/v9"
)

type BandwidthDirection string

const (
	BandwidthEgress  BandwidthDirection = "EGRESS"
	BandwidthIngress BandwidthDirection = "INGRESS"
)

type bandwidthReservation struct {
	Bytes       uint64             `json:"bytes,string"`
	Direction   BandwidthDirection `json:"direction"`
	Stream      string             `json:"stream"`
	State       string             `json:"state"`
	ActivatedMS int64              `json:"activated_ms"`
	ExpiresMS   int64              `json:"expires_ms"`
	Replica     string             `json:"replica"`
	fallback    bool
}

type bandwidthSums struct {
	Egress   uint64 `json:"egress,string"`
	Ingress  uint64 `json:"ingress,string"`
	Revision uint64 `json:"revision,string"`
}

type bandwidthResult struct {
	Allowed bool                  `json:"allowed"`
	Sums    bandwidthSums         `json:"sums"`
	Entry   *bandwidthReservation `json:"entry"`
	Pending bool                  `json:"pending"`
	EventID string                `json:"event_id"`
}

type nodeBandwidthReservations struct {
	mu    sync.Mutex
	nodes map[string]*nodeBandwidthLedger
}

type nodeBandwidthLedger struct {
	mu           sync.Mutex
	entries      map[string]bandwidthReservation
	publishTimer *time.Timer
	fallback     bool
}

func newNodeBandwidthReservations() *nodeBandwidthReservations {
	return &nodeBandwidthReservations{nodes: make(map[string]*nodeBandwidthLedger)}
}

func (m *nodeBandwidthReservations) forNode(nodeID string) *nodeBandwidthLedger {
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.nodes[nodeID]
	if ledger == nil {
		ledger = &nodeBandwidthLedger{entries: make(map[string]bandwidthReservation)}
		m.nodes[nodeID] = ledger
	}
	return ledger
}

func (m *nodeBandwidthReservations) clearNode(nodeID string) {
	ledger := m.forNode(nodeID)
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	clear(ledger.entries)
	ledger.fallback = false
	if ledger.publishTimer != nil {
		ledger.publishTimer.Stop()
		ledger.publishTimer = nil
	}
	// Keep the lock identity stable for operations already waiting on this node.
}

//go:embed bandwidth_reservations.lua
var bandwidthScriptSource string
var bandwidthScript = goredis.NewScript(bandwidthScriptSource)

func (r *RedisStateStore) bandwidthKeys(nodeID string) []string {
	return []string{fmt.Sprintf("{%s}:bw_resv:%s", r.clusterID, nodeID), fmt.Sprintf("{%s}:bw_resv_amt:%s", r.clusterID, nodeID), r.changelog.Key(), fmt.Sprintf("{%s}:bw_resv_revision", r.clusterID), fmt.Sprintf("{%s}:bw_resv_held:%s", r.clusterID, nodeID)}
}

func (r *RedisStateStore) mutateBandwidth(ctx context.Context, op, id, nodeID, replica string, entry bandwidthReservation, link capacity.Link, cutoff int64, now time.Time) (bandwidthResult, error) {
	var result bandwidthResult
	data, err := json.Marshal(entry)
	if err != nil {
		return result, err
	}
	event, err := json.Marshal(StateChange{InstanceID: replica, Entity: StateEntityNodeReservation, Operation: StateOpUpsert, NodeID: nodeID})
	if err != nil {
		return result, err
	}
	raw, err := bandwidthScript.Run(ctx, r.client, r.bandwidthKeys(nodeID), op, id, now.UnixMilli(), string(data), link.Usable(), link.Observed, cutoff, string(event), r.changelog.MaxLen()).Text()
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(raw), &result)
	return result, err
}

func (m *nodeBandwidthLedger) memoryMutation(op, id, nodeID string, entry bandwidthReservation, link capacity.Link, cutoff int64, now time.Time) bandwidthResult {
	records := m.entries
	for key, r := range records {
		if r.ExpiresMS <= now.UnixMilli() || (op == "settle" && r.State == "HELD" && r.ActivatedMS < cutoff) {
			delete(records, key)
		}
	}
	sums := memoryBandwidthSums(records)
	current, exists := records[id]
	result := bandwidthResult{Allowed: true}
	switch op {
	case "reserve", "import":
		if exists {
			result.Allowed = current.Direction == entry.Direction && current.Stream == entry.Stream
		} else {
			if entry.Direction == BandwidthIngress {
				link.Reserved = sums.Ingress
			} else {
				link.Reserved = sums.Egress
			}
			result.Allowed = op == "import" || link.Admits(entry.Bytes)
			if result.Allowed {
				records[id] = entry
				current, exists = entry, true
			}
		}
	case "activate":
		result.Allowed = exists && (entry.Stream == "" || entry.Stream == current.Stream)
		if result.Allowed {
			current.fallback = current.fallback || entry.fallback
			if current.State == "PENDING" {
				current.State = "HELD"
				current.ActivatedMS = now.UnixMilli()
				current.ExpiresMS = now.Add(120 * time.Second).UnixMilli()
			}
			records[id] = current
		}
	case "release", "release_pending":
		if op == "release" || current.State == "PENDING" {
			delete(records, id)
			exists = false
		}
	}
	if exists {
		result.Entry = &current
	}
	result.Sums = memoryBandwidthSums(records)
	return result
}

func memoryBandwidthSums(records map[string]bandwidthReservation) bandwidthSums {
	var sums bandwidthSums
	for _, r := range records {
		if r.Direction == BandwidthIngress {
			sums.Ingress = saturatingAddBandwidth(sums.Ingress, r.Bytes)
		} else {
			sums.Egress = saturatingAddBandwidth(sums.Egress, r.Bytes)
		}
	}
	return sums
}

func (sm *StreamStateManager) bandwidthMutation(ctx context.Context, op, id, nodeID string, entry bandwidthReservation, cutoff int64, now time.Time) (bandwidthResult, error) {
	sm.mu.RLock()
	store, replica := sm.redisStore, sm.instanceID
	if _, evicted := sm.evictedNodes[nodeID]; evicted && op == "reserve" {
		sm.mu.RUnlock()
		return bandwidthResult{}, nil
	}
	n := sm.nodes[nodeID]
	link := capacity.Link{Budget: capacity.DefaultBudget}
	if n != nil {
		if entry.Direction == BandwidthIngress {
			link = n.IngressLink()
		} else {
			link = n.EgressLink()
		}
		if link.Budget == 0 {
			link.Budget = capacity.DefaultBudget
		}
	}
	sm.mu.RUnlock()
	entry.Replica = replica
	ledger := sm.bandwidthReservations.forNode(nodeID)
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	var result bandwidthResult
	var err error
	if store != nil {
		// Already-admitted fallback traffic is imported before admitting new work.
		if ledger.fallback {
			for key, r := range ledger.entries {
				if !r.fallback || r.ExpiresMS <= now.UnixMilli() {
					continue
				}
				if _, err = store.mutateBandwidth(ctx, "import", key, nodeID, replica, r, link, 0, now); err != nil {
					break
				}
				r.fallback = false
				ledger.entries[key] = r
			}
			if err == nil {
				ledger.fallback = false
			}
		}
		if err == nil {
			result, err = store.mutateBandwidth(ctx, op, id, nodeID, replica, entry, link, cutoff, now)
		}
	}
	if ctx.Err() != nil {
		return bandwidthResult{}, ctx.Err()
	}
	if store == nil || err != nil {
		if err != nil {
			logging.NewLogger().WithError(err).WithField("node_id", nodeID).Warn("Redis bandwidth reservations unavailable; using replica memory")
		}
		entry.fallback = true
		ledger.fallback = true
		result = ledger.memoryMutation(op, id, nodeID, entry, link, cutoff, now)
	} else {
		// Keep a local mirror for outages; Redis expiry and settlement also remove mirrored entries.
		if op == "settle" || op == "snapshot" {
			ledger.memoryMutation(op, id, nodeID, entry, link, cutoff, now)
		}
		if result.Entry != nil {
			ledger.entries[id] = *result.Entry
		} else if op == "release" || op == "release_pending" {
			delete(ledger.entries, id)
		}
		if result.EventID != "" {
			sm.watermarks.Record(stateChangeKey(StateChange{Entity: StateEntityNodeReservation, NodeID: nodeID}), result.EventID)
		}
		if result.Pending && ledger.publishTimer == nil {
			ledger.publishTimer = time.AfterFunc(250*time.Millisecond, func() {
				ledger.mu.Lock()
				ledger.publishTimer = nil
				ledger.mu.Unlock()
				select {
				case <-sm.stalenessChecker:
					return
				default:
				}
				publishCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, publishErr := sm.bandwidthMutation(publishCtx, "snapshot", "", nodeID, bandwidthReservation{}, 0, time.Now())
				reportBandwidthError("publish", nodeID, publishErr)
			})
		}
	}
	sm.applyBandwidthSums(nodeID, result.Sums, store == nil || err != nil)
	return result, nil
}

// ReserveNodeBandwidth makes an idempotent, cell-wide admission at the 85% cap.
func (sm *StreamStateManager) ReserveNodeBandwidth(ctx context.Context, id, nodeID, stream string, direction BandwidthDirection, bytes uint64) (bool, error) {
	result, err := sm.reserveNodeBandwidth(ctx, id, nodeID, stream, direction, bytes)
	return result.Allowed, err
}

func (sm *StreamStateManager) reserveNodeBandwidth(ctx context.Context, id, nodeID, stream string, direction BandwidthDirection, bytes uint64) (bandwidthResult, error) {
	if id == "" || nodeID == "" || bytes == 0 || (direction != BandwidthEgress && direction != BandwidthIngress) {
		return bandwidthResult{}, errors.New("invalid bandwidth reservation")
	}
	now := time.Now()
	return sm.bandwidthMutation(ctx, "reserve", id, nodeID, bandwidthReservation{Bytes: bytes, Direction: direction, Stream: stream, State: "PENDING", ExpiresMS: now.Add(30 * time.Second).UnixMilli()}, 0, now)
}

func (sm *StreamStateManager) ActivateNodeBandwidth(ctx context.Context, id, nodeID string) (bool, error) {
	result, err := sm.bandwidthMutation(ctx, "activate", id, nodeID, bandwidthReservation{}, 0, time.Now())
	return result.Allowed, err
}

func (sm *StreamStateManager) ReleaseNodeBandwidth(ctx context.Context, id, nodeID string) error {
	_, err := sm.bandwidthMutation(ctx, "release", id, nodeID, bandwidthReservation{}, 0, time.Now())
	return err
}

func (sm *StreamStateManager) applyBandwidthSums(nodeID string, sums bandwidthSums, fallback bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.applyBandwidthSumsLocked(nodeID, sums, fallback)
}

func (sm *StreamStateManager) applyBandwidthSumsLocked(nodeID string, sums bandwidthSums, fallback bool) {
	if _, evicted := sm.evictedNodes[nodeID]; evicted {
		return
	}
	n := sm.nodes[nodeID]
	if n == nil {
		return
	}
	if !fallback && sums.Revision < n.BandwidthReservationRevision {
		return
	}
	if !fallback {
		n.BandwidthReservationRevision = sums.Revision
	}
	n.AddBandwidth, n.IngressReservedBytesPerSec = sums.Egress, sums.Ingress
	sm.recomputeNodeScoresLocked(n)
}

// SettleNodeBandwidth releases only traffic contained in a complete measurement window.
func (sm *StreamStateManager) SettleNodeBandwidth(ctx context.Context, nodeID string, windowStartMS, windowEndMS int64, receivedAt time.Time) error {
	duration := 10 * time.Second
	if windowEndMS > windowStartMS && windowEndMS-windowStartMS >= 1000 && windowEndMS-windowStartMS <= 30000 {
		duration = time.Duration(windowEndMS-windowStartMS) * time.Millisecond
	}
	cutoff := receivedAt.Add(-duration - time.Second).UnixMilli()
	_, err := sm.bandwidthMutation(ctx, "settle", "", nodeID, bandwidthReservation{}, cutoff, receivedAt)
	return err
}

func (sm *StreamStateManager) runBandwidthReservationSweep() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-sm.stalenessChecker:
			return
		case <-ticker.C:
			sm.mu.RLock()
			store, replica := sm.redisStore, sm.instanceID
			nodes := make([]string, 0, len(sm.nodes))
			for id := range sm.nodes {
				nodes = append(nodes, id)
			}
			sm.mu.RUnlock()
			owners := map[string]ConnOwner{}
			if store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				observed, err := store.bandwidthSweepOwners(ctx, nodes)
				cancel()
				if err == nil {
					owners = observed
				}
			}
			for _, id := range nodes {
				if owner := owners[id]; owner.InstanceID != "" && owner.InstanceID != replica {
					continue
				}
				select {
				case <-sm.stalenessChecker:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, sweepErr := sm.bandwidthMutation(ctx, "snapshot", "", id, bandwidthReservation{}, 0, time.Now())
				reportBandwidthError("sweep", id, sweepErr)
				cancel()
			}
		}
	}
}

func (sm *StreamStateManager) EstimateStreamBandwidth(nodeID, stream string) uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.estimateStreamBandwidthLocked(nodeID, stream)
}

func (sm *StreamStateManager) estimateStreamBandwidthLocked(nodeID, stream string) uint64 {
	var top uint64
	if s := sm.streams[stream]; s != nil {
		for _, track := range s.Tracks {
			if track.Bitrate > 0 {
				top = max(top, uint64(track.Bitrate))
			}
		}
	}
	if top > 0 {
		return capacity.EstimateFromTopBitrateKbps(top)
	}
	if n := sm.nodes[nodeID]; n != nil {
		return sm.calculateEstBandwidthPerUserLocked(n)
	}
	return capacity.MinimumViewerEstimate
}

func (r *RedisStateStore) allBandwidthSums(ctx context.Context) (map[string]bandwidthSums, error) {
	prefix := fmt.Sprintf("{%s}:bw_resv_amt:", r.clusterID)
	result := make(map[string]bandwidthSums)
	iter := r.client.Scan(ctx, 0, prefix+"*", 200).Iterator()
	for iter.Next(ctx) {
		nodeID := strings.TrimPrefix(iter.Val(), prefix)
		value, err := r.mutateBandwidth(ctx, "snapshot", "", nodeID, "", bandwidthReservation{}, capacity.Link{}, 0, time.Now())
		if err != nil {
			return nil, err
		}
		result[nodeID] = value.Sums
	}
	return result, iter.Err()
}

// HoldNodeBandwidth admits traffic that starts immediately, such as an upstream pull.
func (sm *StreamStateManager) HoldNodeBandwidth(ctx context.Context, id, nodeID, stream string, direction BandwidthDirection, bytes uint64) (bool, error) {
	ok, err := sm.ReserveNodeBandwidth(ctx, id, nodeID, stream, direction, bytes)
	if err != nil || !ok {
		return false, err
	}
	return sm.ActivateNodeBandwidth(ctx, id, nodeID)
}

func (sm *StreamStateManager) ReleasePendingNodeBandwidth(ctx context.Context, id, nodeID string) error {
	_, err := sm.bandwidthMutation(ctx, "release_pending", id, nodeID, bandwidthReservation{}, 0, time.Now())
	return err
}

func reportBandwidthError(action, nodeID string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		logging.NewLogger().WithError(err).WithFields(logging.Fields{"node_id": nodeID, "action": action}).Warn("Could not update bandwidth reservations")
	}
}

func (r *RedisStateStore) bandwidthSweepOwners(ctx context.Context, nodes []string) (map[string]ConnOwner, error) {
	result := make(map[string]ConnOwner, len(nodes))
	for start := 0; start < len(nodes); start += 500 {
		end := min(start+500, len(nodes))
		keys := make([]string, 0, end-start)
		for _, id := range nodes[start:end] {
			keys = append(keys, r.keyConnOwner(id))
		}
		values, err := r.client.MGet(ctx, keys...).Result()
		if err != nil {
			return nil, err
		}
		for i, value := range values {
			if raw, ok := value.(string); ok {
				result[nodes[start+i]] = decodeConnOwner(raw)
			}
		}
	}
	return result, nil
}
