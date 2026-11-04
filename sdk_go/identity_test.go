package frameworks

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Khan/genqlient/graphql"
	"github.com/Livepeer-FrameWorks/sdk-go/identity"
)

func TestDocumentIdentities(t *testing.T) {
	var fixture struct {
		Operations []struct{ Name, Document, Identity string }
	}
	loadFixture(t, "operations.json", &fixture)
	for _, op := range fixture.Operations {
		t.Run(op.Name, func(t *testing.T) {
			got, err := identity.Sum(op.Document, op.Name)
			if err != nil || got != op.Identity {
				t.Fatalf("identity %q, %v; want %q", got, err, op.Identity)
			}
		})
	}
}

func TestSameNameCustomDocumentGate(t *testing.T) {
	transport := newScripted(map[string][]fixtureResponse{
		"ServerInfo":          {{Status: 200, Body: []byte(`{"data":{"serverInfo":{"version":"v0.3.11","features":[]}}}`)}, {Status: 200, Body: []byte(`{"data":{"serverInfo":{"version":"v0.3.11","features":[]}}}`)}},
		"GetBillingLifecycle": {{Status: 200, Body: []byte(`{"data":{"tenant":{"id":"tenant"}}}`)}},
	})
	client, err := NewClient(ClientOptions{URL: "https://identity-go.test/graphql", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	err = client.MakeRequest(context.Background(), &graphql.Request{Query: "query GetBillingLifecycle { tenant { id } }"}, &graphql.Response{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = GetBillingLifecycle(context.Background(), client)
	var unsupported *UnsupportedOperationError
	if !errors.As(err, &unsupported) {
		t.Fatalf("packaged operation gate: %v", err)
	}
	if client.identityName(GetBillingLifecycle_Operation, "GetBillingLifecycle") != "GetBillingLifecycle" {
		t.Fatal("emitted document lost its compatibility identity")
	}
}

func TestSharedIdentityFixtures(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name, Document, Identity string
			OperationName            string `json:"operationName"`
		}
	}
	loadFixture(t, "document_identity.json", &fixture)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			hash, err := identity.Sum(c.Document, c.OperationName)
			if err != nil || hash != c.Identity {
				t.Fatalf("%s %v; want %s", hash, err, c.Identity)
			}
		})
	}
}
