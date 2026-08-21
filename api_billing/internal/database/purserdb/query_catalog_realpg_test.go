//go:build schema_verify

package purserdb

import (
	"context"
	"database/sql"
	"encoding/json"
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

type generatedQuery struct {
	file string
	name string
	sql  string
}

func TestGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	db := startQueryCatalogRealPG(t)
	queries := loadGeneratedQueries(t)
	if len(queries) < 100 {
		t.Fatalf("found only %d generated queries; catalog discovery is incomplete", len(queries))
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for index, query := range queries {
		preparedName := fmt.Sprintf("purser_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+preparedName+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+preparedName); err != nil {
			t.Fatalf("deallocate %s from %s: %v", query.name, query.file, err)
		}
	}

	t.Run("x402 intent replay is explicit", func(t *testing.T) {
		queries := New(db)
		params := UpsertX402SettlementIntentParams{
			Network: "base", PayerAddress: "0x1111111111111111111111111111111111111111", Nonce: "1",
			TenantID: "10000000-0000-0000-0000-000000000001", AmountCents: 125,
			AuthPayload: json.RawMessage(`{"scheme":"exact"}`), ClientIp: "", QuoteID: "",
		}
		inserted, err := queries.UpsertX402SettlementIntent(ctx, params)
		if err != nil {
			t.Fatalf("insert settlement intent: %v", err)
		}
		if inserted.ID == "" || inserted.TenantID != params.TenantID || inserted.AmountCents != params.AmountCents {
			t.Fatalf("inserted settlement = %#v", inserted)
		}
		if _, err := queries.UpsertX402SettlementIntent(ctx, params); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("replayed insert error = %v, want sql.ErrNoRows", err)
		}
		existing, err := queries.GetX402SettlementByIdentity(ctx, GetX402SettlementByIdentityParams{
			Network: params.Network, PayerAddress: params.PayerAddress, Nonce: params.Nonce,
		})
		if err != nil {
			t.Fatalf("load replayed settlement intent: %v", err)
		}
		if existing.ID != inserted.ID || existing.AuthPayload == "" {
			t.Fatalf("existing settlement = %#v, inserted = %#v", existing, inserted)
		}
	})
}

func loadGeneratedQueries(t *testing.T) []generatedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	queries := make([]generatedQuery, 0, len(paths))
	for _, path := range paths {
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
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
				for valueIndex, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr != nil {
						t.Fatalf("unquote constant in %s: %v", path, unquoteErr)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					name := "unknown"
					if valueIndex < len(value.Names) {
						name = value.Names[valueIndex].Name
					}
					queries = append(queries, generatedQuery{file: path, name: name, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-query-catalog-realpg-%d", time.Now().UnixNano())
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
		t.Fatal(err)
	}
	return db
}
