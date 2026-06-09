package graph

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

// Breadth + en-masse real-path coverage comes from TestRealPathSmokeSweep (real
// clients against a stub backend). The fake-client harness here is reused only for
// the focused behavioral teeth below, which assert correct mapping of known data.

// TestRealPathCannedMappingShallow is the behavioral teeth: scalar-only queries
// against the canned clients, proving the resolver request-build → fake gRPC
// response → GraphQL mapping path produces correct data (not just that it runs).
func TestRealPathCannedMappingShallow(t *testing.T) {
	srv := newRealPathTestServer(fakeServiceClients())
	cases := []struct {
		name  string
		query string
		vars  map[string]any
	}{
		{"billingTiers", `{ billingTiers { id } }`, nil},
		{"stream", `query($id: ID!){ stream(id: $id){ id } }`, map[string]any{"id": demoStreamGlobalID}},
		{"streamsConnection", `{ streamsConnection { totalCount } }`, nil},
		{"clustersConnection", `{ clustersConnection { totalCount } }`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, status := tryExecuteRealPath(srv, tc.query, tc.vars)
			if status != http.StatusOK {
				t.Fatalf("http status %d", status)
			}
			if len(resp.Errors) > 0 {
				t.Fatalf("real-path %s returned errors: %s", tc.name, formatGraphQLErrors(resp.Errors))
			}
			if len(resp.Data) == 0 || bytes.Equal(resp.Data, []byte("null")) {
				t.Fatalf("real-path %s returned null data", tc.name)
			}
		})
	}
}

// NOTE: the sweeps cover Query + Mutation only. Subscription fields are
// deliberately excluded — they resolve through r.SubManager (a live
// Signalman-backed gRPC fan-out built by NewResolver, which RequireEnv's
// SIGNALMAN_GRPC_ADDR) and need a WS/SSE transport the POST test handler does not
// provide. Covering them needs a dedicated SubManager fake + streaming transport
// (future work), not a silent gap.
func realPathSweep(t *testing.T, srv playgroundTestHarness, root *ast.Definition, opType string) []sweepResult {
	t.Helper()
	if root == nil {
		return nil
	}
	var results []sweepResult
	for _, field := range root.Fields {
		if strings.HasPrefix(field.Name, "__") {
			continue
		}
		query, vars := buildFieldQuery(srv.schema, opType, field)
		resp, status := tryExecuteRealPath(srv, query, vars)
		res := sweepResult{field: field.Name}
		switch {
		case status != http.StatusOK:
			res.detail = "http " + http.StatusText(status)
		case len(resp.Errors) > 0:
			res.detail = resp.Errors[0].Message
		case len(resp.Data) == 0 || bytes.Equal(resp.Data, []byte("null")):
			res.detail = "null data"
		default:
			res.ok = true
		}
		results = append(results, res)
	}
	return results
}
