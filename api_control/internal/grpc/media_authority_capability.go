package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"golang.org/x/sync/errgroup"
)

const cellPlacementCapabilityRefreshReason = "cell_placement_capability_ready"

// cellPlacementCapabilityReady is the single predicate behind the activation
// barrier: a cell counts as capable only when its own Foghorn attested schema-2
// enforcement readiness. Stored rows never imply capability for other cells.
func cellPlacementCapabilityReady(maxSchemaVersion int32, enforcementReady bool) bool {
	return enforcementReady && maxSchemaVersion >= int32(sharedauthority.PlacementSchemaVersion)
}

// attestedCellPlacementCapability normalizes an acknowledgement's attestation.
// Absent or malformed attestations are recorded as not ready, never ignored,
// so a cell that stops attesting cannot keep an earlier readiness on file.
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

// recordCellPlacementCapability persists one cell's attestation and, whenever
// the cell is ready, enqueues a refresh for every tenant that cell still holds
// on a pre-placement schema. The inbox key includes the cell's first-ready
// time, so repeated acknowledgements collapse into one compile per readiness.
func (s *CommodoreServer) recordCellPlacementCapability(ctx context.Context, cellID string, capability *foghornpb.MediaCellPlacementCapability) error {
	cellID = strings.TrimSpace(cellID)
	if cellID == "" || s == nil || s.db == nil {
		return errors.New("cell placement capability requires a cell and database")
	}
	maxSchema, ready, replicas := attestedCellPlacementCapability(capability)
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		row, err := queries.UpsertMediaCellPlacementCapability(ctx, commodoredb.UpsertMediaCellPlacementCapabilityParams{
			CellID: cellID, MaxSchemaVersion: maxSchema, EnforcementReady: ready, LiveReplicas: replicas,
		})
		if err != nil {
			return fmt.Errorf("record cell placement capability: %w", err)
		}
		if !row.EnforcementReady || !row.FirstReadyAt.Valid {
			return nil
		}
		// A cell that attests node placement also refreshes tenants still below
		// schema 3. first_ready_at does not move when an already ready cell starts
		// attesting schema 3, so that readiness carries its own inbox key.
		listSchema, keySuffix := int32(sharedauthority.PlacementSchemaVersion), ""
		if maxSchema >= int32(sharedauthority.NodePlacementSchemaVersion) {
			listSchema, keySuffix = int32(sharedauthority.NodePlacementSchemaVersion), ":schema-3"
		}
		tenants, err := queries.ListLegacySchemaTenantsTargetingCell(ctx, commodoredb.ListLegacySchemaTenantsTargetingCellParams{
			CellID: cellID, SchemaVersion: listSchema,
		})
		if err != nil {
			return fmt.Errorf("list legacy-schema tenants for cell %q: %w", cellID, err)
		}
		readiness := strconv.FormatInt(row.FirstReadyAt.Time.UTC().Unix(), 10)
		for _, tenantID := range tenants {
			if _, err := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
				SourceService: "commodore", SourceEventID: "cell-capability:" + cellID + ":" + tenantID + ":" + readiness + keySuffix,
				TenantID: tenantID, Reason: cellPlacementCapabilityRefreshReason,
			}); err != nil {
				return fmt.Errorf("enqueue placement activation refresh for tenant %q: %w", tenantID, err)
			}
		}
		return nil
	})
}

// refreshPlacementCellCapabilities asks each target cell for an attestation
// before publication. Authority acknowledgements keep the stored capability
// fresh, while this path breaks the first-delivery dependency for a new cell.
func (s *CommodoreServer) refreshPlacementCellCapabilities(ctx context.Context, cells []string) error {
	targets := sortedUnique(cells)
	var group errgroup.Group
	group.SetLimit(mediaAuthorityDeliveryWorkers)
	for _, cellID := range targets {
		cellID := cellID
		group.Go(func() error {
			client, err := s.resolveFoghornForClusterDirect(ctx, cellID)
			if err != nil {
				return fmt.Errorf("resolve placement capability cell %q: %w", cellID, err)
			}
			capability, err := client.GetMediaCellPlacementCapability(ctx)
			if err != nil {
				return fmt.Errorf("read placement capability for cell %q: %w", cellID, err)
			}
			if err := s.recordCellPlacementCapability(ctx, cellID, capability); err != nil {
				return fmt.Errorf("persist placement capability for cell %q: %w", cellID, err)
			}
			return nil
		})
	}
	return group.Wait()
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
	wanted := make([]string, 0, len(cells))
	for _, cell := range cells {
		if cell = strings.TrimSpace(cell); cell != "" && !slices.Contains(wanted, cell) {
			wanted = append(wanted, cell)
		}
	}
	if len(wanted) == 0 {
		return sharedauthority.NodePlacementSchemaVersion, nil
	}
	rows, err := queries.ListMediaCellPlacementCapabilities(ctx, wanted)
	if err != nil {
		return 0, fmt.Errorf("read cell placement capabilities: %w", err)
	}
	schemas := make(map[string]uint32, len(rows))
	for _, row := range rows {
		schemas[row.CellID] = cellPlacementSchema(row.MaxSchemaVersion, row.EnforcementReady)
	}
	result := uint32(sharedauthority.NodePlacementSchemaVersion)
	for _, cell := range wanted {
		result = min(result, schemas[cell])
	}
	return result, nil
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
