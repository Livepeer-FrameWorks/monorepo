package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	cellPlacementCapabilityRefreshReason = "cell_placement_capability_ready"
	// The probe is one ledger read at the cell's Foghorn and one write here.
	placementProbeTimeout = 10 * time.Second
	placementProbeQuiet   = 30 * time.Second
)

// cellPlacementCapabilityReady is the single predicate behind the activation
// barrier: a cell counts as capable only when its own Foghorn attested schema-2
// enforcement readiness. Stored rows never imply capability for other cells.
func cellPlacementCapabilityReady(maxSchemaVersion int32, enforcementReady bool) bool {
	return enforcementReady && maxSchemaVersion >= int32(sharedauthority.PlacementSchemaVersion)
}

// cellPlacementCapabilityAttested reports whether a cell's answer carries an
// attestation at all. A Foghorn whose ledger read failed answers without one,
// and a ledger without live replicas lacks even the answering replica's own
// heartbeat; neither says anything about what the cell enforces.
func cellPlacementCapabilityAttested(capability *foghornpb.MediaCellPlacementCapability) bool {
	return capability != nil && capability.GetLiveReplicas() > 0
}

// attestedCellPlacementCapability normalizes an attestation. A malformed one
// (an unknown schema version, an implausible replica count) is not ready.
func attestedCellPlacementCapability(capability *foghornpb.MediaCellPlacementCapability) (maxSchema int32, ready bool, replicas int32) {
	if capability == nil {
		return int32(sharedauthority.SchemaVersion), false, 0
	}
	versions := slices.Clone(capability.GetSupportedSchemaVersions())
	slices.Sort(versions)
	maxSchema = int32(sharedauthority.SchemaVersion)
	for _, version := range versions {
		if version == 0 || version > uint32(sharedauthority.PlacementSchemaVersion) {
			return int32(sharedauthority.SchemaVersion), false, 0
		}
		maxSchema = int32(version)
	}
	if capability.GetLiveReplicas() > 1<<20 {
		return int32(sharedauthority.SchemaVersion), false, 0
	}
	replicas = int32(capability.GetLiveReplicas())
	ready = capability.GetEnforcementReady() && replicas > 0 && maxSchema >= int32(sharedauthority.PlacementSchemaVersion)
	// Node placement is a flag on top of schema 2, not a listed version, so a
	// Foghorn that upgrades before Commodore never looks malformed to it.
	if ready && capability.GetNodePlacementReady() {
		maxSchema = int32(sharedauthority.NodePlacementSchemaVersion)
	}
	return maxSchema, ready, replicas
}

