package main

import (
	"context"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestRecoveredOperationErrorTerminates(t *testing.T) {
	ctx := context.Background()
	handler := recoverOperation(ctx, func(context.Context) graphql.ResponseHandler {
		panic("operation fixture")
	}, logging.NewLogger())
	response := handler(ctx)
	if response == nil || len(response.Errors) != 1 || response.Errors[0].Message != "internal error" {
		t.Fatalf("recovered response = %+v", response)
	}
	if response := handler(ctx); response != nil {
		t.Fatal("recovered operation keeps producing errors instead of finishing")
	}
}

func TestQueryDepthErrorTerminates(t *testing.T) {
	op := &ast.OperationDefinition{Operation: ast.Query, SelectionSet: ast.SelectionSet{
		&ast.Field{Name: "viewer", SelectionSet: ast.SelectionSet{&ast.Field{Name: "name"}}},
	}}
	ctx := graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: op, Doc: &ast.QueryDocument{Operations: ast.OperationList{op}},
	})
	handler := queryDepthLimit(1)(ctx, func(context.Context) graphql.ResponseHandler {
		t.Fatal("excessive-depth operation reached resolver")
		return nil
	})
	response := handler(ctx)
	if response == nil || len(response.Errors) != 1 || response.Errors[0].Message != "query exceeds maximum depth of 1 (got 2)" {
		t.Fatalf("depth response = %+v", response)
	}
	if response := handler(ctx); response != nil {
		t.Fatal("excessive-depth operation keeps producing errors instead of finishing")
	}
}
