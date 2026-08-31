package configseedackoutbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Client is Navigator's ACK projection surface. Delivery is deliberately
// outside the Helmsman control stream once Writer has persisted the request.
type Client interface {
	ReportConfigSeedApplyResult(context.Context, *dnspb.ReportConfigSeedApplyResultRequest) (*dnspb.ReportConfigSeedApplyResultResponse, error)
}

// Metrics exposes the outbox backlog and its bounded delivery outcomes.
type Metrics struct {
	Pending              *prometheus.GaugeVec
	OldestPendingSeconds *prometheus.GaugeVec
	Quarantined          *prometheus.GaugeVec
	Outcomes             *prometheus.CounterVec
}

// Writer is the media-local durability boundary for ConfigSeed apply results.
type Writer struct {
	db      *sql.DB
	metrics *Metrics
}

func NewWriter(db *sql.DB, metrics ...*Metrics) *Writer {
	return &Writer{db: db, metrics: firstMetrics(metrics)}
}

func (w *Writer) Enqueue(ctx context.Context, nodeID, clusterID string, ack *ipcpb.ConfigSeedApplyResult) error {
	if w == nil || w.db == nil {
		return errors.New("config-seed apply ACK outbox is unavailable")
	}
	nodeID = strings.TrimSpace(nodeID)
	clusterID = strings.TrimSpace(clusterID)
	if nodeID == "" || clusterID == "" || ack == nil || ack.GetSeedVersion() == 0 || ack.GetSeedVersion() > math.MaxInt64 {
		return errors.New("config-seed apply ACK requires node, cluster, and seed version")
	}
	appliedAt := time.Now().Unix()
	if ack.GetAppliedAt() != nil {
		appliedAt = ack.GetAppliedAt().AsTime().Unix()
	}
	req := &dnspb.ReportConfigSeedApplyResultRequest{
		NodeId: nodeID, ClusterId: clusterID, SeedVersion: ack.GetSeedVersion(),
		AppliedBundleIds: append([]string(nil), ack.GetAppliedBundleIds()...),
		FailedBundleIds:  append([]string(nil), ack.GetFailedBundleIds()...),
		Success:          ack.GetSuccess(), Error: ack.GetError(), AppliedAt: appliedAt,
		BundleVersions: cloneStringMap(ack.GetBundleVersions()),
	}
	sort.Strings(req.AppliedBundleIds)
	sort.Strings(req.FailedBundleIds)
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		w.observe("enqueue_error")
		return fmt.Errorf("encode config-seed apply ACK: %w", err)
	}
	signature, err := resultSignature(req.Success, req.AppliedBundleIds, req.FailedBundleIds, req.BundleVersions)
	if err != nil {
		w.observe("enqueue_error")
		return fmt.Errorf("sign config-seed apply ACK: %w", err)
	}
	rows, err := foghorndb.New(w.db).EnqueueConfigSeedApplyAck(ctx, foghorndb.EnqueueConfigSeedApplyAckParams{
		NodeID: nodeID, ClusterID: clusterID, SeedVersion: int64(ack.GetSeedVersion()), RequestPayload: payload, ResultSignature: signature,
	})
	if err != nil {
		w.observe("enqueue_error")
		return err
	}
	if rows == 0 {
		currentSeed, classifyErr := foghorndb.New(w.db).GetConfigSeedApplyAckSeedVersion(ctx, nodeID)
		if classifyErr == nil && currentSeed > int64(ack.GetSeedVersion()) {
			w.observe("stale")
		} else {
			w.observe("deduplicated")
		}
	} else {
		w.observe("enqueued")
	}
	return err
}

