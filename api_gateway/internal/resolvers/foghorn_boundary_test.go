package resolvers

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const foghornClientImport = "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"

func TestBridgeFoghornBoundary(t *testing.T) {
	internalRoot := filepath.Clean(filepath.Join(".."))
	allowedFile := filepath.Join("resolvers", "infrastructure.go")

	var violations []string
	fset := token.NewFileSet()

	err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}

		rel, relErr := filepath.Rel(internalRoot, path)
		if relErr != nil {
			return relErr
		}

		foghornAliases := map[string]bool{}
		for _, imp := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(imp.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if importPath != foghornClientImport {
				continue
			}
			if rel != allowedFile {
				violations = append(violations, fmt.Sprintf("%s imports Foghorn client", rel))
			}
			if imp.Name != nil {
				foghornAliases[imp.Name.Name] = true
			} else {
				foghornAliases["foghorn"] = true
			}
		}
		if len(foghornAliases) == 0 {
			return nil
		}

		clientVars := map[string]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range assign.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok {
					continue
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "NewGRPCClient" {
					continue
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || !foghornAliases[ident.Name] || i >= len(assign.Lhs) {
					continue
				}
				if lhs, ok := assign.Lhs[i].(*ast.Ident); ok && lhs.Name != "_" {
					clientVars[lhs.Name] = true
				}
			}
			return true
		})

		allowedMethods := map[string]bool{
			"PreRegisterEdge": true,
			"Close":           true,
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || !clientVars[ident.Name] || allowedMethods[sel.Sel.Name] {
				return true
			}
			pos := fset.Position(sel.Pos())
			violations = append(violations, fmt.Sprintf("%s:%d calls Foghorn.%s", rel, pos.Line, sel.Sel.Name))
			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("scan Bridge Foghorn boundary: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Bridge may only use Foghorn for bootstrap PreRegisterEdge:\n%s", strings.Join(violations, "\n"))
	}
}
