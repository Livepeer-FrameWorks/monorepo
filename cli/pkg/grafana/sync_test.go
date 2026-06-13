package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func classicDashboard() []byte {
	return []byte(`{"uid":"storage-serving","title":"Storage & Asset Serving","id":42,"panels":[{"type":"timeseries","title":"x"}]}`)
}

func v2Dashboard() []byte {
	return []byte(`{"title":"FrameWorks Ops","elements":{},"layout":{"kind":"RowsLayout"}}`)
}

func TestSyncCreatesFolderDatasourcesAndDashboards(t *testing.T) {
	var folderCreated, promCreated, chCreated, dashboardPosted bool

	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		if user, pass, ok := req.BasicAuth(); !ok || user != "admin" || pass != "secret" {
			t.Fatalf("request not authenticated with admin basic auth: %s %s", req.Method, req.URL.Path)
		}
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/folders/frameworks":
			return jsonResponse(http.StatusNotFound, map[string]any{"message": "not found"})
		case req.Method == http.MethodPost && req.URL.Path == "/api/folders":
			folderCreated = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["uid"] != "frameworks" || body["title"] != "FrameWorks" {
				t.Fatalf("folder payload wrong: %#v", body)
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 2, "uid": "frameworks"})
		case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/api/datasources/uid/"):
			return jsonResponse(http.StatusNotFound, map[string]any{"message": "not found"})
		case req.Method == http.MethodPost && req.URL.Path == "/api/datasources":
			var body map[string]any
			decodeBody(t, req.Body, &body)
			switch body["uid"] {
			case "victoriametrics":
				promCreated = true
				if body["url"] != "http://10.10.0.1:8428" {
					t.Fatalf("victoriametrics datasource URL wrong: %#v", body["url"])
				}
			case "clickhouse":
				chCreated = true
				jsonData := body["jsonData"].(map[string]any)
				if jsonData["username"] != "frameworks_analytics" {
					t.Fatalf("clickhouse datasource user wrong: %#v", jsonData["username"])
				}
			default:
				t.Fatalf("unexpected datasource created: %#v", body["uid"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 5})
		case req.Method == http.MethodPost && req.URL.Path == "/api/dashboards/db":
			dashboardPosted = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["overwrite"] != true || body["folderUid"] != "frameworks" {
				t.Fatalf("dashboard payload wrong: overwrite=%#v folderUid=%#v", body["overwrite"], body["folderUid"])
			}
			dash := body["dashboard"].(map[string]any)
			if dash["id"] != nil {
				t.Fatalf("dashboard id must be nulled, got %#v", dash["id"])
			}
			if dash["uid"] != "storage-serving" {
				t.Fatalf("wrong dashboard uid: %#v", dash["uid"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"status": "success"})
		default:
			return jsonResponse(http.StatusNotFound, map[string]any{"error": req.Method + " " + req.URL.Path})
		}
	})

	var out bytes.Buffer
	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:            "http://grafana.local",
		Username:           "admin",
		Password:           "secret",
		VictoriaMetricsURL: "http://10.10.0.1:8428",
		ClickHouse: &ClickHouseDatasource{
			Server:   "yuga-eu-1.internal",
			Port:     8123,
			Username: "frameworks_analytics",
			Password: "ro-pass",
		},
		Dashboards: []DashboardSource{
			{Name: "storage-serving.json", Content: classicDashboard()},
			{Name: "frameworks-ops.json", Content: v2Dashboard()},
		},
		HTTPClient: client,
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !folderCreated || !promCreated || !chCreated || !dashboardPosted {
		t.Fatalf("expected all creations, folder=%v prom=%v ch=%v dash=%v", folderCreated, promCreated, chCreated, dashboardPosted)
	}
	if summary.FoldersCreated != 1 || summary.DatasourcesCreated != 2 || summary.DashboardsSynced != 1 || summary.Skipped != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if !strings.Contains(out.String(), "skipping frameworks-ops.json") {
		t.Fatalf("V2 skip notice missing:\n%s", out.String())
	}
}

func TestSyncConvergesExistingDatasource(t *testing.T) {
	var updated bool

	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/folders/frameworks":
			return jsonResponse(http.StatusOK, map[string]any{"id": 2, "uid": "frameworks"})
		case req.Method == http.MethodGet && req.URL.Path == "/api/datasources/uid/victoriametrics":
			return jsonResponse(http.StatusOK, map[string]any{"id": 7, "uid": "victoriametrics"})
		case req.Method == http.MethodPut && req.URL.Path == "/api/datasources/uid/victoriametrics":
			updated = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["url"] != "http://10.10.0.1:8428" {
				t.Fatalf("converged URL wrong: %#v", body["url"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"uid": "victoriametrics"})
		default:
			return jsonResponse(http.StatusNotFound, map[string]any{"error": req.Method + " " + req.URL.Path})
		}
	})

	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:            "http://grafana.local",
		APIKey:             "glsa_token",
		VictoriaMetricsURL: "http://10.10.0.1:8428",
		HTTPClient:         client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated || summary.DatasourcesUpdated != 1 || summary.DatasourcesCreated != 0 {
		t.Fatalf("expected datasource convergence, updated=%v summary=%+v", updated, summary)
	}
}

func TestSyncDryRunWritesNothing(t *testing.T) {
	var writes []string

	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			writes = append(writes, req.Method+" "+req.URL.Path)
			return jsonResponse(http.StatusOK, map[string]any{"id": 1})
		}
		return jsonResponse(http.StatusNotFound, map[string]any{"message": "not found"})
	})

	var out bytes.Buffer
	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:            "http://grafana.local",
		Username:           "admin",
		Password:           "secret",
		VictoriaMetricsURL: "http://10.10.0.1:8428",
		ClickHouse:         &ClickHouseDatasource{Server: "ch.internal", Port: 8123, Username: "frameworks_analytics", Password: "x"},
		Dashboards:         []DashboardSource{{Name: "storage-serving.json", Content: classicDashboard()}},
		DryRun:             true,
		HTTPClient:         client,
		Out:                &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 0 {
		t.Fatalf("dry run must not write, got %v", writes)
	}
	if summary.FoldersCreated != 1 || summary.DatasourcesCreated != 2 || summary.DashboardsSynced != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	for _, want := range []string{`create folder "FrameWorks"`, `create datasource "victoriametrics"`, `create datasource "clickhouse"`, "sync dashboard storage-serving.json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out.String())
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func fakeHTTPClient(handler roundTripFunc) *http.Client {
	return &http.Client{Transport: handler}
}

func jsonResponse(status int, value any) (*http.Response, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(content)),
	}, nil
}

func decodeBody(t *testing.T, body io.Reader, target any) {
	t.Helper()

	if err := json.NewDecoder(body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
