package quartermasterdb

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestQuartermasterProductionCodeUsesRepositoryBoundary(t *testing.T) {
	t.Parallel()

	forbidden := map[string]struct{}{
		"Exec":            {},
		"ExecContext":     {},
		"Prepare":         {},
		"PrepareContext":  {},
		"Query":           {},
		"QueryContext":    {},
		"QueryRow":        {},
		"QueryRowContext": {},
	}
	for _, directory := range []string{"../../../cmd/quartermaster", "../../bootstrap", "../../grpc", "../../handlers"} {
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			set := token.NewFileSet()
			file, err := parser.ParseFile(set, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "c" && selector.Sel.Name == "Query" {
					return true
				}
				if _, blocked := forbidden[selector.Sel.Name]; blocked {
					position := set.Position(selector.Pos())
					t.Errorf("%s:%d calls %s directly; database access belongs in quartermasterdb", path, position.Line, selector.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("inspect %s: %v", directory, err)
		}
	}
}

func TestManualAdaptersHaveRealEngineCoverage(t *testing.T) {
	t.Parallel()

	manualMethods := make(map[string]struct{})
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".sql.go") || path == "db.go" {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || !function.Name.IsExported() || len(function.Recv.List) != 1 {
				continue
			}
			pointer, ok := function.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			receiver, ok := pointer.X.(*ast.Ident)
			if ok && receiver.Name == "Queries" {
				manualMethods[function.Name.Name] = struct{}{}
			}
		}
	}

	contract, err := parser.ParseFile(token.NewFileSet(), "query_catalog_realpg_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	covered := make(map[string]struct{})
	ast.Inspect(contract, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok {
			covered[selector.Sel.Name] = struct{}{}
		}
		return true
	})

	var missing []string
	for method := range manualMethods {
		if _, ok := covered[method]; !ok {
			missing = append(missing, method)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("manual Quartermaster adapters missing real-engine contract calls: %s", strings.Join(missing, ", "))
	}
}
