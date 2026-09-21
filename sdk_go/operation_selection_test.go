package frameworks

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Khan/genqlient/graphql"
)

func TestOperationSelectionAndRetrySafety(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name          string `json:"name"`
			Document      string `json:"document"`
			OperationName string `json:"operationName"`
			Kind          string `json:"kind"`
			SelectedName  string `json:"selectedName"`
			Invalid       bool   `json:"invalid"`
		} `json:"cases"`
	}
	loadFixture(t, "operation-selection.json", &fixture)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			kind, name, err := parseOperation(tc.Document, tc.OperationName)
			if tc.Invalid {
				if err == nil {
					t.Fatal("invalid selection accepted")
				}
			} else if err != nil || kind != tc.Kind || name != tc.SelectedName {
				t.Fatalf("selection = %q, %q, %v", kind, name, err)
			}
			transport := newScripted(map[string][]fixtureResponse{"*": {{Status: 503}, {Status: 503}, {Status: 503}}})
			client, err := NewClient(ClientOptions{URL: "https://selection.test/graphql", HTTPClient: &http.Client{Transport: transport}, DisableServerCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			recordSleeps(client)
			var data json.RawMessage
			err = client.MakeRequest(context.Background(), &graphql.Request{Query: tc.Document, OpName: tc.OperationName}, &graphql.Response{Data: &data})
			if err == nil {
				t.Fatal("expected request error")
			}
			attempts := 1
			if tc.Invalid || tc.Kind == "subscription" {
				attempts = 0
			} else if tc.Kind == "query" {
				attempts = 3
			}
			if len(transport.requests) != attempts {
				t.Fatalf("sent %d requests, want %d", len(transport.requests), attempts)
			}
			if attempts > 0 && transport.requests[0].OperationName != tc.SelectedName {
				t.Fatalf("sent wrong operation %q", transport.requests[0].OperationName)
			}
		})
	}
}