// recordCellPlacementCapability queues activation only when readiness advances.
// The attestation row lock serializes the activation marker and tenant enqueues
// in one transaction, including concurrent first acknowledgements.
//
// An answer without an attestation keeps the stored row: only an explicit
// attestation by live replicas withdraws readiness. Dropping readiness on a
// transient ledger read failure would hand new tenants the legacy schema, which
// the cell's enforcing replicas refuse.
func (s *CommodoreServer) recordCellPlacementCapability(ctx context.Context, cellID string, capability *foghornpb.MediaCellPlacementCapability) error {
	cellID = strings.TrimSpace(cellID)
	if cellID == "" || s == nil || s.db == nil {
		return errors.New("cell placement capability requires a cell and database")
	}
	if !cellPlacementCapabilityAttested(capability) {
		if s.logger != nil {
			s.logger.WithField("cell_id", cellID).Debug("Cell answered without a placement capability attestation; stored capability kept")
		}
		return nil
	}
	maxSchema, ready, replicas := attestedCellPlacementCapability(capability)
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		row, err := queries.UpsertMediaCellPlacementCapability(ctx, commodoredb.UpsertMediaCellPlacementCapabilityParams{
			CellID: cellID, MaxSchemaVersion: maxSchema, EnforcementReady: ready, LiveReplicas: replicas,
			// These two do not depend on placement enforcement: a cell that only
			// reads legacy-schema authorities still accepts long validity and
			// reports use.
			LongValidityReady: capability.GetLongValidityReady(),
			UseReportsReady:   capability.GetUseReportsReady(),
		})
		// The upsert skips an attestation identical to a recent one. The row it
		// leaves was written, and its activation decided, by the transaction
		// that last changed it.
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("record cell placement capability: %w", err)
		}
		if !row.EnforcementReady || !row.FirstReadyAt.Valid {
			return nil
		}
		// Advancing from schema 2 to 3 is a distinct activation even when the
		// cell never withdrew its enforcement readiness.
		listSchema, keySuffix := int32(sharedauthority.PlacementSchemaVersion), ""
		if maxSchema >= int32(sharedauthority.NodePlacementSchemaVersion) {
			listSchema, keySuffix = int32(sharedauthority.NodePlacementSchemaVersion), ":schema-3"
		}
		if row.ActivationSchemaVersion >= listSchema {
			return nil
		}
		tenants, err := queries.ListLegacySchemaTenantsTargetingCell(ctx, commodoredb.ListLegacySchemaTenantsTargetingCellParams{
			CellID: cellID, SchemaVersion: listSchema,
		})
		if err != nil {
			return fmt.Errorf("list legacy-schema tenants for cell %q: %w", cellID, err)
		}
		readiness := strconv.FormatInt(row.FirstReadyAt.Time.UTC().Unix(), 10)
		for _, tenantID := range tenants {
			if err := queries.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaAuthorityTarget(tenantID), tenantID,
				cellPlacementCapabilityRefreshReason, "commodore", "cell-capability:"+cellID+":"+tenantID+":"+readiness+keySuffix); err != nil {
				return fmt.Errorf("enqueue placement activation refresh for tenant %q: %w", tenantID, err)
			}
		}
		return queries.MarkMediaCellPlacementActivated(ctx, commodoredb.MarkMediaCellPlacementActivatedParams{
			CellID: cellID, SchemaVersion: listSchema,
		})
	})
}

