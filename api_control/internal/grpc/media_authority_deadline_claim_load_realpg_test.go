//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestMediaAuthorityDeadlineClaimLoad_RealPG(t *testing.T) {
	testMediaAuthorityDeadlineClaimLoad(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityDeadlineClaimLoad_RealYugabyte(t *testing.T) {
	testMediaAuthorityDeadlineClaimLoad(t, startPlacementDeliveryYugabyte(t, "deadline_claim_load"))
}

// Several Commodore replicas claim one short-lease backlog at once while the
// queue observation sweeps and counts the same table, beside the superseded
// and acknowledged history a production queue keeps. Every current delivery is
// claimed exactly once, no superseded version is claimed, a cell never has two
// deliveries in flight, and claims and observations stay well inside their
// budgets. On YugabyteDB a storage round trip per queued row in the claim, or
// per authority in the observation, does not.
func testMediaAuthorityDeadlineClaimLoad(t *testing.T, db *sql.DB) {
	t.Helper()
	const (
		cells       = 40
		authorities = 26000
		// Version 2 is current for every authority. Its deliveries are the
		// acknowledged history, the pending short-lease backlog, and a few
		// rejections; version 1 is superseded history, apart from obsolete
		// rows still pending that the claim must pass over.
		acknowledged = 20000
		pending      = 3000
		rejected     = 50
		obsolete     = 300
		replicas     = 4
	)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// A warm pool, as a running replica has: a new YugabyteDB connection reads
	// the catalog on its first statements, which is connection cost, not claim
	// cost.
	connections := replicas*(mediaAuthorityDeliveryWorkers+2) + 2
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	for _, seed := range []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO commodore.media_authority_versions
    (authority_kind,authority_id,authority_version,payload_schema_version,payload,payload_sha256,source_revisions,issued_at,refresh_after,valid_until)
SELECT 'tenant','claim-load-'||n,v,2,''::bytea,decode(repeat('00',32),'hex'),'[]'::jsonb,NOW(),NOW()+INTERVAL '10 minutes',NOW()+INTERVAL '20 minutes'
FROM generate_series(1,$1::integer) AS n, generate_series(1,2) AS v`, []any{authorities}},
		{`INSERT INTO commodore.media_authority_current (authority_kind,authority_id,authority_version)
SELECT 'tenant','claim-load-'||n,2 FROM generate_series(1,$1::integer) AS n`, []any{authorities}},
		{`INSERT INTO commodore.media_authority_deliveries
    (authority_kind,authority_id,authority_version,cell_id,signed_envelope,short_lease,status,next_attempt_at,created_at)
SELECT 'tenant','claim-load-'||n,1,'cell-'||(n%$3::integer),'\x00'::bytea,TRUE,
       CASE WHEN n<=$2 THEN 'pending' ELSE 'superseded' END,
       NOW()-INTERVAL '1 second',NOW()-INTERVAL '2 hours'+n*INTERVAL '1 millisecond'
FROM generate_series(1,$1::integer) AS n`, []any{authorities, obsolete, cells}},
		{`INSERT INTO commodore.media_authority_deliveries
    (authority_kind,authority_id,authority_version,cell_id,signed_envelope,short_lease,status,next_attempt_at,created_at)
SELECT 'tenant','claim-load-'||n,2,'cell-'||(n%$4::integer),'\x00'::bytea,TRUE,
       CASE WHEN n<=$1::integer THEN 'acknowledged' WHEN n<=$1::integer+$2::integer THEN 'pending' ELSE 'rejected' END,
       NOW()-INTERVAL '1 second',NOW()-INTERVAL '1 hour'+n*INTERVAL '1 millisecond'
FROM generate_series(1,$1::integer+$2::integer+$3::integer) AS n`, []any{acknowledged, pending, rejected, cells}},
		{"ANALYZE commodore.media_authority_deliveries", nil},
	} {
		if _, err := db.ExecContext(ctx, seed.statement, seed.args...); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu                     sync.Mutex
		delivered              = map[string]int{}
		inflight               = map[string]string{}
		claims, observations   []time.Duration
		failures               []string
		remaining              atomic.Int64
		superseded, staleClaim atomic.Int32
	)
	remaining.Store(pending)
	q := commodoredb.New(db)
	replica := func() {
		server := &CommodoreServer{db: db, logger: logging.NewLogger()}
		var poll mediaAuthorityDeadlinePoll
		for remaining.Load() > 0 && ctx.Err() == nil {
			started := time.Now()
			rows, err := server.claimMediaAuthorityDeadlineDeliveries(ctx, mediaAuthorityDeliveryWorkers)
			elapsed := time.Since(started)
			mu.Lock()
			claims = append(claims, elapsed)
			if err != nil {
				failures = append(failures, "claim: "+err.Error())
			}
			mu.Unlock()
			if err != nil || len(rows) == 0 {
				// Replicas that find nothing back off as production does, but
				// the test does not wait out a long delay once the queue is
				// close to drained.
				delay, _ := poll.next(mediaAuthorityDeadlineDrain{err: err}, time.Now())
				select {
				case <-ctx.Done():
				case <-time.After(min(delay, 200*time.Millisecond)):
				}
				continue
			}
			poll.next(mediaAuthorityDeadlineDrain{claimed: len(rows)}, time.Now())
			var group sync.WaitGroup
			for _, row := range rows {
				if row.AuthorityVersion != 2 {
					staleClaim.Add(1)
				}
				key := fmt.Sprintf("%s/%d/%s", row.AuthorityID, row.AuthorityVersion, row.CellID)
				mu.Lock()
				delivered[key]++
				if holder, busy := inflight[row.CellID]; busy {
					t.Errorf("cell %s has %s and %s in flight together", row.CellID, holder, key)
				}
				inflight[row.CellID] = key
				mu.Unlock()
				group.Add(1)
				go func() {
					defer group.Done()
					time.Sleep(2 * time.Millisecond)
					mu.Lock()
					delete(inflight, row.CellID)
					mu.Unlock()
					if n, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID}); err != nil || n != 1 {
						t.Errorf("acknowledge %s: rows=%d err=%v", key, n, err)
						return
					}
					remaining.Add(-1)
				}()
			}
			group.Wait()
		}
	}
	// The observation runs every two seconds for the whole drain, several times
	// as often as replicas observing once a minute each would.
	observer := func() {
		for remaining.Load() > 0 && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			started := time.Now()
			swept, err := authorityMetricQuery(ctx, func(ctx context.Context) (int64, error) {
				return q.SupersedeExpiredObsoleteMediaAuthorityDeliveries(ctx, mediaAuthorityQueueSweepBatch)
			})
			if err == nil {
				superseded.Add(int32(swept))
				_, err = authorityMetricQuery(ctx, func(ctx context.Context) (int64, error) {
					return q.SettleExpiredMediaAuthorityDeliveries(ctx, mediaAuthorityQueueSweepBatch)
				})
			}
			if err == nil {
				_, err = authorityMetricQuery(ctx, q.ListMediaAuthorityDeliveryStats)
			}
			mu.Lock()
			observations = append(observations, time.Since(started))
			if err != nil && ctx.Err() == nil {
				failures = append(failures, "observation: "+err.Error())
			}
			mu.Unlock()
		}
	}
	started := time.Now()
	var group sync.WaitGroup
	group.Add(1)
	go func() { defer group.Done(); observer() }()
	for range replicas {
		group.Add(1)
		go func() { defer group.Done(); replica() }()
	}
	group.Wait()
	drained := time.Since(started)
	if ctx.Err() != nil {
		t.Fatalf("queue did not drain: %d deliveries left after %s", remaining.Load(), drained)
	}
	for key, count := range delivered {
		if count != 1 {
			t.Errorf("%s delivered %d times", key, count)
		}
	}
	if len(delivered) != pending || staleClaim.Load() != 0 {
		t.Fatalf("delivered %d distinct deliveries (%d of a superseded version), want %d current", len(delivered), staleClaim.Load(), pending)
	}
	percentile := func(samples []time.Duration, p int) time.Duration {
		if len(samples) == 0 {
			return 0
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[min(len(samples)-1, len(samples)*p/100)]
	}
	claimP99, observationMax := percentile(claims, 99), percentile(observations, 100)
	t.Logf("replicas=%d pending=%d history=%d drained=%s claims=%d claim_median=%s claim_p99=%s claim_max=%s observations=%d observation_median=%s observation_max=%s superseded=%d failures=%d",
		replicas, pending, authorities+acknowledged, drained, len(claims), percentile(claims, 50), claimP99, percentile(claims, 100),
		len(observations), percentile(observations, 50), observationMax, superseded.Load(), len(failures))
	if len(failures) > 0 {
		t.Fatalf("%d claims or observations failed, first: %s", len(failures), failures[0])
	}
	if superseded.Load() != obsolete {
		t.Fatalf("the sweep settled %d obsolete deliveries, want %d", superseded.Load(), obsolete)
	}
	if claimP99 >= mediaAuthorityDeadlineClaimBudget/2 {
		t.Fatalf("claim p99 %s is not under half the %s claim budget", claimP99, mediaAuthorityDeadlineClaimBudget)
	}
	if observationMax >= mediaAuthorityStatsTimeout {
		t.Fatalf("an observation took %s, at its %s budget", observationMax, mediaAuthorityStatsTimeout)
	}
}
