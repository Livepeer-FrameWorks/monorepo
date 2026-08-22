package periscopequerydb

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionClickHouseReadsCrossServiceBoundary(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	for _, relative := range []string{"internal/grpc", "internal/handlers"} {
		directory := filepath.Join(root, relative)
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			set := token.NewFileSet()
			file, parseErr := parser.ParseFile(set, path, nil, 0)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", path, parseErr)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				position := set.Position(call.Pos())
				if selector.Sel.Name == "QueryContext" || selector.Sel.Name == "QueryRowContext" {
					t.Errorf("%s:%d executes ClickHouse outside periscopequerydb", relative+"/"+entry.Name(), position.Line)
					return true
				}
				if selector.Sel.Name != "Query" && selector.Sel.Name != "QueryRow" {
					return true
				}
				pkg, packageOK := selector.X.(*ast.Ident)
				if !packageOK || pkg.Name != "periscopequerydb" {
					return true
				}
				if len(call.Args) < 3 {
					t.Errorf("%s:%d calls %s without context, database, and SQL", relative+"/"+entry.Name(), position.Line, selector.Sel.Name)
				}
				return true
			})
		}
	}
}
