package control

import (
	"context"
	"net/http"
	"testing"
)

// IsActiveDVR / LookupActiveDVR are the guard every DVR cleanup site consults:
// an active DVR must never have its directory or segments deleted. They must be
// nil-safe (no manager, empty hash) and report hits/misses precisely.
func TestActiveDVRLookups(t *testing.T) {
	t.Run("nil manager and empty hash never report active", func(t *testing.T) {
		prev := dvrManager
		dvrManager = nil
		t.Cleanup(func() { dvrManager = prev })

		if IsActiveDVR("anything") {
			t.Fatal("nil manager must report not-active")
		}
		if _, ok := LookupActiveDVR("anything"); ok {
			t.Fatal("nil manager lookup must miss")
		}
	})

	t.Run("populated manager reports hit and miss", func(t *testing.T) {
		setupTestDVRManager(t)
		job := &DVRJob{DVRHash: "h1", InternalName: "stream1"}
		dvrManager.jobs["h1"] = job

		if IsActiveDVR("") {
			t.Fatal("empty hash must never report active")
		}
		if !IsActiveDVR("h1") {
			t.Fatal("h1 should be active")
		}
		if IsActiveDVR("h2") {
			t.Fatal("h2 was never added")
		}

		got, ok := LookupActiveDVR("h1")
		if !ok || got != job {
			t.Fatalf("LookupActiveDVR(h1) = %v,%v want the registered job", got, ok)
		}
		if _, ok := LookupActiveDVR("h2"); ok {
			t.Fatal("LookupActiveDVR(h2) must miss")
		}
	})
}

// newHTTPRequest is the thin context-bound request factory: a well-formed call
// carries the method and URL through; an invalid method surfaces the error.
func TestNewHTTPRequest(t *testing.T) {
	req, err := newHTTPRequest(context.Background(), http.MethodGet, "http://example/x", nil)
	if err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if req.Method != http.MethodGet || req.URL.String() != "http://example/x" {
		t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
	}

	if _, err := newHTTPRequest(context.Background(), "bad method", "http://example/x", nil); err == nil {
		t.Fatal("invalid method must error")
	}
}
