package graph

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/demo"

	"github.com/vektah/gqlparser/v2/ast"
)

// reachableTypes returns every named type reachable from the fields of root,
// following field types, union members, and interface implementations.
func reachableTypes(schema *ast.Schema, roots ...*ast.FieldDefinition) map[string]bool {
	seen := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		def := schema.Types[name]
		if def == nil {
			return
		}
		seen[name] = true
		for _, f := range def.Fields {
			visit(f.Type.Name())
		}
		for _, member := range def.Types {
			visit(member)
		}
		if def.Kind == ast.Interface {
			for _, impl := range schema.GetPossibleTypes(def) {
				visit(impl.Name)
			}
		}
	}
	for _, field := range roots {
		visit(field.Type.Name())
	}
	return seen
}

// The signing secret type is reachable only from the createWebhookEndpoint
// and rotateWebhookEndpointSecret results: no query, subscription, or other
// mutation can return it, and the endpoint type carries no secret field.
func TestWebhookSecretReachableOnlyFromCreateAndRotate(t *testing.T) {
	schema := newPlaygroundTestServer().schema
	const secretType = "WebhookEndpointSecret"

	readRoots := append(ast.FieldList{}, schema.Query.Fields...)
	if schema.Subscription != nil {
		readRoots = append(readRoots, schema.Subscription.Fields...)
	}
	if reachableTypes(schema, readRoots...)[secretType] {
		t.Fatalf("%s is reachable from a query or subscription", secretType)
	}

	var returning []string
	for _, field := range schema.Mutation.Fields {
		if reachableTypes(schema, field)[secretType] {
			returning = append(returning, field.Name)
		}
	}
	sort.Strings(returning)
	if strings.Join(returning, ",") != "createWebhookEndpoint,rotateWebhookEndpointSecret" {
		t.Fatalf("mutations returning %s = %v, want createWebhookEndpoint and rotateWebhookEndpointSecret", secretType, returning)
	}

	for _, typeName := range []string{"WebhookEndpoint", "WebhookDelivery", "WebhookDeliveryAttempt", "WebhookTestResult"} {
		for _, field := range schema.Types[typeName].Fields {
			if strings.EqualFold(field.Name, "secret") {
				t.Fatalf("%s.%s exposes a secret", typeName, field.Name)
			}
		}
	}
}

// webhookDelivery and the deliveries connection run through the generated
// schema in demo mode and both return the delivery's attempt history.
func TestWebhookDeliveryExecutesInDemoMode(t *testing.T) {
	srv := newPlaygroundTestServer()
	resp := executeGraphQL(t, srv, `query($id: ID!, $endpointId: ID!) {
		webhookDelivery(id: $id) { id status attempts attemptHistory { attemptNumber statusCode errorClass } }
		webhookDeliveriesConnection(endpointId: $endpointId, statuses: [FAILED]) { totalCount nodes { id attemptHistory { id } } }
		webhookEndpoint(id: $endpointId) { id status eventTypes }
	}`, map[string]any{"id": "demo_webhook_delivery_003", "endpointId": demo.DemoWebhookEndpointID})
	if len(resp.Errors) > 0 {
		t.Fatalf("errors: %+v", resp.Errors)
	}
	var data struct {
		WebhookDelivery struct {
			Status         string `json:"status"`
			Attempts       int    `json:"attempts"`
			AttemptHistory []struct {
				AttemptNumber int `json:"attemptNumber"`
			} `json:"attemptHistory"`
		} `json:"webhookDelivery"`
		WebhookDeliveriesConnection struct {
			TotalCount int `json:"totalCount"`
			Nodes      []struct {
				ID             string            `json:"id"`
				AttemptHistory []json.RawMessage `json:"attemptHistory"`
			} `json:"nodes"`
		} `json:"webhookDeliveriesConnection"`
		WebhookEndpoint struct {
			ID string `json:"id"`
		} `json:"webhookEndpoint"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.WebhookDelivery.Status != "FAILED" || len(data.WebhookDelivery.AttemptHistory) != data.WebhookDelivery.Attempts || data.WebhookDelivery.Attempts == 0 {
		t.Fatalf("webhookDelivery = %+v", data.WebhookDelivery)
	}
	conn := data.WebhookDeliveriesConnection
	if conn.TotalCount != 1 || len(conn.Nodes) != 1 || conn.Nodes[0].ID != "demo_webhook_delivery_003" || len(conn.Nodes[0].AttemptHistory) != data.WebhookDelivery.Attempts {
		t.Fatalf("webhookDeliveriesConnection = %+v", conn)
	}
	if data.WebhookEndpoint.ID != demo.DemoWebhookEndpointID {
		t.Fatalf("webhookEndpoint = %+v", data.WebhookEndpoint)
	}
}
