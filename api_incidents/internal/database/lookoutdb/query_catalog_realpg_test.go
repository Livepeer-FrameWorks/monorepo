//go:build schema_verify

package lookoutdb

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"frameworks/api_incidents/internal/lookouttest"
)

// TestGeneratedQueryCatalogPrepares_RealPG prepares every sqlc query against
// the baseline so a query that drifts from the schema fails before runtime.
func TestGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	prepareGeneratedQueries(t, lookouttest.StartPostgres(t))
}

// TestGeneratedQueryCatalogPrepares_RealYugabyte prepares the same catalog on
// the supported YugabyteDB release.
func TestGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	prepareGeneratedQueries(t, lookouttest.StartYugabyte(t))
}

func prepareGeneratedQueries(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	files, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	prepared := 0
	fset := token.NewFileSet()
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Values) != 1 {
					continue
				}
				lit, ok := value.Values[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				query, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", value.Names[0].Name, err)
				}
				stmt, err := db.PrepareContext(ctx, query)
				if err != nil {
					t.Errorf("prepare %s: %v", value.Names[0].Name, err)
					continue
				}
				_ = stmt.Close()
				prepared++
			}
		}
	}
	if prepared == 0 {
		t.Fatal("no generated queries found")
	}
}
