package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// With the internal TLS listener carrying the API, the plain listener answers
// only the probes, so no bearer secret is accepted over plaintext.
func TestPlainListenerServesOnlyProbesBesideInternalTLS(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	plain := probesOnly(api)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/health", http.StatusTeapot},
		{http.MethodGet, "/ready", http.StatusTeapot},
		{http.MethodGet, "/v1/inventory", http.StatusNotFound},
		{http.MethodGet, "/v1/work", http.StatusNotFound},
		{http.MethodPost, "/v1/observations", http.StatusNotFound},
		{http.MethodGet, "/v1/candidates", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		plain.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.want {
			t.Errorf("%s %s on the plain listener = %d, want %d", tc.method, tc.path, response.Code, tc.want)
		}
	}
}
