package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestClickHouseWritesUseTypedServiceWriters(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read handlers package: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		set := token.NewFileSet()
		file, parseErr := parser.ParseFile(set, entry.Name(), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", entry.Name(), parseErr)
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
			if selector.Sel.Name == "PrepareBatch" {
				receiver, receiverOK := selector.X.(*ast.SelectorExpr)
				validAdapter := false
				if receiverOK {
					base, baseOK := receiver.X.(*ast.Ident)
					validAdapter = baseOK && base.Name == "c" && receiver.Sel.Name == "conn"
				}
				if entry.Name() != "handlers.go" || !validAdapter {
					t.Errorf("%s:%d calls PrepareBatch outside the native adapter", entry.Name(), position.Line)
				}
				return true
			}
			if selector.Sel.Name != "Append" {
				return true
			}
			if len(call.Args) != 1 {
				t.Errorf("%s:%d uses variadic/raw batch Append with %d arguments", entry.Name(), position.Line, len(call.Args))
				return true
			}
			literal, ok := call.Args[0].(*ast.CompositeLit)
			if !ok {
				t.Errorf("%s:%d batch Append does not receive a typed row literal", entry.Name(), position.Line)
				return true
			}
			rowType, ok := literal.Type.(*ast.SelectorExpr)
			pkg, packageOK := rowType.X.(*ast.Ident)
			if !ok || !packageOK || pkg.Name != "periscopeingestdb" || !strings.HasSuffix(rowType.Sel.Name, "Row") {
				t.Errorf("%s:%d batch Append row is not owned by periscopeingestdb", entry.Name(), position.Line)
			}
			return true
		})
	}
}
