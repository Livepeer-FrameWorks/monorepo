package metabase

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRefusesUnmanagedCardConflict(t *testing.T) {
	specPath := writeSpec(t, []Card{testCard()})
	var writes []string
	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			writes = append(writes, req.Method+" "+req.URL.Path)
		}
		return metabaseFixtureResponse(t, req, metabaseCard{
			ID:           67,
			Name:         testCard().Name,
			Display:      "table",
			CollectionID: 4,
			DatasetQuery: datasetQuery{Database: 2, Type: "native", Native: nativeQuery{Query: "SELECT 1"}},
		})
	})

	_, err := Sync(context.Background(), SyncOptions{
		BaseURL:    "http://metabase.local",
		SessionID:  "session",
		SpecPath:   specPath,
		Database:   "FrameWorks ClickHouse",
		Collection: "FrameWorks",
		Dashboard:  "FrameWorks Periscope",
		HTTPClient: client,
	})
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("expected unmanaged conflict, got %v", err)
	}
	if len(writes) != 0 {
		t.Fatalf("unmanaged conflict should not write, got %v", writes)
	}
}

func TestSyncUpdatesManagedCardAndAddsItToDashboard(t *testing.T) {
	card := testCard()
	specPath := writeSpec(t, []Card{card})
	oldDescription := managedDescription("", card.Slug, "sha256:old")
	var cardUpdated bool
	var dashboardUpdated bool

	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPut && req.URL.Path == "/api/card/67":
			cardUpdated = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["name"] != card.Name {
				t.Fatalf("updated wrong card name: %#v", body["name"])
			}
			if !strings.Contains(body["description"].(string), managedMarkerPrefix) {
				t.Fatal("updated card missing managed marker")
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 67})
		case req.Method == http.MethodPost && req.URL.Path == "/api/dashboard/9/cards":
			dashboardUpdated = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["cardId"].(float64) != 67 {
				t.Fatalf("dashboard added wrong card id: %#v", body["cardId"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 101})
		default:
			return metabaseFixtureResponse(t, req, metabaseCard{
				ID:           67,
				Name:         card.Name,
				Display:      "table",
				Description:  oldDescription,
				CollectionID: 4,
				DatasetQuery: datasetQuery{Database: 2, Type: "native", Native: nativeQuery{Query: "SELECT 1"}},
			})
		}
	})

	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:    "http://metabase.local",
		SessionID:  "session",
		SpecPath:   specPath,
		Database:   "FrameWorks ClickHouse",
		Collection: "FrameWorks",
		Dashboard:  "FrameWorks Periscope",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cardUpdated || !dashboardUpdated {
		t.Fatalf("expected card and dashboard updates, card=%v dashboard=%v", cardUpdated, dashboardUpdated)
	}
	if summary.Updated != 1 || summary.AddedToDashboard != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestSyncAdoptsEquivalentUnmanagedCard(t *testing.T) {
	card := testCard()
	specPath := writeSpec(t, []Card{card})
	var adopted bool

	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPut && req.URL.Path == "/api/card/67" {
			adopted = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if _, ok := body["dataset_query"]; ok {
				t.Fatal("adoption should update description only")
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 67})
		}
		return metabaseFixtureResponse(t, req, metabaseCard{
			ID:           67,
			Name:         card.Name,
			Display:      "table",
			CollectionID: 4,
			DatasetQuery: datasetQuery{Database: 2, Type: "native", Native: nativeQuery{Query: card.Query}},
		})
	})

	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:    "http://metabase.local",
		SessionID:  "session",
		SpecPath:   specPath,
		Database:   "FrameWorks ClickHouse",
		Collection: "FrameWorks",
		Adopt:      true,
		HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !adopted || summary.Adopted != 1 {
		t.Fatalf("expected adoption, adopted=%v summary=%+v", adopted, summary)
	}
}

func testCard() Card {
	return Card{
		Slug:     "periscope.routing-decisions-by-selected-node",
		Name:     "Routing decisions by selected node",
		Display:  "table",
		Database: "FrameWorks ClickHouse",
		Query: `SELECT selected_node_id,
       count() AS decisions
FROM periscope.routing_decisions
GROUP BY selected_node_id`,
	}
}

func writeSpec(t *testing.T, cards []Card) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("cards:\n")
	for _, card := range cards {
		b.WriteString("  - slug: " + card.Slug + "\n")
		b.WriteString("    name: " + card.Name + "\n")
		b.WriteString("    display: " + card.Display + "\n")
		b.WriteString("    database: " + card.Database + "\n")
		b.WriteString("    query: |\n")
		for _, line := range strings.Split(card.Query, "\n") {
			b.WriteString("      " + line + "\n")
		}
	}
	path := filepath.Join(t.TempDir(), "cards.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func metabaseFixtureResponse(t *testing.T, req *http.Request, card metabaseCard) (*http.Response, error) {
	t.Helper()

	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/api/database":
		return jsonResponse(http.StatusOK, map[string]any{"data": []map[string]any{{"id": 2, "name": "FrameWorks ClickHouse"}}})
	case req.Method == http.MethodGet && req.URL.Path == "/api/collection":
		return jsonResponse(http.StatusOK, []map[string]any{{"id": 4, "name": "FrameWorks"}})
	case req.Method == http.MethodGet && req.URL.Path == "/api/dashboard":
		return jsonResponse(http.StatusOK, []map[string]any{{"id": 9, "name": "FrameWorks Periscope"}})
	case req.Method == http.MethodGet && req.URL.Path == "/api/search":
		return jsonResponse(http.StatusOK, map[string]any{"data": []map[string]any{{"id": 67, "name": card.Name, "model": "card"}}})
	case req.Method == http.MethodGet && req.URL.Path == "/api/card/67":
		return jsonResponse(http.StatusOK, card)
	case req.Method == http.MethodGet && req.URL.Path == "/api/dashboard/9":
		return jsonResponse(http.StatusOK, dashboard{ID: 9, Name: "FrameWorks Periscope", Dashcards: nil})
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": req.Method + " " + req.URL.Path})
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

	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatalf("decode body %s: %v", string(content), err)
	}
}