// resultSignature covers exactly the fields that change Navigator's per-bundle
// projection. Observation time and diagnostic text remain in the delivered
// payload, but cannot turn a replay of the same outcome into a new revision.
func resultSignature(success bool, appliedBundleIDs, failedBundleIDs []string, bundleVersions map[string]string) ([]byte, error) {
	projection := &dnspb.ReportConfigSeedApplyResultRequest{
		Success:          success,
		AppliedBundleIds: append([]string(nil), appliedBundleIDs...),
		FailedBundleIds:  append([]string(nil), failedBundleIDs...),
		BundleVersions:   cloneStringMap(bundleVersions),
	}
	sort.Strings(projection.AppliedBundleIds)
	sort.Strings(projection.FailedBundleIds)
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(projection)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func (w *Writer) observe(outcome string) {
	if w != nil && w.metrics != nil && w.metrics.Outcomes != nil {
		w.metrics.Outcomes.WithLabelValues(outcome).Inc()
	}
}

type Worker struct {
	db         *sql.DB
	client     Client
	logger     logging.Logger
	interval   time.Duration
	leaseOwner string
	metrics    *Metrics
}

func NewWorker(db *sql.DB, client Client, logger logging.Logger, metrics ...*Metrics) *Worker {
	return &Worker{db: db, client: client, logger: logger, interval: 5 * time.Second, leaseOwner: uuid.NewString(), metrics: firstMetrics(metrics)}
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.db == nil || w.client == nil {
		return
	}
	w.drain(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	queries := foghorndb.New(w.db)
	lease := sql.NullString{String: w.leaseOwner, Valid: true}
	rows, err := queries.ClaimDueConfigSeedApplyAcks(ctx, foghorndb.ClaimDueConfigSeedApplyAcksParams{LeaseOwner: lease, BatchSize: 32})
	if err != nil {
		w.observe("scan_error")
		w.logger.WithError(err).Warn("Failed to load durable ConfigSeed apply ACKs")
		return
	}
	var group sync.WaitGroup
	for _, row := range rows {
		row := row
		group.Add(1)
		go func() {
			defer group.Done()
			w.deliver(ctx, lease, row)
		}()
	}
	group.Wait()
}

func (w *Worker) deliver(ctx context.Context, lease sql.NullString, row foghorndb.ClaimDueConfigSeedApplyAcksRow) {
	queries := foghorndb.New(w.db)
	req := &dnspb.ReportConfigSeedApplyResultRequest{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.RequestPayload, req); err != nil {
		w.quarantine(ctx, queries, lease, row.ID, row.Revision, row.NodeID, fmt.Errorf("decode ACK: %w", err))
		return
	}
	if req.GetNodeId() != row.NodeID || req.GetClusterId() != row.ClusterID || req.GetSeedVersion() != uint64(row.SeedVersion) {
		w.quarantine(ctx, queries, lease, row.ID, row.Revision, row.NodeID, errors.New("persisted config-seed apply ACK identity/version mismatch"))
		return
	}
	signature, err := resultSignature(req.GetSuccess(), req.GetAppliedBundleIds(), req.GetFailedBundleIds(), req.GetBundleVersions())
	if err != nil || !bytes.Equal(signature, row.ResultSignature) {
		if err == nil {
			err = errors.New("persisted config-seed apply ACK result signature mismatch")
		} else {
			err = fmt.Errorf("recompute config-seed apply ACK result signature: %w", err)
		}
		w.quarantine(ctx, queries, lease, row.ID, row.Revision, row.NodeID, err)
		return
	}
	req.DeliverySequence = uint64(row.Revision)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	resp, err := w.client.ReportConfigSeedApplyResult(callCtx, req)
	cancel()
	if err == nil && (resp == nil || !resp.GetAccepted()) {
		err = errors.New("navigator did not accept ConfigSeed apply ACK")
	}
	if err != nil {
		// InvalidArgument is Navigator's terminal classification for a
		// malformed delivery (e.g. a missing cluster identity): replaying the
		// identical payload can never succeed, so quarantine instead of
		// retrying forever. A later ACK at the same or newer seed repairs it.
		if status.Code(err) == codes.InvalidArgument {
			w.quarantine(ctx, queries, lease, row.ID, row.Revision, row.NodeID, fmt.Errorf("navigator rejected ACK as invalid: %w", err))
			return
		}
		w.retry(ctx, queries, lease, row.ID, row.Revision, row.NodeID, err)
		return
	}
	settled, err := queries.SettleDeliveredConfigSeedApplyAck(ctx, foghorndb.SettleDeliveredConfigSeedApplyAckParams{ID: row.ID, Revision: row.Revision, LeaseOwner: lease})
	if err != nil {
		w.observe("settle_error")
		w.logger.WithError(err).WithField("node_id", row.NodeID).Warn("Failed to settle ConfigSeed apply ACK")
	} else if settled == 0 {
		w.observe("superseded")
		w.release(ctx, queries, lease, row.ID, row.NodeID)
	} else {
		w.observe("delivered")
	}
}

func (w *Worker) quarantine(ctx context.Context, queries *foghorndb.Queries, lease sql.NullString, id, revision int64, nodeID string, invalidErr error) {
	settled, err := queries.QuarantineInvalidConfigSeedApplyAck(ctx, foghorndb.QuarantineInvalidConfigSeedApplyAckParams{
		ID: id, Revision: revision, LeaseOwner: lease, LastError: sql.NullString{String: invalidErr.Error(), Valid: true},
	})
	if err != nil {
		w.observe("quarantine_error")
		w.logger.WithError(err).WithField("node_id", nodeID).Error("Failed to quarantine invalid ConfigSeed apply ACK")
		return
	}
	if settled == 0 {
		w.observe("superseded")
		w.release(ctx, queries, lease, id, nodeID)
		return
	}
	w.observe("quarantined")
	w.logger.WithError(invalidErr).WithField("node_id", nodeID).Error("Quarantined invalid ConfigSeed apply ACK")
}

func (w *Worker) retry(ctx context.Context, queries *foghorndb.Queries, lease sql.NullString, id, revision int64, nodeID string, deliveryErr error) {
	settled, err := queries.RetryConfigSeedApplyAck(ctx, foghorndb.RetryConfigSeedApplyAckParams{
		ID: id, Revision: revision, LeaseOwner: lease, LastError: sql.NullString{String: deliveryErr.Error(), Valid: true},
	})
	if err != nil {
		w.observe("retry_error")
		w.logger.WithError(err).WithField("node_id", nodeID).Error("Failed to reschedule ConfigSeed apply ACK")
	} else if settled == 0 {
		w.observe("superseded")
		w.release(ctx, queries, lease, id, nodeID)
	} else {
		w.observe("retry")
	}
	w.logger.WithError(deliveryErr).WithField("node_id", nodeID).Warn("ConfigSeed apply ACK remains pending")
}

func (w *Worker) observe(outcome string) {
	if w != nil && w.metrics != nil && w.metrics.Outcomes != nil {
		w.metrics.Outcomes.WithLabelValues(outcome).Inc()
	}
}

func firstMetrics(metrics []*Metrics) *Metrics {
	if len(metrics) == 0 {
		return nil
	}
	return metrics[0]
}

// RunMetrics observes durable backlog independently from Navigator delivery,
// including when Navigator is intentionally unconfigured or unavailable.
func RunMetrics(ctx context.Context, db *sql.DB, metrics *Metrics, logger logging.Logger) {
	if db == nil || metrics == nil {
		return
	}
	observe := func() {
		stats, err := foghorndb.New(db).GetConfigSeedApplyAckOutboxStats(ctx)
		if err != nil {
			if metrics.Outcomes != nil {
				metrics.Outcomes.WithLabelValues("scan_error").Inc()
			}
			logger.WithError(err).Warn("Failed to observe ConfigSeed apply ACK outbox")
			return
		}
		if metrics.Pending != nil {
			metrics.Pending.WithLabelValues().Set(float64(stats.Pending))
		}
		if metrics.OldestPendingSeconds != nil {
			metrics.OldestPendingSeconds.WithLabelValues().Set(stats.OldestPendingSeconds)
		}
		if metrics.Quarantined != nil {
			metrics.Quarantined.WithLabelValues().Set(float64(stats.Quarantined))
		}
	}
	observe()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			observe()
		}
	}
}

func (w *Worker) release(ctx context.Context, queries *foghorndb.Queries, lease sql.NullString, id int64, nodeID string) {
	if _, err := queries.ReleaseConfigSeedApplyAckLease(ctx, foghorndb.ReleaseConfigSeedApplyAckLeaseParams{ID: id, LeaseOwner: lease}); err != nil {
		w.logger.WithError(err).WithField("node_id", nodeID).Warn("Failed to release superseded ConfigSeed apply ACK lease")
	}
}
