package commodoredb

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Handler packages may open transactions, but SQL execution belongs in this
// database package so query ownership, generation, and contracts stay auditable.
func TestCommodoreHandlersDoNotExecuteRawSQL(t *testing.T) {
	methods := map[string]bool{
		"Exec": true, "ExecContext": true, "Query": true, "QueryContext": true,
		"QueryRow": true, "QueryRowContext": true, "Prepare": true, "PrepareContext": true,
	}
	for _, directory := range []string{"../../grpc", "../../bootstrap"} {
		paths, err := filepath.Glob(filepath.Join(directory, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				continue
			}
			fileSet := token.NewFileSet()
			file, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !methods[selector.Sel.Name] {
					return true
				}
				position := fileSet.Position(call.Pos())
				t.Errorf("raw database call %s in %s:%d; add a sqlc query or typed commodoredb adapter", selector.Sel.Name, path, position.Line)
				return true
			})
		}
	}
}
