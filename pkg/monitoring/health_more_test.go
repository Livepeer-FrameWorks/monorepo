package monitoring

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPServiceHealthCheck_StatusBoundary(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantHealth string
	}{
		{"399 is healthy", 399, "healthy"},
		{"400 is unhealthy", 400, "unhealthy"},
		{"500 is unhealthy", 500, "unhealthy"},
		{"200 is healthy", 200, "healthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
			}))
			defer s.Close()
			res := HTTPServiceHealthCheck("svc", s.URL)()
			if res.Status != c.wantHealth {
				t.Fatalf("status %d: got %q, want %q", c.status, res.Status, c.wantHealth)
			}
		})
	}
}
