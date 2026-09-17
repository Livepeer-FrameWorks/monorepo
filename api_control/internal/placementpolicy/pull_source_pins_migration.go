package placementpolicy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/lib/pq"
)

const PullSourcePinsToStreamRulesID = "commodore_pull_source_pins_to_stream_rules_v0_3_8"

const zeroStreamID = "00000000-0000-0000-0000-000000000000"

// PullSourcePinCandidatesSQL lists live streams whose source carries a legacy
// cluster pin, in stream ID order after a checkpoint.
const PullSourcePinCandidatesSQL = `
SELECT stream.id::text, stream.tenant_id::text,
       CASE WHEN stream.ingest_mode = 'pull' THEN pull.allowed_cluster_ids ELSE mist.allowed_cluster_ids END
FROM commodore.streams AS stream
LEFT JOIN commodore.stream_pull_sources AS pull
  ON pull.stream_id = stream.id AND stream.ingest_mode = 'pull'
LEFT JOIN commodore.stream_mist_sources AS mist
  ON mist.stream_id = stream.id AND stream.ingest_mode = 'mist_native'
WHERE stream.deleted_at IS NULL
  AND stream.id > $1::uuid
  AND cardinality(COALESCE(
      CASE WHEN stream.ingest_mode = 'pull' THEN pull.allowed_cluster_ids ELSE mist.allowed_cluster_ids END,
      '{}'::text[])) > 0
ORDER BY stream.id
LIMIT $2`

type pinCandidate struct {
	streamID string
	tenantID string
	pins     []string
}

type pullSourcePinsCheckpoint struct {
	AfterStreamID string `json:"after_stream_id,omitempty"`
}

// RegisterPullSourcePinsToStreamRules registers the pin conversion with the
// Commodore data-migration registry. It lives beside the placement store it
// writes through; the generic datamigrations package is imported by
// commodoredb query contracts and cannot depend on the store.
func RegisterPullSourcePinsToStreamRules() {
	datamigrate.Register(datamigrate.Migration{
		ID: PullSourcePinsToStreamRulesID, Service: "commodore", IntroducedIn: "v0.3.8",
		RequiredBeforePhase: "contract",
		Description:         "convert per-source pull and managed-stream cluster pins into each stream's own ingest placement rules",
		Run:                 RunPullSourcePinsToStreamRules,
		Verify:              VerifyPullSourcePinsToStreamRules,
	})
}

// RunPullSourcePinsToStreamRules converts each pinned stream in its own
// transaction through the system apply path, so every conversion carries a
// receipt and an authority refresh. A dry run receives a read-only transaction
// and only counts streams whose rules would change.
func RunPullSourcePinsToStreamRules(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}
	checkpoint := pullSourcePinsCheckpoint{}
	if len(opts.Checkpoint) > 0 && string(opts.Checkpoint) != "{}" {
		if err := json.Unmarshal(opts.Checkpoint, &checkpoint); err != nil {
			return datamigrate.Progress{}, fmt.Errorf("decode pull-source pin checkpoint: %w", err)
		}
	}
	after := checkpoint.AfterStreamID
	if after == "" {
		after = zeroStreamID
	}
	candidates, err := listPinCandidates(ctx, db, after, batchSize)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	progress := datamigrate.Progress{Done: len(candidates) < batchSize}
	for _, candidate := range candidates {
		progress.Scanned++
		changed, convertErr := convertStreamPins(ctx, db, candidate, opts.DryRun)
		switch {
		case errors.Is(convertErr, ErrNotFound):
			progress.Skipped++
		case convertErr != nil:
			return progress, fmt.Errorf("convert pins for stream %s: %w", candidate.streamID, convertErr)
		case changed:
			progress.Changed++
		default:
			progress.Skipped++
		}
		checkpoint.AfterStreamID = candidate.streamID
	}
	if progress.Checkpoint, err = json.Marshal(checkpoint); err != nil {
		return progress, fmt.Errorf("encode pull-source pin checkpoint: %w", err)
	}
	return progress, nil
}

