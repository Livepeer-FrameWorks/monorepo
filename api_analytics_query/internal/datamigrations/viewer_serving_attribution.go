package datamigrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbqueries "github.com/Livepeer-FrameWorks/monorepo/pkg/database/queries"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const ViewerServingAttributionID = "periscope_viewer_serving_attribution_v0_3_15"

type attributionCheckpoint struct {
	StreamID  string `json:"stream_id"`
	Timestamp int64  `json:"timestamp"`
	EventID   string `json:"event_id"`
}

const attributionLowerBound = `(stream_id, timestamp, event_id) > (toUUID(?), toDateTime(?), toUUID(?))`
const attributionUpperBound = `(stream_id, timestamp, event_id) <= (toUUID(?), toDateTime(?), toUUID(?))`

const activeAttributionIdentity = `(stream_id,node_id,session_id,source_timestamp_ms) IN (
 SELECT stream_id,node_id,session_id,assumeNotNull(lifecycle_connected_at_ms)
 FROM viewer_lifecycles WHERE lifecycle_open)`

func attributionLifecycleCTE() string {
	return "WITH " + dbqueries.ViewerLifecyclesCTE("tenant_id=toUUID(?) AND connected_at IS NOT NULL") + " "
}

// RegisterViewerServingAttribution binds the standard runner's state and leases
// to one logical dataset while retaining the catalog's stable migration ID.
func RegisterViewerServingAttribution(sourceID string, openClickHouse func() (database.ClickHouseConn, error)) {
	if strings.TrimSpace(sourceID) == "" {
		panic("viewer attribution migration requires a logical source ID")
	}
	datamigrate.Register(datamigrate.Migration{
		ID: ViewerServingAttributionID, StateNamespace: sourceID, Service: "periscope-metering", IntroducedIn: "v0.3.15",
		RequiredBeforePhase: "postdeploy",
		Description:         "retain trusted serving identity for open viewer lifecycles",
		Scopes: func(ctx context.Context, _ datamigrate.DB) ([]datamigrate.ScopeKey, error) {
			ch, err := openClickHouse()
			if err != nil {
				return nil, err
			}
			rows, err := ch.QueryContext(ctx, `WITH `+dbqueries.ViewerLifecyclesCTE("connected_at IS NOT NULL")+` SELECT DISTINCT toString(tenant_id) AS tenant FROM viewer_lifecycles WHERE lifecycle_open ORDER BY tenant`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var scopes []datamigrate.ScopeKey
			for rows.Next() {
				var tenantID string
				if err := rows.Scan(&tenantID); err != nil {
					return nil, err
				}
				scopes = append(scopes, datamigrate.ScopeKey{Kind: "tenant", Value: tenantID})
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			if len(scopes) == 0 {
				scopes = append(scopes, datamigrate.ScopeKey{Kind: "dataset", Value: sourceID})
			}
			return scopes, nil
		},
		Run: func(ctx context.Context, _ datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
			ch, err := openClickHouse()
			if err != nil {
				return datamigrate.Progress{}, err
			}
			if opts.Scope.Kind == "dataset" && opts.Scope.Value == sourceID {
				if err := verifyViewerServingAttribution(ctx, ch); err != nil {
					return datamigrate.Progress{}, err
				}
				return datamigrate.Progress{Done: true}, nil
			}
			return backfillViewerServingAttribution(ctx, ch, opts)
		},
		Verify: func(ctx context.Context, _ datamigrate.DB) error {
			ch, err := openClickHouse()
			if err != nil {
				return err
			}
			return verifyViewerServingAttribution(ctx, ch)
		},
	})
}

func backfillViewerServingAttribution(ctx context.Context, ch database.ClickHouseConn, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	if opts.Scope.Kind != "tenant" || opts.Scope.Value == "" {
		return datamigrate.Progress{}, fmt.Errorf("viewer attribution requires a tenant scope")
	}
	checkpoint := attributionCheckpoint{StreamID: "00000000-0000-0000-0000-000000000000", EventID: "00000000-0000-0000-0000-000000000000"}
	if len(opts.Checkpoint) > 0 {
		if err := json.Unmarshal(opts.Checkpoint, &checkpoint); err != nil {
			return datamigrate.Progress{}, err
		}
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	batchSize = min(batchSize, 10000)
	rows, err := ch.QueryContext(ctx, attributionLifecycleCTE()+`SELECT toString(stream_id), timestamp, toString(event_id)
		FROM periscope.viewer_connection_events WHERE tenant_id=toUUID(?)
		AND event_type='connect' AND session_id!='' AND cluster_id!='' AND `+activeAttributionIdentity+` AND `+attributionLowerBound+`
		ORDER BY stream_id,timestamp,event_id LIMIT ?`, opts.Scope.Value, opts.Scope.Value, checkpoint.StreamID, checkpoint.Timestamp, checkpoint.EventID, batchSize)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	defer rows.Close()
	var count int64
	upper := checkpoint
	for rows.Next() {
		var timestamp time.Time
		if scanErr := rows.Scan(&upper.StreamID, &timestamp, &upper.EventID); scanErr != nil {
			return datamigrate.Progress{}, scanErr
		}
		upper.Timestamp = timestamp.Unix()
		count++
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if rowErr != nil {
		return datamigrate.Progress{}, rowErr
	}
	if count > 0 && !opts.DryRun {
		_, err = ch.ExecContext(ctx, `INSERT INTO periscope.viewer_lifecycle_serving_attribution
			`+attributionLifecycleCTE()+`SELECT tenant_id,stream_id,node_id,session_id,source_timestamp_ms,
			       groupUniqArrayState(CAST(cluster_id AS String))
			FROM periscope.viewer_connection_events WHERE tenant_id=toUUID(?)
			AND event_type='connect' AND session_id!='' AND cluster_id!=''
			AND `+activeAttributionIdentity+` AND `+attributionLowerBound+` AND `+attributionUpperBound+`
			GROUP BY tenant_id,stream_id,node_id,session_id,source_timestamp_ms`,
			opts.Scope.Value, opts.Scope.Value, checkpoint.StreamID, checkpoint.Timestamp, checkpoint.EventID, upper.StreamID, upper.Timestamp, upper.EventID)
		if err != nil {
			return datamigrate.Progress{}, err
		}
	}
	encoded, err := json.Marshal(upper)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	changed := count
	if opts.DryRun {
		changed = 0
	}
	return datamigrate.Progress{Scanned: count, Changed: changed, Checkpoint: encoded, Done: count < int64(batchSize)}, nil
}

func verifyViewerServingAttribution(ctx context.Context, ch database.ClickHouseConn) error {
	schemaRows, err := ch.QueryContext(ctx, `SELECT tenant_id,stream_id,node_id,session_id,lifecycle_connected_at_ms,
		groupUniqArrayMerge(serving_clusters) FROM periscope.viewer_lifecycle_serving_attribution
		WHERE 0 GROUP BY tenant_id,stream_id,node_id,session_id,lifecycle_connected_at_ms`)
	if err != nil {
		return fmt.Errorf("serving attribution schema not ready: %w", err)
	}
	defer schemaRows.Close()
	if closeErr := schemaRows.Close(); closeErr != nil {
		return closeErr
	}
	rows, err := ch.QueryContext(ctx, `WITH `+dbqueries.ViewerLifecyclesCTE("connected_at IS NOT NULL")+` SELECT DISTINCT toString(tenant_id) AS tenant FROM viewer_lifecycles WHERE lifecycle_open ORDER BY tenant`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var tenantID string
		if scanErr := rows.Scan(&tenantID); scanErr != nil {
			return scanErr
		}
		tenants = append(tenants, tenantID)
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if rowErr != nil {
		return rowErr
	}
	for _, tenantID := range tenants {
		var missing uint64
		err = ch.QueryRowContext(ctx, attributionLifecycleCTE()+`SELECT count() FROM (
			SELECT stream_id,node_id,session_id,source_timestamp_ms,CAST(cluster_id AS String) AS cluster
			FROM periscope.viewer_connection_events WHERE tenant_id=toUUID(?)
			AND event_type='connect' AND session_id!='' AND cluster_id!='' AND `+activeAttributionIdentity+`
			EXCEPT DISTINCT
			SELECT stream_id,node_id,session_id,lifecycle_connected_at_ms,
			       arrayJoin(groupUniqArrayMerge(serving_clusters)) AS cluster
			FROM periscope.viewer_lifecycle_serving_attribution WHERE tenant_id=toUUID(?)
			GROUP BY stream_id,node_id,session_id,lifecycle_connected_at_ms
		)`, tenantID, tenantID, tenantID).Scan(&missing)
		if err != nil {
			return err
		}
		if missing != 0 {
			return fmt.Errorf("tenant %s has %d retained connect identities missing from attribution", tenantID, missing)
		}
	}
	return nil
}
