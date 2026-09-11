package graph

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"frameworks/api_gateway/internal/demo"
)

func TestPlacementDemoCanonicalOperationsExecuteWithoutServiceClients(t *testing.T) {
	server := newPlaygroundTestServer()
	root := filepath.Join(findRepoRoot(t), "pkg", "graphql", "operations")
	fragments := readFragments(t, filepath.Join(root, "fragments"))
	scope := map[string]any{"kind": "TENANT"}
	change := map[string]any{"scope": scope, "expectedRevision": "0", "expectedParentRevision": "0", "updates": []any{map[string]any{"verb": "SERVE", "kind": "CLEAR"}}}
	apply := map[string]any{"scope": scope, "expectedRevision": "0", "expectedParentRevision": "0", "updates": change["updates"], "reviewToken": "demo-preview-only", "idempotencyKey": "demo-change", "acknowledgedWarningIds": []string{}}
	consent := map[string]any{"clusterId": demo.DemoSelfHostedCluster, "expectedRevision": "0", "allowIngest": true, "allowServe": false, "allowExternalSource": false}
	consentApply := map[string]any{"clusterId": demo.DemoSelfHostedCluster, "expectedRevision": "0", "allowIngest": true, "allowServe": false, "allowExternalSource": false, "reviewToken": "demo-preview-only", "idempotencyKey": "demo-change", "acknowledgedWarningIds": []string{}}
	for _, tc := range []struct {
		file, field, typename string
		variables             map[string]any
	}{
		{"queries/GetMediaPlacementPolicy.gql", "mediaPlacementPolicy", "MediaPlacementPolicyState", map[string]any{"scope": scope}},
		{"queries/GetMediaPlacementOptions.gql", "mediaPlacementOptions", "MediaPlacementOptionsConnection", map[string]any{"scope": scope}},
		{"queries/PreviewMediaPlacement.gql", "previewMediaPlacement", "MediaPlacementPreview", map[string]any{"input": map[string]any{"scope": scope, "verb": "SERVE", "streamId": demo.DemoStreamID, "coordinates": map[string]any{"latitude": 37.77, "longitude": -122.42}}}},
		{"queries/ReviewMediaPlacementChange.gql", "reviewMediaPlacementChange", "MediaPlacementReview", map[string]any{"input": change}},
		{"queries/GetClusterMediaConsent.gql", "clusterMediaConsent", "MediaCapacityConsent", map[string]any{"clusterId": demo.DemoSelfHostedCluster}},
		{"queries/ReviewClusterMediaConsentChange.gql", "reviewClusterMediaConsentChange", "MediaPlacementReview", map[string]any{"input": consent}},
		{"mutations/ApplyMediaPlacementChange.gql", "applyMediaPlacementChange", "MediaPlacementError", map[string]any{"input": apply}},
		{"mutations/ApplyClusterMediaConsentChange.gql", "applyClusterMediaConsentChange", "MediaPlacementError", map[string]any{"input": consentApply}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			query := stripClientDirectives(appendRequiredFragments(t, readFile(t, filepath.Join(root, tc.file)), fragments))
			response := executeGraphQL(t, server, query, tc.variables)
			if len(response.Errors) != 0 {
				t.Fatalf("canonical demo operation failed: %+v", response.Errors)
			}
			var data map[string]map[string]any
			if err := json.Unmarshal(response.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data[tc.field]["__typename"] != tc.typename {
				t.Fatalf("unexpected demo result: %s", response.Data)
			}
			if tc.typename == "MediaPlacementError" && data[tc.field]["code"] != "UNSUPPORTED" {
				t.Fatalf("demo mutation was not explicitly read-only: %s", response.Data)
			}
		})
	}
}
