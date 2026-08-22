//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
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
	db := startCommodoreQueryCatalogRealPG(t)
	queries := commodoreGeneratedQueries(t)
	if len(queries) != 274 {
		t.Fatalf("found %d generated Commodore queries, want 274", len(queries))
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range queries {
		name := fmt.Sprintf("commodore_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

func TestManualQueryAdapters_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "33333333-3333-3333-3333-333333333333"
		vodID    = "44444444-4444-4444-4444-444444444444"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, password_hash, is_active)
		VALUES ($1::uuid, $2::uuid, 'adapter@example.com', 'x', TRUE)
	`, userID, tenantID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, internal_name, stream_key, playback_id, ingest_mode, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'adapter-stream', 'adapter-key', 'adapter-playback', 'push', 'Adapter stream')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	queries := New(db)
	affected, refused, err := queries.ApplyActiveIngestPlacementBatch(ctx, ActiveIngestPlacementBatchParams{
		TenantIDs: []string{tenantID}, InternalNames: []string{"adapter-stream"},
		ClaimTokens: []string{"adapter-claim"}, ClusterIDs: []string{"media-eu"},
		LeaseSeconds: 60, Renew: true,
	})
	if err != nil || affected != 1 || len(refused) != 0 {
		t.Fatalf("renew placement: affected=%d refused=%v err=%v", affected, refused, err)
	}
	affected, refused, err = queries.ApplyActiveIngestPlacementBatch(ctx, ActiveIngestPlacementBatchParams{
		TenantIDs: []string{tenantID}, InternalNames: []string{"adapter-stream"},
		ClaimTokens: []string{"adapter-claim"}, ClusterIDs: []string{"media-eu"}, Renew: false,
	})
	if err != nil || affected != 1 || len(refused) != 0 {
		t.Fatalf("release placement: affected=%d refused=%v err=%v", affected, refused, err)
	}

	if err := queries.InsertVODUploadRegistration(ctx, InsertVODUploadRegistrationParams{
		ID: vodID, TenantID: tenantID, UserID: userID, VodHash: "adapter-vod",
		InternalName: "vod+adapter", PlaybackID: "adapter-vod-playback",
		Title:       sql.NullString{String: "Adapter VOD", Valid: true},
		Description: sql.NullString{String: "contract", Valid: true},
		Filename:    "adapter.mp4", ContentType: sql.NullString{String: "video/mp4", Valid: true},
		SizeBytes:       sql.NullInt64{Int64: 123, Valid: true},
		OriginClusterID: sql.NullString{String: "media-eu", Valid: true},
	}); err != nil {
		t.Fatalf("insert VOD registration: %v", err)
	}
	resolvedStream, err := queries.ResolveIdentifierCatalog(ctx, ResolveIdentifierCatalogParams{
		IncludeIds: true,
		Identifier: streamID,
	})
	if err != nil {
		t.Fatalf("resolve stream identifier: %v", err)
	}
	if resolvedStream.IdentifierType != "stream_id" || resolvedStream.StreamID != streamID || resolvedStream.TenantID != tenantID {
		t.Fatalf("unexpected resolved stream: %+v", resolvedStream)
	}
	resolvedVOD, err := queries.ResolveIdentifierCatalog(ctx, ResolveIdentifierCatalogParams{
		Identifier: "adapter-vod",
	})
	if err != nil {
		t.Fatalf("resolve VOD identifier: %v", err)
	}
	if resolvedVOD.IdentifierType != "vod" || resolvedVOD.TenantID != tenantID || resolvedVOD.UserID != userID {
		t.Fatalf("unexpected resolved VOD: %+v", resolvedVOD)
	}
	catalog, err := queries.ListStorageArtifactCatalog(ctx, StorageArtifactFilter{
		TenantID: tenantID, SortField: "created_at", SortDirection: "DESC", Limit: 25,
	})
	if err != nil {
		t.Fatalf("list storage catalog: %v", err)
	}
	if catalog.Total != 1 || len(catalog.Rows) != 1 || catalog.Rows[0].ArtifactHash != "adapter-vod" || catalog.KindCounts["vod"] != 1 {
		t.Fatalf("unexpected storage catalog: total=%d rows=%+v facets=%v", catalog.Total, catalog.Rows, catalog.KindCounts)
	}
}

type commodoreGeneratedQuery struct {
	file string
	name string
	sql  string
}

func commodoreGeneratedQueries(t *testing.T) []commodoreGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []commodoreGeneratedQuery
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
					queries = append(queries, commodoreGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startCommodoreQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-commodore-query-catalog-realpg-%d", time.Now().UnixNano())
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
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
