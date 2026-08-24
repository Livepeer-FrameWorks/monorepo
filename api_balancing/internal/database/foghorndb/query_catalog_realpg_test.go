//go:build schema_verify

package foghorndb

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

func TestFoghornGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	prepareFoghornCatalog(t, startFoghornCatalogPostgres(t))
}

func TestFoghornGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	prepareFoghornCatalog(t, startFoghornCatalogYugabyte(t))
}

func TestArtifactNodePlacementSerializesAbsentRows_RealPG(t *testing.T) {
	verifyArtifactNodePlacementSerialization(t, startFoghornCatalogPostgres(t))
}

func TestArtifactNodePlacementSerializesAbsentRows_RealYugabyte(t *testing.T) {
	verifyArtifactNodePlacementSerialization(t, startFoghornCatalogYugabyte(t))
}

func verifyArtifactNodePlacementSerialization(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const hash = "placement-serialization-proof"
	const tenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status) VALUES ($1, 'clip', $2::uuid, 'ready')`, hash, tenant); err != nil {
		t.Fatal(err)
	}

	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback() //nolint:errcheck
	q1 := New(tx1)
	if err := q1.LockArtifactPlacementParent(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := q1.LockArtifactNodeState(ctx, LockArtifactNodeStateParams{ArtifactHash: hash, NodeID: "node-1"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("first prior state = %v, want sql.ErrNoRows", err)
	}
	if _, err := q1.UpsertCachedArtifactNode(ctx, UpsertCachedArtifactNodeParams{ArtifactHash: hash, NodeID: "node-1", FilePath: "/tmp/a", SizeBytes: 1}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		priorExisted bool
		err          error
	}
	started := make(chan struct{})
	done := make(chan result, 1)
	go func() {
		tx2, err := db.BeginTx(ctx, nil)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer tx2.Rollback() //nolint:errcheck
		close(started)
		q2 := New(tx2)
		if err := q2.LockArtifactPlacementParent(ctx, hash); err != nil {
			done <- result{err: err}
			return
		}
		_, err = q2.LockArtifactNodeState(ctx, LockArtifactNodeStateParams{ArtifactHash: hash, NodeID: "node-1"})
		priorExisted := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			done <- result{err: err}
			return
		}
		if _, err := q2.UpsertCachedArtifactNode(ctx, UpsertCachedArtifactNodeParams{ArtifactHash: hash, NodeID: "node-1", FilePath: "/tmp/b", SizeBytes: 2}); err != nil {
			done <- result{err: err}
			return
		}
		done <- result{priorExisted: priorExisted, err: tx2.Commit()}
	}()
	<-started
	select {
	case second := <-done:
		t.Fatalf("second writer escaped the parent lock before commit: %+v", second)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}
	second := <-done
	if second.err != nil {
		t.Fatal(second.err)
	}
	if !second.priorExisted {
		t.Fatal("second writer did not observe the row committed by the serialized first writer")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.artifact_nodes WHERE artifact_hash=$1 AND node_id='node-1'`, hash).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("placement row count = %d, want 1", count)
	}
}

func prepareFoghornCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range foghornGeneratedQueries(t) {
		name := fmt.Sprintf("foghorn_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

type foghornGeneratedQuery struct {
	file string
	name string
	sql  string
}

func foghornGeneratedQueries(t *testing.T) []foghornGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []foghornGeneratedQuery
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
					queries = append(queries, foghornGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	if len(queries) == 0 {
		t.Fatal("no generated Foghorn queries found")
	}
	return queries
}

func startFoghornCatalogPostgres(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("fw-foghorn-catalog-pg-%d", time.Now().UnixNano())
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	return startFoghornCatalogEngine(t, name, image, "5432/tcp", "postgres", "postgres", "harness", []string{"-e", "POSTGRES_PASSWORD=harness"})
}

func startFoghornCatalogYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("fw-foghorn-catalog-yb-%d", time.Now().UnixNano())
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	return startFoghornCatalogEngine(t, name, image, "5433/tcp", "yugabyte", "yugabyte", "", []string{
		"--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)"`,
	})
}

func startFoghornCatalogEngine(t *testing.T, name, image, containerPort, user, database, password string, args []string) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	runArgs := []string{"run", "-d", "--name", name, "-P"}
	if containerPort == "5432/tcp" {
		runArgs = append(runArgs, args...)
		runArgs = append(runArgs, image)
	} else {
		runArgs = append(runArgs, args...)
	}
	if output, err := dockerpg.Run(runArgs...); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, containerPort)
	if err != nil {
		t.Fatal(err)
	}
	dsn := fmt.Sprintf("postgres://%s@127.0.0.1:%s/%s?sslmode=disable", user, port, database)
	if password != "" {
		dsn = fmt.Sprintf("postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable", user, password, port, database)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
