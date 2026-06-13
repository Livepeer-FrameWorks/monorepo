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
		case req.Method == http.MethodGet && req.URL.Path == "/api/dashboard/9":
			return jsonResponse(http.StatusOK, map[string]any{
				"id": 9, "name": "FrameWorks Periscope",
				"dashcards": []map[string]any{{"id": 31, "card_id": 50, "row": 0, "col": 0, "size_x": 24, "size_y": 4}},
			})
		case req.Method == http.MethodPut && req.URL.Path == "/api/dashboard/9":
			dashboardUpdated = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			dashcards := body["dashcards"].([]any)
			if len(dashcards) != 2 {
				t.Fatalf("PUT must keep existing dashcards and append, got %d entries", len(dashcards))
			}
			if existing := dashcards[0].(map[string]any); existing["card_id"].(float64) != 50 {
				t.Fatalf("existing dashcard not preserved: %#v", existing)
			}
			added := dashcards[1].(map[string]any)
			if added["card_id"].(float64) != 67 {
				t.Fatalf("dashboard added wrong card id: %#v", added["card_id"])
			}
			if added["row"].(float64) != 4 {
				t.Fatalf("new dashcard must land below the existing grid, row=%#v", added["row"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 9})
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

func TestSyncCreatesMissingCollectionAndDashboard(t *testing.T) {
	card := testCard()
	specPath := writeSpec(t, []Card{card})
	var createdCollection, createdDashboard, createdCard bool

	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/collection":
			return jsonResponse(http.StatusOK, []map[string]any{{"id": 4, "name": "Something Else"}})
		case req.Method == http.MethodGet && req.URL.Path == "/api/dashboard":
			return jsonResponse(http.StatusOK, []map[string]any{})
		case req.Method == http.MethodPost && req.URL.Path == "/api/collection":
			createdCollection = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["name"] != "FrameWorks" {
				t.Fatalf("created collection with wrong name: %#v", body["name"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 11})
		case req.Method == http.MethodPost && req.URL.Path == "/api/dashboard":
			createdDashboard = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["name"] != "FrameWorks Tenants" {
				t.Fatalf("created dashboard with wrong name: %#v", body["name"])
			}
			if body["collection_id"].(float64) != 11 {
				t.Fatalf("dashboard not placed in created collection: %#v", body["collection_id"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 21})
		case req.Method == http.MethodGet && req.URL.Path == "/api/search":
			return jsonResponse(http.StatusOK, map[string]any{"data": []map[string]any{}})
		case req.Method == http.MethodPost && req.URL.Path == "/api/card":
			createdCard = true
			var body map[string]any
			decodeBody(t, req.Body, &body)
			if body["collection_id"].(float64) != 11 {
				t.Fatalf("card not placed in created collection: %#v", body["collection_id"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 67})
		case req.Method == http.MethodGet && req.URL.Path == "/api/dashboard/21":
			return jsonResponse(http.StatusOK, dashboard{ID: 21, Name: "FrameWorks Tenants"})
		case req.Method == http.MethodPut && req.URL.Path == "/api/dashboard/21":
			var body map[string]any
			decodeBody(t, req.Body, &body)
			added := body["dashcards"].([]any)[0].(map[string]any)
			if added["card_id"].(float64) != 67 {
				t.Fatalf("dashboard added wrong card id: %#v", added["card_id"])
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 21})
		default:
			return metabaseFixtureResponse(t, req, metabaseCard{})
		}
	})

	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:    "http://metabase.local",
		SessionID:  "session",
		SpecPath:   specPath,
		Database:   "FrameWorks ClickHouse",
		Collection: "FrameWorks",
		Dashboard:  "FrameWorks Tenants",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !createdCollection || !createdDashboard || !createdCard {
		t.Fatalf("expected collection/dashboard/card creation, got collection=%v dashboard=%v card=%v",
			createdCollection, createdDashboard, createdCard)
	}
	if summary.Created != 1 || summary.AddedToDashboard != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestSyncDryRunCreatesNothing(t *testing.T) {
	card := testCard()
	specPath := writeSpec(t, []Card{card})
	var writes []string

	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			writes = append(writes, req.Method+" "+req.URL.Path)
			return jsonResponse(http.StatusOK, map[string]any{"id": 99})
		}
		switch req.URL.Path {
		case "/api/collection":
			return jsonResponse(http.StatusOK, []map[string]any{})
		case "/api/dashboard":
			return jsonResponse(http.StatusOK, []map[string]any{})
		case "/api/search":
			return jsonResponse(http.StatusOK, map[string]any{"data": []map[string]any{}})
		default:
			return metabaseFixtureResponse(t, req, metabaseCard{})
		}
	})

	var out bytes.Buffer
	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:    "http://metabase.local",
		SessionID:  "session",
		SpecPath:   specPath,
		Database:   "FrameWorks ClickHouse",
		Collection: "FrameWorks",
		Dashboard:  "FrameWorks Tenants",
		DryRun:     true,
		HTTPClient: client,
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 0 {
		t.Fatalf("dry run must not write, got %v", writes)
	}
	if summary.Created != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	for _, want := range []string{`create collection "FrameWorks"`, `create dashboard "FrameWorks Tenants"`, `create card`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out.String())
		}
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
