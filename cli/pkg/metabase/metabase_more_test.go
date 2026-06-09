package metabase

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDo_Headers(t *testing.T) {
	t.Parallel()
	var captured *http.Request
	c := client{
		baseURL: "http://mb.local",
		http: fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
			captured = req
			return jsonResponse(http.StatusOK, map[string]any{"ok": true})
		}),
	}

	t.Run("session header set when session present, api-key absent", func(t *testing.T) {
		c.sessionID = "sess"
		c.apiKey = ""
		_ = c.do(context.Background(), http.MethodGet, "/x", nil, nil)
		if captured.Header.Get("X-Metabase-Session") != "sess" {
			t.Fatalf("expected session header; got %q", captured.Header.Get("X-Metabase-Session"))
		}
		if captured.Header.Get("X-API-Key") != "" {
			t.Fatalf("api-key header must be absent when empty")
		}
	})

	t.Run("api-key header set when present, session absent", func(t *testing.T) {
		c.sessionID = ""
		c.apiKey = "key123"
		_ = c.do(context.Background(), http.MethodGet, "/x", nil, nil)
		if captured.Header.Get("X-API-Key") != "key123" {
			t.Fatalf("expected api-key header; got %q", captured.Header.Get("X-API-Key"))
		}
		if captured.Header.Get("X-Metabase-Session") != "" {
			t.Fatalf("session header must be absent when empty")
		}
	})

	t.Run("content-type set only when body present", func(t *testing.T) {
		c.sessionID = "sess"
		c.apiKey = ""
		_ = c.do(context.Background(), http.MethodGet, "/x", nil, nil)
		if captured.Header.Get("Content-Type") != "" {
			t.Fatalf("no body must omit Content-Type; got %q", captured.Header.Get("Content-Type"))
		}
		_ = c.do(context.Background(), http.MethodPost, "/x", map[string]any{"a": 1}, nil)
		if captured.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("body must set Content-Type json; got %q", captured.Header.Get("Content-Type"))
		}
	})
}

func TestDo_StatusBoundary(t *testing.T) {
	t.Parallel()
	mk := func(status int) error {
		c := client{
			baseURL: "http://mb.local",
			http: fakeHTTPClient(func(_ *http.Request) (*http.Response, error) {
				return jsonResponse(status, map[string]any{"msg": "x"})
			}),
		}
		return c.do(context.Background(), http.MethodGet, "/x", nil, nil)
	}
	// 200, 299 success; 199, 300 failure.
	if err := mk(200); err != nil {
		t.Errorf("200 should succeed; got %v", err)
	}
	if err := mk(299); err != nil {
		t.Errorf("299 should succeed; got %v", err)
	}
	if err := mk(300); err == nil {
		t.Errorf("300 must fail (>= 300)")
	}
	if err := mk(199); err == nil {
		t.Errorf("199 must fail (< 200)")
	}
}

func TestManagedDescription_EmptyVsExisting(t *testing.T) {
	t.Parallel()
	// Empty existing → marker only, no leading newlines.
	got := managedDescription("", "slug-x", "sha256:abc")
	if strings.HasPrefix(got, "\n") || strings.Contains(got, "\n\n<!--") {
		t.Fatalf("empty existing must be marker only; got %q", got)
	}
	if !strings.Contains(got, "slug-x") || !strings.Contains(got, "sha256:abc") {
		t.Fatalf("marker missing slug/hash; got %q", got)
	}
	// Non-empty existing → preserved + double-newline + marker.
	got2 := managedDescription("Human note.", "slug-x", "sha256:abc")
	if !strings.HasPrefix(got2, "Human note.\n\n<!--") {
		t.Fatalf("existing must be preserved before marker; got %q", got2)
	}
}

func TestManagedMetadata_RoundTrip(t *testing.T) {
	t.Parallel()
	desc := managedDescription("", "the-slug", "sha256:deadbeef")
	slug, hash, ok := managedMetadata(desc)
	if !ok || slug != "the-slug" || hash != "sha256:deadbeef" {
		t.Fatalf("round-trip failed: slug=%q hash=%q ok=%v", slug, hash, ok)
	}
	// Unmanaged description → not ok.
	if _, _, ok := managedMetadata("just a plain description"); ok {
		t.Fatal("plain description must not be managed")
	}
}

