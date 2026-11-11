package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDoRetriesAuthenticationOnce(t *testing.T) {
	for _, stillRejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "still rejected"}[stillRejected], func(t *testing.T) {
			var tokens []string
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tokens = append(tokens, r.Header.Get("Authorization"))
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				requests = append(requests, body)
				if len(tokens) == 1 || stillRejected {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
			}))
			defer server.Close()
			client := New(server.URL)
			refreshes := 0
			client.refresh = func(_ context.Context, origin, rejected string) (string, error) {
				refreshes++
				if origin != server.URL || rejected != "old" {
					t.Errorf("origin=%s rejected=%s", origin, rejected)
				}
				return "new", nil
			}
			var out struct {
				OK bool `json:"ok"`
			}
			err := client.Do(context.Background(), "query($id:ID!){ok}", "old", map[string]any{"id": "item"}, &out)
			if (err != nil) != stillRejected || refreshes != 1 || !reflect.DeepEqual(tokens, []string{"Bearer old", "Bearer new"}) {
				t.Fatalf("err=%v refreshes=%d tokens=%v", err, refreshes, tokens)
			}
			if !reflect.DeepEqual(requests[0], requests[1]) {
				t.Fatal("retry changed the operation")
			}
			if !stillRejected && !out.OK {
				t.Fatal("missing response data")
			}
		})
	}
}

func TestDoDoesNotRefreshPublicOrForbiddenRequest(t *testing.T) {
	for _, tc := range []struct {
		token string
		code  int
	}{{"", 401}, {"user", 403}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.code) }))
		client := New(server.URL)
		client.refresh = func(_ context.Context, _, _ string) (string, error) {
			t.Error("request must not trigger renewal")
			return "", nil
		}
		if err := client.Do(context.Background(), "{ok}", tc.token, nil, nil); err == nil {
			t.Fatal("expected rejection")
		}
		server.Close()
	}
}