// refreshPlacementCellCapabilities asks each target cell for an attestation
// before publication. Authority acknowledgements keep the stored capability
// fresh, while this path breaks the first-delivery dependency for a new cell.
func (s *CommodoreServer) refreshPlacementCellCapabilities(ctx context.Context, cells []string) error {
	failures := s.probePlacementCellCapabilities(ctx, cells)
	errs := make([]error, 0, len(failures))
	for _, cellID := range sortedUnique(cells) {
		if err := failures[cellID]; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// probePlacementCellCapabilities asks each cell for an attestation and records
// it, returning the error of every cell that could not be asked or recorded.
// A cell whose probe failed is not asked again for placementProbeQuiet and
// reports that failure instead: every legacy tenant targeting an unreachable
// cell retries its activation, and each retry would otherwise hold a refresh
// worker for up to placementProbeTimeout.
func (s *CommodoreServer) probePlacementCellCapabilities(ctx context.Context, cells []string) map[string]error {
	targets := sortedUnique(cells)
	results := make([]error, len(targets))
	var group errgroup.Group
	group.SetLimit(mediaAuthorityDeliveryWorkers)
	for i, cellID := range targets {
		if failure, ok := s.placementProbeFailures.Load(cellID); ok {
			if recent, typed := failure.(placementProbeFailure); typed && time.Since(recent.at) < placementProbeQuiet {
				results[i] = recent.err
				continue
			}
		}
		group.Go(func() error {
			probeCtx, cancel := context.WithTimeout(ctx, placementProbeTimeout)
			defer cancel()
			if err := s.probePlacementCellCapability(probeCtx, cellID); err != nil {
				results[i] = err
				if ctx.Err() == nil {
					s.placementProbeFailures.Store(cellID, placementProbeFailure{at: time.Now(), err: err})
				}
				return nil
			}
			s.placementProbeFailures.Delete(cellID)
			return nil
		})
	}
	_ = group.Wait() //nolint:errcheck // every probe reports through results
	failures := make(map[string]error)
	for i, cellID := range targets {
		if results[i] != nil {
			failures[cellID] = results[i]
		}
	}
	return failures
}

func (s *CommodoreServer) probePlacementCellCapability(ctx context.Context, cellID string) error {
	_, err := s.readBackPlacementCellCapability(ctx, cellID)
	return err
}

// readBackPlacementCellCapability asks the cell's own Foghorn for its
// attestation, records it, and returns what the cell answered.
func (s *CommodoreServer) readBackPlacementCellCapability(ctx context.Context, cellID string) (*foghornpb.MediaCellPlacementCapability, error) {
	client, err := s.resolveFoghornForClusterDirect(ctx, cellID)
	if err != nil {
		return nil, fmt.Errorf("resolve placement capability cell %q: %w", cellID, err)
	}
	capability, err := client.GetMediaCellPlacementCapability(ctx)
	if err != nil {
		return nil, fmt.Errorf("read placement capability for cell %q: %w", cellID, err)
	}
	// The cell answered, so deliveries that failed while it could not be
	// reached are retried now instead of after their backoff.
	if _, dueErr := s.makeCellMediaAuthorityDeliveriesDue(ctx, cellID); dueErr != nil && s.logger != nil {
		s.logger.WithError(dueErr).WithField("cell_id", cellID).Warn("Failed to make backed-off media authority deliveries due for a reachable cell")
	}
	if err := s.recordCellPlacementCapability(ctx, cellID, capability); err != nil {
		return nil, fmt.Errorf("persist placement capability for cell %q: %w", cellID, err)
	}
	return capability, nil
}

// ReportMediaCellPlacementCapability records a cell's attestation as soon as
// its Foghorn observes a change, instead of on the next acknowledgement. The
// service token is shared by every service and names no cell, so the report
// only prompts Commodore to read the attestation back from the named cell's
// Foghorn, which it resolves and dials itself.
func (s *CommodoreServer) ReportMediaCellPlacementCapability(ctx context.Context, req *commodorepb.ReportMediaCellPlacementCapabilityRequest) (*emptypb.Empty, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "placement capability report requires service authentication")
	}
	cellID := strings.TrimSpace(req.GetControlCellId())
	if cellID == "" || len(cellID) > 255 {
		return nil, status.Error(codes.InvalidArgument, "control_cell_id is required")
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "placement capability store is not configured")
	}
	readCtx, cancel := context.WithTimeout(ctx, placementProbeTimeout)
	defer cancel()
	if _, err := s.readBackPlacementCellCapability(readCtx, cellID); err != nil {
		// Never the cell's own code: the reporter reads Unimplemented as a
		// Commodore without this RPC, not as a cell without the probe.
		return nil, status.Errorf(codes.Unavailable, "read back placement capability: %v", err)
	}
	s.placementProbeFailures.Delete(cellID)
	return &emptypb.Empty{}, nil
}

type placementProbeFailure struct {
	at  time.Time
	err error
}

// placementProbeAnsweredUnsupported reports a probe the cell's Foghorn refused
// because its release has no capability probe, and so no placement enforcement.
func placementProbeAnsweredUnsupported(err error) bool {
	return status.Code(err) == codes.Unimplemented
}

// cellPlacementSchema is the highest placement schema a stored attestation lets
// Commodore issue to that cell, zero when it attests none.
func cellPlacementSchema(maxSchemaVersion int32, enforcementReady bool) uint32 {
	switch {
	case !cellPlacementCapabilityReady(maxSchemaVersion, enforcementReady):
		return 0
	case maxSchemaVersion >= int32(sharedauthority.NodePlacementSchemaVersion):
		return sharedauthority.NodePlacementSchemaVersion
	default:
		return sharedauthority.PlacementSchemaVersion
	}
}

