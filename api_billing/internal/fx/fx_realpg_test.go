//go:build schema_verify

package fx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func startFXRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-fx-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply purser schema: %v", err)
	}
	return db
}

func TestFXLookupReferenceAge_RealPG(t *testing.T) {
	db := startFXRealPG(t)
	ctx := context.Background()
	rates, err := ParseECB(readFixture(t, "eurofxref-hist-90d.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var throughMaundyThursday []Rate
	for _, rate := range rates {
		if !rate.ReferenceDate.After(day("2026-04-02")) {
			throughMaundyThursday = append(throughMaundyThursday, rate)
		}
	}
	if err := Upsert(ctx, db, throughMaundyThursday, time.Now()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		on   string
		want string
	}{
		{"2026-04-02", "2026-04-02"},
		{"2026-04-06", "2026-04-02"}, // Easter Monday
		{"2026-04-07", "2026-04-02"}, // five days old: accepted
	} {
		rate, err := Lookup(ctx, db, USD, day(tc.on))
		if err != nil {
			t.Fatalf("lookup on %s: %v", tc.on, err)
		}
		if got := rate.ReferenceDate.Format(time.DateOnly); got != tc.want || rate.UnitsPerEUR.String() != "1.0815" {
			t.Fatalf("lookup on %s = %s %s, want %s", tc.on, got, rate.UnitsPerEUR, tc.want)
		}
	}
	if _, err := Lookup(ctx, db, USD, day("2026-04-08")); !errors.Is(err, ErrStaleRate) {
		t.Fatalf("six days old err = %v, want ErrStaleRate", err)
	}
	if _, err := Lookup(ctx, db, GBP, day("2026-03-26")); !errors.Is(err, ErrNoRate) {
		t.Fatalf("before first rate err = %v, want ErrNoRate", err)
	}
	if _, err := Lookup(ctx, db, "JPY", day("2026-04-02")); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Fatalf("JPY err = %v", err)
	}
	if rate, err := Lookup(ctx, db, EUR, day("2026-04-08")); err != nil || !rate.UnitsPerEUR.Equal(Identity(day("2026-04-08")).UnitsPerEUR) {
		t.Fatalf("EUR lookup = %+v, %v", rate, err)
	}

	if err := Upsert(ctx, db, rates, time.Now()); err != nil {
		t.Fatal(err)
	}
	rate, err := Lookup(ctx, db, GBP, day("2026-04-08"))
	if err != nil || rate.UnitsPerEUR.String() != "0.85445" {
		t.Fatalf("GBP on 2026-04-08 = %+v, %v", rate, err)
	}
	rate, err = Lookup(ctx, db, GBP, day("2026-04-05"))
	if err != nil || !rate.ReferenceDate.Equal(day("2026-04-02")) {
		t.Fatalf("GBP over Easter = %+v, %v", rate, err)
	}

	// Rates from one reference date convert across currencies; mixing dates is refused.
	usdRate, _ := Lookup(ctx, db, USD, day("2026-04-08"))
	gbpOld, _ := Lookup(ctx, db, GBP, day("2026-04-07"))
	if _, err := Cross(10000, gbpOld, usdRate); !errors.Is(err, ErrReferenceDateMismatch) {
		t.Fatalf("cross-date err = %v", err)
	}

	var stored int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.fx_rates`).Scan(&stored); err != nil || stored != 14 {
		t.Fatalf("stored rates = %d, %v; upsert must not duplicate", stored, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.fx_rates (currency, reference_date, units_per_eur, source) VALUES ('usd', DATE '2026-01-02', 1.1, 'ecb')`); err == nil {
		t.Fatal("lowercase fx currency accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.fx_rates (currency, reference_date, units_per_eur, source) VALUES ('USD', DATE '2026-01-02', 0, 'ecb')`); err == nil {
		t.Fatal("zero fx rate accepted")
	}
}

func TestFXSyncerFeedSelectionAndAgeGauge_RealPG(t *testing.T) {
	db := startFXRealPG(t)
	ctx := context.Background()
	var requested []Feed
	fetch := func(_ context.Context, feed Feed) ([]byte, error) {
		requested = append(requested, feed)
		switch feed {
		case Feed90Days:
			return readFixture(t, "eurofxref-hist-90d.xml"), nil
		case FeedDaily:
			return readFixture(t, "eurofxref-daily.xml"), nil
		default:
			return nil, fmt.Errorf("unexpected feed %s", feed)
		}
	}
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "fx_rate_reference_age_seconds"}, []string{"currency"})
	syncer := NewSyncer(db, logging.NewLogger(), fetch, gauge)
	syncer.now = func() time.Time { return time.Date(2026, 4, 8, 18, 0, 0, 0, time.UTC) }

	if err := syncer.RefreshAge(ctx); err != nil {
		t.Fatal(err)
	}
	if count := testutil.CollectAndCount(gauge); count != 0 {
		t.Fatalf("age series without stored rates = %d, want none", count)
	}
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 || requested[0] != Feed90Days || requested[1] != FeedDaily {
		t.Fatalf("feeds = %v, want the 90-day feed for an empty store and then the daily feed", requested)
	}
	if err := syncer.RefreshAge(ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues(USD)); got != 18*3600 {
		t.Fatalf("USD reference age = %v seconds, want 64800", got)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM purser.fx_rates WHERE currency = 'GBP' AND reference_date > DATE '2026-04-01'`); err != nil {
		t.Fatal(err)
	}
	requested = nil
	syncer.now = func() time.Time { return time.Date(2026, 4, 8, 18, 0, 0, 0, time.UTC) }
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 || requested[0] != Feed90Days {
		t.Fatalf("feeds after a 7-day GBP gap = %v, want the 90-day feed", requested)
	}
}
