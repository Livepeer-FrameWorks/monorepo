package mediaauthority

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

const (
	// ReplicaHeartbeatInterval bounds how stale a replica's ledger row can be
	// while it is alive; ReplicaLivenessWindow is the reader's tolerance.
	ReplicaHeartbeatInterval = 15 * time.Second
	ReplicaLivenessWindow    = 60 * time.Second
	replicaLedgerRetention   = 24 * time.Hour
	// replicaAuthorityFeatureLevel is what this binary does with media authority
	// beyond placement. Level 1: accepts 30-day media-object authorities, fetches
	// an authority the cell does not hold, and reports the authorities it
	// decides on. The three ship together because Commodore stops keeping every
	// object in every cell only when a cell does all of them.
	replicaAuthorityFeatureLevel = 1
)

// placementEnforced is process state: startup sets it only after the public
// ingest/viewer resolvers, final viewer admission and prepared-source admission
// are all installed. It is never configurable and never cleared.
var placementEnforced atomic.Bool

// SetPlacementEnforced records that this replica enforces schema-2 placement.
func SetPlacementEnforced(ready bool) { placementEnforced.Store(ready) }

// PlacementEnforced reports whether this replica enforces schema-2 placement.
func PlacementEnforced() bool { return placementEnforced.Load() }

// ShadowComparable decides whether a connected admission response may promote
// the local signed pair. Legacy schema-1 pairs compare on legacy facts alone.
// Schema-2 pairs carry policy that connected responses never echo, so they are
// comparable only on a replica that enforces that policy itself.
func ShadowComparable(tenant *mediaauthoritypb.TenantAuthority, object *mediaauthoritypb.MediaObjectAuthority) bool {
	if sharedauthority.LegacyShadowComparable(tenant, object) {
		return true
	}
	return PlacementEnforced() && sharedauthority.PlacementShadowComparable(tenant, object)
}

// CellPlacementCapability is what this replica can attest about its cell from
// shared state: every live replica reads schema 2 and enforces placement.
type CellPlacementCapability struct {
	SupportedSchemaVersions []uint32
	EnforcementReady        bool
	LiveReplicas            int64
	NodePlacementReady      bool
	// Every live replica accepts 30-day media-object authorities, and every live
	// replica reports use. Both follow from the replicas' authority feature
	// level; they are separate on the wire because Commodore acts on them
	// separately.
	LongValidityReady bool
	UseReportsReady   bool
}

// CellPlacementCapability reads the replica ledger. A cell with no live rows,
// any live replica below schema 2, any non-enforcing replica, or a local
// process that has not itself installed enforcement is not ready. Node
// placement (schema 3) is attested only when every live replica heartbeats it:
// a replica on an older release rejects node selectors as unknown fields. It is
// a separate flag, never a supported version, because Commodore releases
// without node placement reject version 3 and would mark the cell not ready.
func (s *Store) CellPlacementCapability(ctx context.Context) (CellPlacementCapability, error) {
	if s == nil || s.db == nil {
		return CellPlacementCapability{}, errors.New("media authority store is unavailable")
	}
	row, err := foghorndb.New(s.db).ReadControlCellPlacementCapability(ctx, int32(ReplicaLivenessWindow/time.Second))
	if err != nil {
		return CellPlacementCapability{}, err
	}
	capability := CellPlacementCapability{
		SupportedSchemaVersions: []uint32{sharedauthority.SchemaVersion, sharedauthority.PlacementSchemaVersion},
		LiveReplicas:            row.LiveReplicas,
		NodePlacementReady:      row.LiveReplicas > 0 && row.MinSchemaVersion >= int32(sharedauthority.NodePlacementSchemaVersion),
		LongValidityReady:       row.LiveReplicas > 0 && row.MinAuthorityFeatureLevel >= 1,
		UseReportsReady:         row.LiveReplicas > 0 && row.MinAuthorityFeatureLevel >= 1,
	}
	capability.EnforcementReady = PlacementEnforced() && row.LiveReplicas > 0 && row.AllEnforced &&
		row.MinSchemaVersion >= int32(sharedauthority.PlacementSchemaVersion)
	return capability, nil
}