// placementTargetSchema returns the highest placement schema every listed cell
// attests, zero when any cell attests none. An empty cell list is vacuously
// ready: a tenant with nobody to notify has nobody who could reject the schema,
// and any later target is checked again at publication.
func placementTargetSchema(ctx context.Context, queries *commodoredb.Queries, cells []string) (uint32, error) {
	attestations, err := readPlacementCellAttestations(ctx, queries, cells)
	if err != nil {
		return 0, err
	}
	return attestations.schema(), nil
}

// placementCellAttestations is the stored attestation of each listed cell; a
// cell without a row has attested nothing.
type placementCellAttestations struct {
	cells []string
	rows  map[string]commodoredb.ListMediaCellPlacementCapabilitiesRow
}

func readPlacementCellAttestations(ctx context.Context, queries *commodoredb.Queries, cells []string) (placementCellAttestations, error) {
	attestations := placementCellAttestations{rows: map[string]commodoredb.ListMediaCellPlacementCapabilitiesRow{}}
	for _, cell := range cells {
		if cell = strings.TrimSpace(cell); cell != "" && !slices.Contains(attestations.cells, cell) {
			attestations.cells = append(attestations.cells, cell)
		}
	}
	if len(attestations.cells) == 0 {
		return attestations, nil
	}
	rows, err := queries.ListMediaCellPlacementCapabilities(ctx, attestations.cells)
	if err != nil {
		return placementCellAttestations{}, fmt.Errorf("read cell placement capabilities: %w", err)
	}
	for _, row := range rows {
		attestations.rows[row.CellID] = row
	}
	return attestations, nil
}

func (a placementCellAttestations) cellSchema(cell string) uint32 {
	row, ok := a.rows[cell]
	if !ok {
		return 0
	}
	return cellPlacementSchema(row.MaxSchemaVersion, row.EnforcementReady)
}

// schema is the highest placement schema every cell attests, zero when any
// attests none.
func (a placementCellAttestations) schema() uint32 {
	result := uint32(sharedauthority.NodePlacementSchemaVersion)
	for _, cell := range a.cells {
		result = min(result, a.cellSchema(cell))
	}
	return result
}

// below lists the cells that do not attest schema.
func (a placementCellAttestations) below(schema uint32) []string {
	var cells []string
	for _, cell := range a.cells {
		if a.cellSchema(cell) < schema {
			cells = append(cells, cell)
		}
	}
	return cells
}

// awaiting lists the cells that do not attest schema yet but may: a cell whose
// probe failed for any reason other than its release lacking the probe, one
// that has attested nothing, and one whose Foghorn reads placement schema 2
// without enforcing it yet (replicas starting or still upgrading). A cell whose
// Foghorn answered that it reads no placement schema is not awaited; if it
// upgrades, its first ready attestation re-queues the tenants it holds back.
func (a placementCellAttestations) awaiting(schema uint32, probeFailures map[string]error) []string {
	var cells []string
	for _, cell := range a.below(schema) {
		if probeErr, failed := probeFailures[cell]; failed {
			if !placementProbeAnsweredUnsupported(probeErr) {
				cells = append(cells, cell)
			}
			continue
		}
		row, attested := a.rows[cell]
		if !attested || row.MaxSchemaVersion >= int32(sharedauthority.PlacementSchemaVersion) {
			cells = append(cells, cell)
		}
	}
	return cells
}

// placementCellsReady reports whether every listed cell has attested schema-2
// enforcement.
func placementCellsReady(ctx context.Context, queries *commodoredb.Queries, cells []string) (bool, error) {
	return placementCellsReadyFor(ctx, queries, cells, sharedauthority.PlacementSchemaVersion)
}

// placementCellsReadyFor reports whether every listed cell attests at least schema.
func placementCellsReadyFor(ctx context.Context, queries *commodoredb.Queries, cells []string, schema uint32) (bool, error) {
	attested, err := placementTargetSchema(ctx, queries, cells)
	if err != nil {
		return false, err
	}
	return attested >= schema, nil
}
