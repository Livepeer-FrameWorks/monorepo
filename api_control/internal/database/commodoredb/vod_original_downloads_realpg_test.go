//go:build schema_verify

package commodoredb

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func TestVodOriginalDownloadsShareTenantBurst_RealPG(t *testing.T) {
	verifyVodOriginalDownloadBurst(t, startCommodoreQueryCatalogRealPG(t))
}

func TestVodOriginalDownloadsShareTenantBurst_RealYugabyte(t *testing.T) {
	verifyVodOriginalDownloadBurst(t, startCommodoreQueryCatalogRealYugabyte(t))
}

func verifyVodOriginalDownloadBurst(t *testing.T, conn *sql.DB) {
	t.Helper()
	ctx := t.Context()
	const tenant = "7a1e0000-0000-4000-8000-000000000057"
	params := ConsumeVodOriginalDownloadGrantParams{TenantID: tenant, IntervalSeconds: 3, BurstSeconds: 12}
	var workers sync.WaitGroup
	start := make(chan struct{})
	outcomes := make(chan error, 10)
	for range 10 {
		workers.Go(func() {
			<-start
			// Independent repositories model different Commodore instances.
			err := database.RetryPostgres(ctx, 5, 10*time.Millisecond, func() error {
				_, err := New(conn).ConsumeVodOriginalDownloadGrant(ctx, params)
				return err
			})
			outcomes <- err
		})
	}
	close(start)
	workers.Wait()
	close(outcomes)
	granted := 0
	for err := range outcomes {
		if err == nil {
			granted++
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	if granted != 5 {
		t.Fatalf("concurrent gateways received %d original grants, want shared burst 5", granted)
	}
	other := params
	other.TenantID = "7a1e0000-0000-4000-8000-000000000058"
	if _, err := New(conn).ConsumeVodOriginalDownloadGrant(ctx, other); err != nil {
		t.Fatalf("another tenant inherited the exhausted bucket: %v", err)
	}
	// Refill exactly one token without a wall-clock sleep; the other four stay spent.
	if _, err := conn.ExecContext(ctx, `UPDATE commodore.vod_original_download_limits
		SET next_grant_at = statement_timestamp() + INTERVAL '11 seconds' WHERE tenant_id = $1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := New(conn).ConsumeVodOriginalDownloadGrant(ctx, params); err != nil {
		t.Fatalf("refilled grant refused: %v", err)
	}
	if _, err := New(conn).ConsumeVodOriginalDownloadGrant(ctx, params); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("refill restored more than one token: %v", err)
	}
}