// RecordReplicaHeartbeat writes this replica's current capability into the
// shared ledger. The enforcement flag is read at write time, so a replica that
// installs enforcement after its first heartbeat corrects itself on the next.
func (s *Store) RecordReplicaHeartbeat(ctx context.Context, replicaID, release string) error {
	if s == nil || s.db == nil {
		return errors.New("media authority store is unavailable")
	}
	replicaID = strings.TrimSpace(replicaID)
	if replicaID == "" || len(replicaID) > 255 {
		return errors.New("replica identity is required")
	}
	return foghorndb.New(s.db).UpsertControlReplicaHeartbeat(ctx, foghorndb.UpsertControlReplicaHeartbeatParams{
		ReplicaID: replicaID, ReleaseVersion: strings.TrimSpace(release),
		PlacementSchemaVersion: int32(sharedauthority.NodePlacementSchemaVersion), PlacementEnforced: PlacementEnforced(),
		AuthorityFeatureLevel: replicaAuthorityFeatureLevel,
	})
}

// RunReplicaHeartbeat writes immediately, then on every interval until ctx ends.
// Stale rows older than the retention window are pruned opportunistically; a
// row inside the liveness window but past its heartbeat only delays attestation.
func (s *Store) RunReplicaHeartbeat(ctx context.Context, replicaID, release string, logger logging.Logger) {
	ticker := time.NewTicker(ReplicaHeartbeatInterval)
	defer ticker.Stop()
	beat := func() {
		beatCtx, cancel := context.WithTimeout(ctx, ReplicaHeartbeatInterval)
		defer cancel()
		if err := s.RecordReplicaHeartbeat(beatCtx, replicaID, release); err != nil && logger != nil {
			logger.WithError(err).Warn("Control replica heartbeat failed; cell placement attestation may lapse")
		}
	}
	beat()
	pruneEvery := replicaLedgerRetention / ReplicaHeartbeatInterval
	var ticks time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			beat()
			ticks++
			if ticks%pruneEvery != 0 {
				continue
			}
			pruneCtx, cancel := context.WithTimeout(ctx, ReplicaHeartbeatInterval)
			_, err := foghorndb.New(s.db).DeleteStaleControlReplicas(pruneCtx, int32(replicaLedgerRetention/time.Second))
			cancel()
			if err != nil && logger != nil {
				logger.WithError(err).Warn("Control replica ledger prune failed")
			}
		}
	}
}

// promotePlacementReadiness marks a placement-schema authority locally ready on
// apply. Commodore only issues schema 2 or 3 to a cell after that cell attested it,
// so an enforcing replica has already proven what shadow comparison proves for
// schema 1. Promotion at apply avoids the bootstrap deadlock where routing needs a
// ready pair before any connected admission could have compared it.
func promotePlacementReadiness(ctx context.Context, queries *foghorndb.Queries, verified *sharedauthority.Verified) error {
	if !PlacementEnforced() || verified == nil || !sharedauthority.IsPlacementSchema(verifiedPlacementSchema(verified)) {
		return nil
	}
	version := int64(verified.Envelope.GetAuthorityVersion())
	if tenant := verified.Tenant; tenant != nil {
		tenantID := tenant.GetTenantId()
		if _, err := queries.MarkTenantAuthorityLocalReadReady(ctx, foghorndb.MarkTenantAuthorityLocalReadReadyParams{TenantID: tenantID, AuthorityVersion: version}); err != nil {
			return err
		}
		if _, err := queries.MarkTenantAuthorityLocalIngestReady(ctx, foghorndb.MarkTenantAuthorityLocalIngestReadyParams{TenantID: tenantID, AuthorityVersion: version}); err != nil {
			return err
		}
		_, err := queries.MarkTenantAuthorityLocalSourceReady(ctx, foghorndb.MarkTenantAuthorityLocalSourceReadyParams{TenantID: tenantID, AuthorityVersion: version})
		return err
	}
	object := verified.MediaObject
	if object == nil {
		return nil
	}
	tenantID, authorityID := object.GetTenantId(), verified.Envelope.GetAuthorityId()
	if _, err := queries.MarkMediaObjectAuthorityLocalReadReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalReadReadyParams{TenantID: tenantID, AuthorityID: authorityID, AuthorityVersion: version}); err != nil {
		return err
	}
	if _, err := queries.MarkMediaObjectAuthorityLocalIngestReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalIngestReadyParams{TenantID: tenantID, AuthorityID: authorityID, AuthorityVersion: version}); err != nil {
		return err
	}
	_, err := queries.MarkMediaObjectAuthorityLocalSourceReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalSourceReadyParams{TenantID: tenantID, AuthorityID: authorityID, AuthorityVersion: version})
	return err
}
