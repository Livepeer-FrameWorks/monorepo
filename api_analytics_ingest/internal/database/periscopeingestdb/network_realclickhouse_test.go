//go:build schema_verify

package periscopeingestdb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
)

func verifyNetworkReplayAndRollups(t *testing.T, conn driver.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tenant := uuid.New()
	now := time.Now().UTC()
	observed := now.Truncate(time.Hour).Add(-time.Hour + time.Minute)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	insert := func(id uuid.UUID, success bool, latency uint32) {
		t.Helper()
		metric := obs.Metric{EventID: id.String(), Timestamp: observed, Provider: "livepeer", Network: "42161", Environment: "staging", ProducerKind: "gateway", Region: "eu", Kind: "segment", ResourceID: "0xabc", IP: "192.0.2.1", Workload: "transcoding", ProfileKey: "720p", Success: success, Qualifying: true, LatencyMS: latency, SourceDurationMS: 2000, Attempts: 2, FallbackCount: 1}
		raw, err := json.Marshal(metric)
		if err != nil {
			t.Fatal(err)
		}
		writer, err := PrepareNetworkObservation(ctx, nativeContractConn{Conn: conn})
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		if err := writer.Append(NetworkObservationRow{TenantID: tenant, EventID: id, Timestamp: observed, Metric: metric, PublicData: string(raw), PrivateData: "{}"}); err != nil {
			t.Fatal(err)
		}
		if err := writer.Send(); err != nil {
			t.Fatal(err)
		}
	}
	rebuild := func() {
		t.Helper()
		for _, interval := range []int{300, 3600} {
			if err := RebuildNetworkRollups(ctx, conn, tenant.String(), "42161", "staging", now.Add(-time.Minute), time.Now().Add(time.Minute), interval); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertCounts := func(want uint64) {
		t.Helper()
		var rawCount uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM periscope.network_observations FINAL WHERE tenant_id=?", tenant).Scan(&rawCount); err != nil {
			t.Fatal(err)
		}
		if rawCount != want {
			t.Fatalf("raw replay count=%d want %d", rawCount, want)
		}
		for _, interval := range []uint32{300, 3600} {
			var samples, successes, failures, retries, fallbacks uint64
			var p95 float64
			if err := conn.QueryRow(ctx, `SELECT sum(samples),sum(successes),sum(failures),sum(retries),sum(fallbacks),toFloat64(quantilesTDigestIfMerge(0.5,0.95,0.99)(latency_state)[2]) FROM periscope.network_observation_rollups FINAL WHERE tenant_id=? AND interval_seconds=?`, tenant, interval).Scan(&samples, &successes, &failures, &retries, &fallbacks, &p95); err != nil {
				t.Fatal(err)
			}
			if samples != want || successes != want-1 || failures != 1 || retries != want || fallbacks != want || p95 < 100 || p95 > 900 {
				t.Fatalf("interval %d samples/success/failure/retry/fallback/p95=%d/%d/%d/%d/%d/%f", interval, samples, successes, failures, retries, fallbacks, p95)
			}
		}
	}
	insert(ids[0], true, 100)
	insert(ids[0], true, 100)
	insert(ids[1], false, 900)
	rebuild()
	rebuild()
	assertCounts(2)
	// A newly delivered observation rebuilds the entire original bucket.
	insert(ids[2], true, 300)
	rebuild()
	assertCounts(3)
}
