package datamigrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	FederatedArtifactLifecycleID       = "foghorn_federated_artifact_lifecycle_v0_3_0"
	FederatedPointerPurgeEligibilityID = "foghorn_federated_pointer_purge_eligibility_v0_3_0"
)

var registerOnce sync.Once

func Register() {
	registerOnce.Do(func() {
		datamigrate.Register(datamigrate.Migration{
			ID: FederatedArtifactLifecycleID, Service: "foghorn", IntroducedIn: "v0.3.0",
			Description: "normalize legacy federation routing pointers into the managed artifact lifecycle",
			Run:         runFederatedArtifactLifecycle,
			Verify:      verifyFederatedArtifactLifecycle,
		})
		datamigrate.Register(datamigrate.Migration{
			ID: FederatedPointerPurgeEligibilityID, Service: "foghorn", IntroducedIn: "v0.3.0",
			Description: "preserve federated pointer age when the dedicated purge clock is introduced",
			DependsOn:   []string{FederatedArtifactLifecycleID},
			Run:         runFederatedPointerPurgeEligibility,
			Verify:      verifyFederatedPointerPurgeEligibility,
		})
	})
}

func runFederatedPointerPurgeEligibility(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	queries, err := generatedQueries(db)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	row, err := queries.BackfillFederatedPointerPurgeEligibilityBatch(ctx, int32(batchSize))
	if err != nil {
		return datamigrate.Progress{}, fmt.Errorf("backfill federated pointer purge eligibility: %w", err)
	}
	return datamigrate.Progress{Scanned: row.ScannedCount, Changed: row.ChangedCount, Done: row.ScannedCount < int64(batchSize)}, nil
}

func verifyFederatedPointerPurgeEligibility(ctx context.Context, db datamigrate.DB) error {
	queries, err := generatedQueries(db)
	if err != nil {
		return err
	}
	remaining, err := queries.CountFederatedPointersWithUnnormalizedPurgeEligibility(ctx)
	if err != nil {
		return fmt.Errorf("verify federated pointer purge eligibility: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("federated pointer purge eligibility has %d rows remaining", remaining)
	}
	return nil
}

func runFederatedArtifactLifecycle(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	queries, err := generatedQueries(db)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	row, err := queries.BackfillFederatedArtifactLifecycleBatch(ctx, int32(batchSize))
	if err != nil {
		return datamigrate.Progress{}, fmt.Errorf("backfill federated artifact lifecycle: %w", err)
	}
	return datamigrate.Progress{Scanned: row.ScannedCount, Changed: row.ChangedCount, Done: row.ScannedCount < int64(batchSize)}, nil
}

func verifyFederatedArtifactLifecycle(ctx context.Context, db datamigrate.DB) error {
	queries, err := generatedQueries(db)
	if err != nil {
		return err
	}
	remaining, err := queries.CountLegacyFederatedArtifactPointers(ctx)
	if err != nil {
		return fmt.Errorf("verify federated artifact lifecycle: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("federated artifact lifecycle has %d rows remaining", remaining)
	}
	return nil
}

func generatedQueries(db datamigrate.DB) (*foghorndb.Queries, error) {
	queryDB, ok := db.(foghorndb.DBTX)
	if !ok {
		return nil, errors.New("federated artifact migration requires the generated Foghorn DB boundary")
	}
	return foghorndb.New(queryDB), nil
}

var runRegisteredBackground = func(ctx context.Context, db *sql.DB, batchSize int) error {
	for _, id := range []string{FederatedArtifactLifecycleID, FederatedPointerPurgeEligibilityID} {
		if err := datamigrate.HandleRun(ctx, func() (*sql.DB, error) { return db, nil }, io.Discard, []string{
			id, "--batch-size", strconv.Itoa(batchSize),
		}); err != nil {
			return err
		}
	}
	return nil
}

// RunBackground repairs and records the database local to this Foghorn cell.
// Each cell has its own physical database, so every service instance runs the
// registered migration against its own ledger; the shared lease makes HA
// replicas cooperative and the process context makes shutdown cancellable.
func RunBackground(ctx context.Context, db *sql.DB, logger logging.Logger) {
	Register()
	for {
		err := runRegisteredBackground(ctx, db, 1000)
		if err == nil {
			logger.Info("Federated artifact lifecycle migration complete")
			return
		}
		if ctx.Err() == nil {
			logger.WithError(err).Warn("Federated artifact lifecycle migration will retry")
		}
		delay := 5 * time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
