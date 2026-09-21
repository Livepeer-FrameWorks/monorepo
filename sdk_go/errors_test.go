package frameworks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/Khan/genqlient/graphql"
)

type errorsFixture struct {
	Cases []struct {
		Name         string          `json:"name"`
		Response     fixtureResponse `json:"response"`
		ExpectResult *struct {
			Field   string   `json:"field"`
			Success []string `json:"success"`
		} `json:"expectResult"`
		Error  map[string]any  `json:"error"`
		Result json.RawMessage `json:"result"`
	} `json:"cases"`
}

// fixtureUnionMember stands in for a generated union member: the fixture's
// result unions are checked through ExpectResult with a success type that
// matches on __typename.
type fixtureUnionMember map[string]any

type fixtureSuccess struct{ fixtureUnionMember }

func TestErrorShapes(t *testing.T) {
	var fx errorsFixture
	loadFixture(t, "errors.json", &fx)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			transport := newScripted(map[string][]fixtureResponse{"*": {tc.Response}})
			c, err := NewClient(ClientOptions{URL: "https://errors.test/graphql", HTTPClient: &http.Client{Transport: transport}, Retry: &RetryPolicy{MaxAttempts: 1}, DisableServerCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]map[string]any
			err = c.MakeRequest(context.Background(), &graphql.Request{Query: "query ErrorProbe { a }", OpName: "ErrorProbe"}, &graphql.Response{Data: &data})
			var result any
			if err == nil && tc.ExpectResult != nil {
				member := data[tc.ExpectResult.Field]
				var v any = fixtureUnionMember(member)
				typename, _ := member["__typename"].(string)
				for _, s := range tc.ExpectResult.Success {
					if s == typename {
						v = fixtureSuccess{fixtureUnionMember(member)}
					}
				}
				var success fixtureSuccess
				success, err = ExpectResult[fixtureSuccess](v)
				result = map[string]any(success.fixtureUnionMember)
			}
			if tc.Error != nil {
				checkError(t, err, tc.Error)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var want any
			_ = json.Unmarshal(tc.Result, &want)
			got, _ := json.Marshal(result)
			var gotAny any
			_ = json.Unmarshal(got, &gotAny)
			if !reflect.DeepEqual(gotAny, want) {
				t.Errorf("result = %s, want %s", got, tc.Result)
			}
		})
	}
}

func TestSchemaMismatchIsAlsoAGraphQLError(t *testing.T) {
	_, err, _ := classify(422, "", []byte(`{"data":null,"errors":[{"message":"x","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`))
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("errors.As(*GraphQLError) failed for %T", err)
	}
}

func TestExpectResultOnGeneratedUnion(t *testing.T) {
	var resp CreateStreamResponse
	body := `{"createStream":{"__typename":"ValidationError","message":"name is required","code":"REQUIRED","field":"name","constraint":null}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	_, resultErr := ExpectResult[*CreateStreamCreateStream](resp.CreateStream)
	checkError(t, resultErr, map[string]any{"kind": "ResultError", "typename": "ValidationError", "message": "name is required", "code": "REQUIRED", "field": "name"})

	body = `{"createStream":{"__typename":"Stream","id":"s1","streamId":"s1","name":"n","playbackId":"p","record":false,"ingestMode":"PUSH","createdAt":"2026-09-19T14:03:27Z","updatedAt":"2026-09-19T14:03:27Z","monitoring":"INHERIT"}}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	stream, err := ExpectResult[*CreateStreamCreateStream](resp.CreateStream)
	if err != nil || stream.GetId() != "s1" {
		t.Fatalf("stream = %v, %v", stream, err)
	}
}
