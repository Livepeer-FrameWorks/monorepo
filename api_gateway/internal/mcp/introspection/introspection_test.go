package introspection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

// --- pure helpers over an in-memory Schema (no HTTP) ---

func sampleSchema() *Schema {
	return &Schema{
		QueryType:    &TypeName{Name: "Query"},
		MutationType: &TypeName{Name: "Mutation"},
		Types: []FullType{
			{Kind: "OBJECT", Name: "Query", Fields: []Field{
				{Name: "__typename", Type: named("SCALAR", "String")},
				{Name: "stream", Description: "one stream", Type: named("OBJECT", "Stream"), Args: []InputValue{
					{Name: "id", Type: wrap("NON_NULL", named("SCALAR", "ID"))},
				}},
				{Name: "status", Type: named("ENUM", "StreamState")},
			}},
			{Kind: "OBJECT", Name: "Mutation", Fields: []Field{
				{Name: "createStream", Type: named("OBJECT", "Stream")},
			}},
			{Kind: "OBJECT", Name: "Stream", Fields: []Field{
				{Name: "id", Type: wrap("NON_NULL", named("SCALAR", "ID"))},
				{Name: "name", Type: named("SCALAR", "String")},
			}},
			{Kind: "ENUM", Name: "StreamState", EnumValues: []EnumValue{{Name: "LIVE"}, {Name: "OFFLINE"}}},
		},
	}
}

func TestRootTypeForOperation(t *testing.T) {
	s := sampleSchema()
	for _, op := range []string{"query", "mutation"} {
		rt, err := rootTypeForOperation(s, op)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if rt == nil {
			t.Fatalf("%s: nil root type", op)
		}
	}
	if _, err := rootTypeForOperation(s, "subscription"); err == nil {
		t.Fatal("subscription: want error (schema has no subscription type)")
	}
	if _, err := rootTypeForOperation(s, "bogus"); err == nil {
		t.Fatal("bogus op: want error")
	}
}

func TestBuildTypeIndexAndFindField(t *testing.T) {
	idx := buildTypeIndex(sampleSchema())
	if idx["Stream"] == nil || idx["Query"] == nil {
		t.Fatal("index missing expected types")
	}
	stream := idx["Stream"]
	if findFieldByName(stream.Fields, "name") == nil {
		t.Fatal("expected to find field name on Stream")
	}
	if findFieldByName(stream.Fields, "nope") != nil {
		t.Fatal("did not expect to find nonexistent field")
	}
}

func TestIsScalarOrEnum(t *testing.T) {
	idx := buildTypeIndex(sampleSchema())
	if !isScalarOrEnum("String", idx) {
		t.Fatal("String should be scalar")
	}
	if !isScalarOrEnum("StreamState", idx) {
		t.Fatal("StreamState enum should count")
	}
	if isScalarOrEnum("Stream", idx) {
		t.Fatal("Stream object should not be scalar/enum")
	}
}

func TestDefaultValueString(t *testing.T) {
	if defaultValueString(nil) != "" {
		t.Fatal("nil → empty string")
	}
	if defaultValueString(strptr("5")) != "5" {
		t.Fatal("ptr → its value")
	}
}

// --- end-to-end via httptest: refresh, cache, traversal, execute ---

// introspectJSON renders sampleSchema as an introspection response.
func introspectJSON(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(IntrospectionResult{Data: IntrospectionData{Schema: *sampleSchema()}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func authCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "test.jwt.token")
}

func TestGetSchema_RefreshCachesAndServesTraversal(t *testing.T) {
	var hits int
	body := introspectJSON(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if got := r.Header.Get("Authorization"); got != "Bearer test.jwt.token" {
			t.Errorf("auth header = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	ctx := authCtx()

	// First call fetches; second is served from cache (TTL not elapsed).
	root, err := c.GetRootFields(ctx, "query", 2)
	if err != nil {
		t.Fatalf("GetRootFields: %v", err)
	}
	// __typename is filtered out; stream + status remain.
	var names []string
	for _, f := range root {
		names = append(names, f.Name)
	}
	if len(root) != 2 {
		t.Fatalf("root fields = %v, want 2 (internal __ filtered)", names)
	}

	// GetType on an enum returns its values, served from the same cached schema.
	ts, err := c.GetType(ctx, "StreamState", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts.EnumValues) != 2 {
		t.Fatalf("enum values = %v, want 2", ts.EnumValues)
	}
	if hits != 1 {
		t.Fatalf("backend hit %d times, want 1 (schema cached across calls)", hits)
	}
}

func TestFindFieldPath(t *testing.T) {
	body := introspectJSON(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	ctx := authCtx()

	// query.stream.name traverses object → object → scalar.
	fs, err := c.FindFieldPath(ctx, "query", "stream.name", 2)
	if err != nil {
		t.Fatalf("FindFieldPath: %v", err)
	}
	if fs.Name != "name" {
		t.Fatalf("resolved field = %q, want name", fs.Name)
	}

	if _, err := c.FindFieldPath(ctx, "query", "stream.nope", 2); err == nil {
		t.Fatal("want error for missing leaf field")
	}
	if _, err := c.FindFieldPath(ctx, "query", "", 2); err == nil {
		t.Fatal("want error for empty path")
	}
	if _, err := c.FindFieldPath(ctx, "query", "name.deeper", 2); err == nil {
		t.Fatal("want error traversing into a scalar")
	}
}

func TestGetSchema_MissingAuthFails(t *testing.T) {
	c := NewClient("http://unused", nil)
	if _, err := c.GetRootFields(context.Background(), "query", 1); err == nil {
		t.Fatal("want error when auth context is absent")
	}
}

func TestRefresh_PropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	if _, err := c.GetRootFields(authCtx(), "query", 1); err == nil {
		t.Fatal("want error on non-200 introspection response")
	}
}

func TestRefresh_PropagatesGraphQLErrors(t *testing.T) {
	body, _ := json.Marshal(IntrospectionResult{Errors: []IntrospectionError{{Message: "nope"}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	if _, err := c.GetRootFields(authCtx(), "query", 1); err == nil {
		t.Fatal("want error when introspection returns GraphQL errors")
	}
}

func TestExecuteQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if _, ok := payload["query"]; !ok {
			t.Error("expected query in body")
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)

	raw, err := c.ExecuteQuery(authCtx(), "{ ok }", map[string]any{"v": 1})
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected non-empty raw response")
	}

	if _, err := c.ExecuteQuery(context.Background(), "{ ok }", nil); err == nil {
		t.Fatal("want auth error without token")
	}
}