func TestEquivalentSQL(t *testing.T) {
	t.Parallel()
	if !equivalentSQL("SELECT  1\nFROM t", "SELECT 1 FROM t") {
		t.Fatal("whitespace-normalized SQL must be equivalent")
	}
	if equivalentSQL("SELECT 1", "SELECT 2") {
		t.Fatal("different SQL must not be equivalent")
	}
}

func TestSyncConflictCountsAndAPIKeyAuth(t *testing.T) {
	t.Parallel()
	specPath := writeSpec(t, []Card{testCard()})
	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		return metabaseFixtureResponse(t, req, metabaseCard{
			ID:           67,
			Name:         testCard().Name,
			Display:      "table",
			CollectionID: 4,
			DatasetQuery: datasetQuery{Database: 2, Type: "native", Native: nativeQuery{Query: "SELECT 1"}},
		})
	})

	// APIKey-only (no SessionID) must pass the auth gate and reach the conflict.
	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:    "http://metabase.local",
		APIKey:     "key-only",
		SpecPath:   specPath,
		Database:   "FrameWorks ClickHouse",
		Collection: "FrameWorks",
		HTTPClient: client,
	})
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("expected unmanaged conflict with api-key auth; got %v", err)
	}
	if summary.Conflicts != 1 {
		t.Fatalf("expected Conflicts=1; got %+v", summary)
	}
}

func TestSync_DryRunCreateSkipsDashboardWhenNoRemoteID(t *testing.T) {
	t.Parallel()
	// A brand-new card in DryRun mode is "created" without a real remote ID
	// (remote.ID stays 0). Even with a dashboard configured, it must NOT be
	// added (gate is remote.ID > 0). A search miss makes the card "not found".
	specPath := writeSpec(t, []Card{testCard()})
	client := fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/database":
			return jsonResponse(http.StatusOK, map[string]any{"data": []map[string]any{{"id": 2, "name": "FrameWorks ClickHouse"}}})
		case "/api/collection":
			return jsonResponse(http.StatusOK, []map[string]any{{"id": 4, "name": "FrameWorks"}})
		case "/api/dashboard":
			return jsonResponse(http.StatusOK, []map[string]any{{"id": 9, "name": "FrameWorks Periscope"}})
		case "/api/search":
			// No existing card → create path.
			return jsonResponse(http.StatusOK, map[string]any{"data": []map[string]any{}})
		default:
			return jsonResponse(http.StatusNotFound, nil)
		}
	})

	summary, err := Sync(context.Background(), SyncOptions{
		BaseURL:    "http://metabase.local",
		SessionID:  "s",
		SpecPath:   specPath,
		Database:   "FrameWorks ClickHouse",
		Collection: "FrameWorks",
		Dashboard:  "FrameWorks Periscope",
		DryRun:     true,
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("dry-run sync: %v", err)
	}
	if summary.Created != 1 {
		t.Fatalf("expected 1 created; got %+v", summary)
	}
	if summary.AddedToDashboard != 0 {
		t.Fatalf("dry-run create (remote.ID==0) must not add to dashboard; got %+v", summary)
	}
}

func TestSync_RejectsMissingAuth(t *testing.T) {
	t.Parallel()
	_, err := Sync(context.Background(), SyncOptions{BaseURL: "http://m.local"})
	if err == nil || !strings.Contains(err.Error(), "session id or API key") {
		t.Fatalf("missing both auth fields must error; got %v", err)
	}
}

func TestFindCardByName_SkipsNonCardModel(t *testing.T) {
	t.Parallel()
	getCardCalled := false
	c := client{
		baseURL: "http://m.local",
		http: fakeHTTPClient(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/api/search":
				// Same name, but model="dashboard" — must be skipped.
				return jsonResponse(http.StatusOK, map[string]any{"data": []map[string]any{
					{"id": 5, "name": "Widget", "model": "dashboard"},
				}})
			case "/api/card/5":
				getCardCalled = true
				return jsonResponse(http.StatusOK, metabaseCard{ID: 5, Name: "Widget"})
			default:
				return jsonResponse(http.StatusNotFound, nil)
			}
		}),
	}
	_, found, err := c.findCardByName(context.Background(), "Widget")
	if err != nil {
		t.Fatalf("findCardByName: %v", err)
	}
	if found {
		t.Fatal("dashboard model must not match as a card")
	}
	if getCardCalled {
		t.Fatal("must not fetch card for non-card model")
	}
}
