//go:build schema_verify

package meteringdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	prepareMeteringQueryCatalog(t, startMeteringQueryCatalogRealPG(t))
}

func TestGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	prepareMeteringQueryCatalog(t, startMeteringQueryCatalogRealYugabyte(t))
}

func prepareMeteringQueryCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := meteringGeneratedQueries(t)
	if len(queries) != 11 {
		t.Fatalf("found %d generated Periscope Metering queries, want 11", len(queries))
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range queries {
		name := fmt.Sprintf("metering_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

func TestMeteringStateTransitions_RealPG(t *testing.T) {
	testMeteringStateTransitions(t, startMeteringQueryCatalogRealPG(t))
}

func TestMeteringStateTransitions_RealYugabyte(t *testing.T) {
	testMeteringStateTransitions(t, startMeteringQueryCatalogRealYugabyte(t))
}

func testMeteringStateTransitions(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := New(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tenantA := "10000000-0000-4000-8000-000000000001"
	tenantB := "20000000-0000-4000-8000-000000000002"

	activatedAt, err := queries.EnsureMeteringSource(ctx, EnsureMeteringSourceParams{
		SourceID: "region-a", SourceRegion: "", ActivatedAt: now,
	})
	if err != nil || !activatedAt.Equal(now) {
		t.Fatalf("activate source = %v, %v", activatedAt, err)
	}
	replayedAt, err := queries.EnsureMeteringSource(ctx, EnsureMeteringSourceParams{
		SourceID: "region-a", SourceRegion: "eu-west", ActivatedAt: now.Add(time.Hour),
	})
	if err != nil || !replayedAt.Equal(now) {
		t.Fatalf("replay source activation = %v, %v", replayedAt, err)
	}
	var persistedRegion string
	if err := db.QueryRowContext(ctx, `SELECT source_region FROM periscope.metering_sources WHERE source_id = 'region-a'`).Scan(&persistedRegion); err != nil {
		t.Fatal(err)
	}
	if persistedRegion != "eu-west" {
		t.Fatalf("source region = %q, want eu-west", persistedRegion)
	}

	if err := queries.InitializeBillingCursor(ctx, InitializeBillingCursorParams{
		SourceID: "region-a", TenantID: tenantA, LastProcessedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.InitializeBillingCursor(ctx, InitializeBillingCursorParams{
		SourceID: "region-a", TenantID: tenantA, LastProcessedAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	cursor, err := queries.GetBillingCursor(ctx, GetBillingCursorParams{SourceID: "region-a", TenantID: tenantA})
	if err != nil || !cursor.Equal(now) {
		t.Fatalf("idempotent cursor = %v, %v", cursor, err)
	}
	if _, err := queries.GetBillingCursor(ctx, GetBillingCursorParams{SourceID: "region-b", TenantID: tenantA}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-source cursor error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.billing_cursors (source_id, tenant_id, last_processed_at) VALUES ('region-a', '00000000-0000-0000-0000-000000000000', $1), ('region-b', $2, $1)`, now, tenantB); err != nil {
		t.Fatal(err)
	}
	cursorTenants, err := queries.ListBillingCursorTenants(ctx, "region-a")
	if err != nil || len(cursorTenants) != 1 || cursorTenants[0] != tenantA {
		t.Fatalf("cursor tenants = %#v, %v", cursorTenants, err)
	}
	nextCursor := now.Add(5 * time.Minute)
	if err := queries.AdvanceBillingCursor(ctx, AdvanceBillingCursorParams{
		LastProcessedAt: nextCursor, SourceID: "region-a", TenantID: tenantA,
	}); err != nil {
		t.Fatal(err)
	}
	cursor, err = queries.GetBillingCursor(ctx, GetBillingCursorParams{SourceID: "region-a", TenantID: tenantA})
	if err != nil || !cursor.Equal(nextCursor) {
		t.Fatalf("advanced cursor = %v, %v", cursor, err)
	}

	if err := queries.UpsertReservationKey(ctx, UpsertReservationKeyParams{
		SourceID: "region-a", TenantID: tenantA, ClusterID: "cluster-a", LastSequence: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.UpsertReservationKey(ctx, UpsertReservationKeyParams{
		SourceID: "region-a", TenantID: tenantA, ClusterID: "cluster-a", LastSequence: 11,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.UpsertReservationKey(ctx, UpsertReservationKeyParams{
		SourceID: "region-b", TenantID: tenantB, ClusterID: "cluster-b", LastSequence: 20,
	}); err != nil {
		t.Fatal(err)
	}
	reservationKeys, err := queries.ListReservationKeys(ctx, "region-a")
	if err != nil || len(reservationKeys) != 1 || reservationKeys[0].TenantID != tenantA || reservationKeys[0].ClusterID != "cluster-a" {
		t.Fatalf("reservation keys = %#v, %v", reservationKeys, err)
	}
	var sequence int64
	if err := db.QueryRowContext(ctx, `SELECT last_sequence FROM periscope.metering_reservation_keys WHERE source_id = 'region-a' AND tenant_id = $1`, tenantA).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if sequence != 11 {
		t.Fatalf("reservation sequence = %d, want 11", sequence)
	}
	if err := queries.DeleteReservationKey(ctx, DeleteReservationKeyParams{
		SourceID: "region-a", TenantID: tenantA, ClusterID: "cluster-a",
	}); err != nil {
		t.Fatal(err)
	}
	reservationKeys, err = queries.ListReservationKeys(ctx, "region-a")
	if err != nil || len(reservationKeys) != 0 {
		t.Fatalf("released reservation keys = %#v, %v", reservationKeys, err)
	}

	token, err := queries.AcquireMeteringLease(ctx, AcquireMeteringLeaseParams{
		SourceID: "region-a", PartitionKey: "finalized", OwnerID: "worker-a", LeaseSeconds: 60,
	})
	if err != nil || token != 1 {
		t.Fatalf("first lease token = %d, %v", token, err)
	}
	if _, err := queries.AcquireMeteringLease(ctx, AcquireMeteringLeaseParams{
		SourceID: "region-a", PartitionKey: "finalized", OwnerID: "worker-b", LeaseSeconds: 60,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("competing lease error = %v", err)
	}
	token, err = queries.AcquireMeteringLease(ctx, AcquireMeteringLeaseParams{
		SourceID: "region-a", PartitionKey: "finalized", OwnerID: "worker-a", LeaseSeconds: 60,
	})
	if err != nil || token != 2 {
		t.Fatalf("owner renewal token = %d, %v", token, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE periscope.metering_leases SET lease_until = NOW() - INTERVAL '1 second' WHERE source_id = 'region-a' AND partition_key = 'finalized'`); err != nil {
		t.Fatal(err)
	}
	token, err = queries.AcquireMeteringLease(ctx, AcquireMeteringLeaseParams{
		SourceID: "region-a", PartitionKey: "finalized", OwnerID: "worker-b", LeaseSeconds: 60,
	})
	if err != nil || token != 3 {
		t.Fatalf("expired lease takeover token = %d, %v", token, err)
	}

	completedThrough, err := queries.GetMeteringSourceCompletion(ctx, "region-a")
	if err != nil || completedThrough.Valid {
		t.Fatalf("initial completion = %#v, %v", completedThrough, err)
	}
	if err := queries.UpdateMeteringSourceCompletion(ctx, UpdateMeteringSourceCompletionParams{
		SourceID: "region-a", CompletedThrough: nextCursor,
	}); err != nil {
		t.Fatal(err)
	}
	completedThrough, err = queries.GetMeteringSourceCompletion(ctx, "region-a")
	if err != nil || !completedThrough.Valid || !completedThrough.Time.Equal(nextCursor) {
		t.Fatalf("advanced completion = %#v, %v", completedThrough, err)
	}
}

type meteringGeneratedQuery struct {
	file string
	name string
	sql  string
}

func meteringGeneratedQueries(t *testing.T) []meteringGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []meteringGeneratedQuery
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					queryName := "unknown"
					if index < len(value.Names) {
						queryName = value.Names[index].Name
					}
					queries = append(queries, meteringGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startMeteringQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-metering-query-catalog-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
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
	schema, err := dbsql.Content.ReadFile("schema/periscope.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func startMeteringQueryCatalogRealYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-metering-query-catalog-yb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--hostname", name, "-P", image,
		"bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)"`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/periscope.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
