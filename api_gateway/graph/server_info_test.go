package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/platformfeatures"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

// serverInfo is the field clients probe before signing in, so it must answer a
// request that carries no identity at all — no token, no demo flag — and answer
// it from process state, without reaching a backend client.
func TestServerInfoAnswersWithoutAnyIdentity(t *testing.T) {
	srv := newPlaygroundTestServer()
	query := readFile(t, filepath.Join(findRepoRoot(t), "pkg", "graphql", "operations", "queries", "GetServerInfo.gql"))

	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	ctx := context.Background()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/graphql", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(ctx, ctxkeys.KeyHTTPRequest, req))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data struct {
			ServerInfo map[string]json.RawMessage `json:"serverInfo"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\n%s", err, rec.Body.String())
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("anonymous serverInfo returned errors: %+v", resp.Errors)
	}

	keys := make([]string, 0, len(resp.Data.ServerInfo))
	for key := range resp.Data.ServerInfo {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "features" || keys[1] != "version" {
		t.Fatalf("serverInfo keys = %v, want exactly version and features", keys)
	}

	var gotVersion string
	if err := json.Unmarshal(resp.Data.ServerInfo["version"], &gotVersion); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if gotVersion != version.Version {
		t.Errorf("version = %q, want the build's release %q", gotVersion, version.Version)
	}
	var gotFeatures []string
	if err := json.Unmarshal(resp.Data.ServerInfo["features"], &gotFeatures); err != nil {
		t.Fatalf("decode features: %v", err)
	}
	want := platformfeatures.Shipped()
	if len(gotFeatures) != len(want) {
		t.Fatalf("features = %v, want the shipped registry slugs %v", gotFeatures, want)
	}
	for i := range want {
		if gotFeatures[i] != want[i] {
			t.Fatalf("features = %v, want %v", gotFeatures, want)
		}
	}
}

// The type carries nothing beyond the release and the feature slugs: no commit,
// build date, component, cluster, region, or roadmap row may ride along on a
// field anyone can read.
func TestServerInfoTypeExposesOnlyVersionAndFeatures(t *testing.T) {
	srv := newPlaygroundTestServer()
	def := srv.schema.Types["ServerInfo"]
	if def == nil {
		t.Fatal("ServerInfo type missing from the schema")
	}
	var fields []string
	for _, field := range def.Fields {
		fields = append(fields, field.Name)
	}
	sort.Strings(fields)
	if len(fields) != 2 || fields[0] != "features" || fields[1] != "version" {
		t.Fatalf("ServerInfo fields = %v, want exactly version and features", fields)
	}
}
