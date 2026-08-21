//go:build schema_verify

package skipperdb

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
	db := startSkipperQueryCatalogRealPG(t)
	queries := skipperGeneratedQueries(t)
	if len(queries) != 62 {
		t.Fatalf("found %d generated Skipper queries, want 62", len(queries))
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range queries {
		name := fmt.Sprintf("skipper_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

func TestCrawlJobCatalog_RealPG(t *testing.T) {
	db := startSkipperQueryCatalogRealPG(t)
	queries := New(db)
	ctx := context.Background()
	tenantA := "10000000-0000-0000-0000-000000000001"
	tenantB := "20000000-0000-0000-0000-000000000002"
	now := time.Now().UTC().Truncate(time.Second)
	created, err := queries.CreateCrawlJob(ctx, CreateCrawlJobParams{
		ID: "30000000-0000-0000-0000-000000000003", TenantID: tenantA,
		SitemapUrl: "https://example.test/sitemap.xml", StartedAt: now,
	})
	if err != nil || created != 1 {
		t.Fatalf("create crawl job rows = %d, err = %v", created, err)
	}
	replayed, err := queries.CreateCrawlJob(ctx, CreateCrawlJobParams{
		ID: "40000000-0000-0000-0000-000000000004", TenantID: tenantA,
		SitemapUrl: "https://example.test/sitemap.xml", StartedAt: now,
	})
	if err != nil || replayed != 0 {
		t.Fatalf("replayed crawl job rows = %d, err = %v", replayed, err)
	}
	job, err := queries.GetCrawlJob(ctx, GetCrawlJobParams{ID: "30000000-0000-0000-0000-000000000003", TenantID: tenantA})
	if err != nil || job.Status != "running" {
		t.Fatalf("crawl job = %#v, err = %v", job, err)
	}
	if _, err := queries.GetCrawlJob(ctx, GetCrawlJobParams{ID: job.ID, TenantID: tenantB}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant crawl lookup error = %v", err)
	}
	if rows, err := queries.ListCrawlJobs(ctx, tenantA); err != nil || len(rows) != 1 {
		t.Fatalf("crawl jobs = %#v, err = %v", rows, err)
	}
	if err := queries.FinishRunningCrawlJob(ctx, FinishRunningCrawlJobParams{
		Status: "failed", Error: sql.NullString{String: "contract failure", Valid: true},
		FinishedAt: sql.NullTime{Time: now, Valid: true}, ID: job.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.CancelRunningCrawlJob(ctx, CancelRunningCrawlJobParams{FinishedAt: sql.NullTime{Time: now.Add(time.Hour), Valid: true}, ID: job.ID}); err != nil {
		t.Fatal(err)
	}
	status, err := queries.GetCrawlJobStatus(ctx, GetCrawlJobStatusParams{ID: job.ID, TenantID: tenantA})
	if err != nil || status != "failed" {
		t.Fatalf("finished status = %q, err = %v", status, err)
	}
	deleted, err := queries.CleanupFinishedCrawlJobs(ctx, CleanupFinishedCrawlJobsParams{
		TenantID: tenantA, Cutoff: sql.NullTime{Time: now.Add(time.Minute), Valid: true},
	})
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup rows = %d, err = %v", deleted, err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO skipper.skipper_knowledge (tenant_id, source_url, source_title, chunk_text, chunk_index) VALUES ($1::uuid, 'https://example.test/page', 'other', 'secret', 0), ($2::uuid, 'https://example.test/page', 'owned', 'visible', 0)`, tenantB, tenantA); err != nil {
		t.Fatal(err)
	}
	sample, err := queries.GetKnowledgeSample(ctx, GetKnowledgeSampleParams{TenantID: tenantA, SourceUrl: "https://example.test/page"})
	if err != nil || sample != "visible" {
		t.Fatalf("tenant knowledge sample = %q, err = %v", sample, err)
	}
}

type skipperGeneratedQuery struct {
	file string
	name string
	sql  string
}

func skipperGeneratedQueries(t *testing.T) []skipperGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []skipperGeneratedQuery
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
					queries = append(queries, skipperGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startSkipperQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-skipper-query-catalog-realpg-%d", time.Now().UnixNano())
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
	schema, err := dbsql.Content.ReadFile("schema/skipper.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
