package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"frameworks/api_gateway/internal/mcp/introspection"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator"
)

func TestGenerateAndExecuteStreamsQuery(t *testing.T) {
	source, err := os.ReadFile("../../../../pkg/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Input: string(source)})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Error("caller auth was not forwarded")
		}
		var request struct {
			Query     string
			Variables map[string]any
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil {
			t.Error(decodeErr)
			w.WriteHeader(400)
			return
		}
		doc, errs := gqlparser.LoadQueryWithRules(schema, request.Query, nil)
		if len(errs) != 0 {
			t.Error(errs)
			w.WriteHeader(400)
			return
		}
		variables, coercionErr := validator.VariableValues(schema, doc.Operations[0], request.Variables)
		if coercionErr != nil {
			t.Error(coercionErr)
			w.WriteHeader(400)
			return
		}
		if variables["first"] != int64(50) {
			t.Errorf("default first = %#v", variables["first"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"streamsConnection":{"totalCount":0}}}`))
	}))
	defer server.Close()
	previous := introspectionClient
	introspectionClient = introspection.NewClient(server.URL, nil)
	t.Cleanup(func() { introspectionClient = previous })
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "caller-token")
	result, generated, err := handleGenerateQuery(ctx, GenerateQueryInput{FieldName: "streamsConnection"}, nil)
	if err != nil || result.IsError {
		t.Fatalf("generate_query: %v, %+v", err, result)
	}
	query := generated.(GenerateQueryResult)
	result, _, err = handleExecuteQuery(ctx, ExecuteQueryInput{Query: query.Query, Variables: query.Variables}, nil)
	if err != nil || result.IsError || !called {
		t.Fatalf("execute_query: %v, %+v, called=%v", err, result, called)
	}
}
