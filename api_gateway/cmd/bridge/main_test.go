package main

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestIsIntrospectionOperationAllFields(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "__schema"},
			&ast.Field{Name: "__type"},
		},
	}

	if !isIntrospectionOperation(op) {
		t.Fatal("expected introspection operation to be recognized")
	}
}

func TestIsIntrospectionOperationMixedFields(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "__schema"},
			&ast.Field{Name: "streamsConnection"},
		},
	}

	if isIntrospectionOperation(op) {
		t.Fatal("expected mixed fields to disable introspection bypass")
	}
}

func TestIsIntrospectionOperationInlineFragment(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.InlineFragment{
				SelectionSet: ast.SelectionSet{
					&ast.Field{Name: "__schema"},
				},
			},
		},
	}

	if isIntrospectionOperation(op) {
		t.Fatal("expected inline fragments to disable introspection bypass")
	}
}