// convertStreamPins derives the update from the stream's pins as they stand in
// the conversion transaction, never from the listed candidate: a source-location
// edit that commits between listing and conversion rewrites both the pins and
// the own ingest rules, and the listed pins no longer describe that stream.
func convertStreamPins(ctx context.Context, db datamigrate.DB, candidate pinCandidate, dryRun bool) (bool, error) {
	scope := Scope{TenantID: candidate.tenantID, Kind: "stream", ID: candidate.streamID}
	if dryRun {
		exec, ok := db.(commodoredb.DBTX)
		if !ok {
			return false, errors.New("dry run requires a query executor")
		}
		pins, err := currentStreamPins(ctx, commodoredb.New(exec), candidate, false)
		if err != nil || len(pins) == 0 {
			return false, err
		}
		policies, err := StreamPolicies(ctx, exec, candidate.tenantID, []string{candidate.streamID})
		if err != nil {
			return false, err
		}
		updates, err := PinsUpdate(policies[candidate.streamID], pins)
		return len(updates) != 0, err
	}
	handle, ok := db.(*sql.DB)
	if !ok {
		return false, errors.New("pin conversion requires a database handle")
	}
	var changed bool
	err := database.WithRetryablePostgresTx(ctx, handle, nil, func(tx *sql.Tx) error {
		result, applyErr := ApplySystem(ctx, tx, SystemApplyInput{
			Scope: scope, ActorID: SystemActorDataMigration,
			// Runs after ApplySystem holds the placement locks, the order a stream
			// update takes them before it writes the pin column.
			Update: func(own *placementpb.PolicySet) ([]*placementpb.VerbUpdate, error) {
				pins, readErr := currentStreamPins(ctx, commodoredb.New(tx), candidate, true)
				if readErr != nil {
					return nil, readErr
				}
				return PinsUpdate(own, pins)
			},
		})
		changed = result.Changed
		return applyErr
	})
	return changed, err
}

// currentStreamPins reads the pins of the stream's current ingest mode, locking
// the source row when lock is set. A read-only dry run cannot take row locks.
// A deleted stream or one without a source row has no pins.
func currentStreamPins(ctx context.Context, q *commodoredb.Queries, candidate pinCandidate, lock bool) ([]string, error) {
	params := commodoredb.GetStreamPullSourcePinsParams{TenantID: candidate.tenantID, StreamID: candidate.streamID}
	var pins []string
	var err error
	if lock {
		pins, err = q.LockStreamPullSourcePins(ctx, commodoredb.LockStreamPullSourcePinsParams(params))
	} else {
		pins, err = q.GetStreamPullSourcePins(ctx, params)
	}
	if errors.Is(err, errNoRows) {
		if lock {
			pins, err = q.LockStreamMistSourcePins(ctx, commodoredb.LockStreamMistSourcePinsParams(params))
		} else {
			pins, err = q.GetStreamMistSourcePins(ctx, commodoredb.GetStreamMistSourcePinsParams(params))
		}
	}
	if errors.Is(err, errNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read current pins: %w", err)
	}
	return sortedUniqueIDs(pins), nil
}

// VerifyPullSourcePinsToStreamRules proves every remaining pin has an own
// ingest allow restricting the stream to a subset of the pinned clusters.
func VerifyPullSourcePinsToStreamRules(ctx context.Context, db datamigrate.DB) error {
	exec, ok := db.(commodoredb.DBTX)
	if !ok {
		return errors.New("verify requires a query executor")
	}
	const page = 500
	after, unexpressed := zeroStreamID, 0
	for {
		candidates, err := listPinCandidates(ctx, db, after, page)
		if err != nil {
			return err
		}
		byTenant := map[string][]string{}
		for _, candidate := range candidates {
			byTenant[candidate.tenantID] = append(byTenant[candidate.tenantID], candidate.streamID)
		}
		policies := map[string]*placementpb.PolicySet{}
		for tenantID, streamIDs := range byTenant {
			tenantPolicies, readErr := StreamPolicies(ctx, exec, tenantID, streamIDs)
			if readErr != nil {
				return fmt.Errorf("read stream placement for tenant %s: %w", tenantID, readErr)
			}
			for streamID, policy := range tenantPolicies {
				policies[streamID] = policy
			}
		}
		for _, candidate := range candidates {
			if !PinsExpressed(policies[candidate.streamID], candidate.pins) {
				unexpressed++
			}
			after = candidate.streamID
		}
		if len(candidates) < page {
			break
		}
	}
	if unexpressed != 0 {
		return fmt.Errorf("%d pinned streams have no equivalent ingest placement rule", unexpressed)
	}
	return nil
}

func listPinCandidates(ctx context.Context, db datamigrate.DB, after string, limit int) ([]pinCandidate, error) {
	rows, err := db.QueryContext(ctx, PullSourcePinCandidatesSQL, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list pinned streams: %w", err)
	}
	defer rows.Close()
	var out []pinCandidate
	for rows.Next() {
		var candidate pinCandidate
		if err := rows.Scan(&candidate.streamID, &candidate.tenantID, pq.Array(&candidate.pins)); err != nil {
			return nil, fmt.Errorf("scan pinned stream: %w", err)
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}
