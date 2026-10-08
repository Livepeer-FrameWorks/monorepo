//go:build schema_verify

package periscopeingestdb

import (
	"context"
	"encoding/json"
	"math/big"
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

func verifyNetworkWorkAccounting(t *testing.T, conn driver.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tenant := uuid.New()
	now := time.Now().UTC()
	at := now.Truncate(time.Hour).Add(-time.Hour + time.Minute)
	amount := func(n, d string) *obs.Amount {
		return &obs.Amount{Numerator: n, Denominator: d, Currency: "ETH", Unit: "wei"}
	}
	facts := []obs.Metric{
		{Kind: "attempt", Success: true, Qualifying: false, Disposition: "hedge_loser", Work: &obs.WorkFact{Version: 1, RequestDispatched: true, ReturnedPixelsKnown: true, ReturnedPixels: 100, ServiceCharge: amount("1", "3"), TicketCreditSent: amount("7", "2"), TicketsSent: 2}},
		{Kind: "attempt", Success: true, Qualifying: true, Work: &obs.WorkFact{Version: 1, RequestDispatched: true, ReturnedPixelsKnown: true, ReturnedPixels: 200, ServiceCharge: amount("2", "3"), TicketCreditSent: amount("0", "1")}},
		{Kind: "segment", Success: true, Qualifying: true, Work: &obs.WorkFact{Version: 1, AcceptedPixels: 200, AcceptedRenditions: 2}},
		{Kind: "attempt", Success: true},
		{Kind: "attempt", Work: &obs.WorkFact{Version: 1}},
		{Kind: "attempt", Work: &obs.WorkFact{Version: 1, RequestDispatched: true, TicketCreditSent: amount("5", "3"), TicketsSent: 1}},
	}
	for i := range facts {
		facts[i].EventID = uuid.NewString()
		facts[i].Timestamp = at
		facts[i].Provider = "livepeer"
		facts[i].Network = "42161"
		facts[i].Environment = "staging"
		facts[i].ProducerKind = "gateway"
		facts[i].ResourceID = "0xwork"
		facts[i].IP = "192.0.2.9"
		facts[i].Region = "eu"
		facts[i].Workload = "transcoding"
		facts[i].ProfileKey = "720p"
		facts[i].SourceDurationMS = 2000
	}
	insert := func(m obs.Metric) {
		t.Helper()
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		writer, err := PrepareNetworkObservation(ctx, nativeContractConn{Conn: conn})
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		if err := writer.Append(NetworkObservationRow{TenantID: tenant, EventID: uuid.MustParse(m.EventID), Timestamp: at, Metric: m, PublicData: string(raw), PrivateData: "{}"}); err != nil {
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
	for _, fact := range facts[:5] {
		insert(fact)
	}
	insert(facts[0])
	rebuild()
	rebuild()
	insert(facts[5])
	insert(facts[5])
	rebuild()
	rebuild()
	for _, interval := range []uint32{300, 3600} {
		var attempts, unknown, dispatched, source, returned, pixels, accepted, acceptedSource, acceptedPixels, renditionMS, charges, credits, tickets uint64
		err := conn.QueryRow(ctx, `SELECT sum(accounted_attempts),sum(unaccounted_samples),sum(dispatched_attempts),sum(dispatched_source_ms),sum(returned_attempts),sum(returned_pixels),sum(accepted_segments),sum(accepted_source_ms),sum(accepted_pixels),sum(accepted_rendition_ms),sum(service_charge_samples),sum(ticket_credit_samples),sum(tickets_sent) FROM periscope.network_work_rollups FINAL WHERE tenant_id=? AND interval_seconds=?`, tenant, interval).Scan(&attempts, &unknown, &dispatched, &source, &returned, &pixels, &accepted, &acceptedSource, &acceptedPixels, &renditionMS, &charges, &credits, &tickets)
		if err != nil {
			t.Fatal(err)
		}
		if attempts != 4 || unknown != 1 || dispatched != 3 || source != 6000 || returned != 2 || pixels != 300 || accepted != 1 || acceptedSource != 2000 || acceptedPixels != 200 || renditionMS != 4000 || charges != 2 || credits != 3 || tickets != 3 {
			t.Fatalf("interval %d: lost/doubled work: %d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d", interval, attempts, unknown, dispatched, source, returned, pixels, accepted, acceptedSource, acceptedPixels, renditionMS, charges, credits, tickets)
		}
		var numerator string
		if err := conn.QueryRow(ctx, `SELECT toString(sum(service_charge_numerator)) FROM periscope.network_work_rollups FINAL WHERE tenant_id=? AND interval_seconds=? AND service_charge_denominator='3'`, tenant, interval).Scan(&numerator); err != nil {
			t.Fatal(err)
		}
		if numerator != "3" {
			t.Fatalf("fractional service charges rounded or replayed: %s/3", numerator)
		}
	}
	probabilityBase := new(big.Int).Lsh(big.NewInt(1), 256)
	wide := new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(1000000000000000000), new(big.Int).Sub(new(big.Int).Set(probabilityBase), big.NewInt(2))), new(big.Int).Sub(probabilityBase, big.NewInt(1)))
	wideFact := facts[5]
	wideFact.EventID = uuid.NewString()
	wideFact.Work = &obs.WorkFact{Version: 1, RequestDispatched: true, TicketCreditSent: obs.AmountFromRat(wide), TicketsSent: 1}
	insert(wideFact)
	insert(wideFact)
	rebuild()
	rebuild()
	for _, interval := range []uint32{300, 3600} {
		var numerator, denominator string
		var samples uint64
		if err := conn.QueryRow(ctx, `SELECT ticket_credit_numerator,ticket_credit_denominator,sum(ticket_credit_samples) FROM periscope.network_work_rollups FINAL WHERE tenant_id=? AND interval_seconds=? AND ticket_credit_numerator=? GROUP BY ticket_credit_numerator,ticket_credit_denominator`, tenant, interval, wide.Num().String()).Scan(&numerator, &denominator, &samples); err != nil {
			t.Fatal(err)
		}
		if numerator != wide.Num().String() || denominator != wide.Denom().String() || samples != 1 {
			t.Fatalf("real ticket fraction lost or replayed at %d: %s/%s x%d", interval, numerator, denominator, samples)
		}
	}
}
