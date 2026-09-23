package frameworks

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"
)

type operationsFixture struct {
	Operations []struct {
		Name      string                     `json:"name"`
		Kind      string                     `json:"kind"`
		Variables map[string]json.RawMessage `json:"variables"`
		Response  struct {
			Data json.RawMessage `json:"data"`
		} `json:"response"`
	} `json:"operations"`
}

type vars map[string]json.RawMessage

// arg decodes one fixture variable into the generated argument type; a
// variable the fixture leaves out is the zero value.
func arg[T any](v vars, name string) T {
	var out T
	if raw, ok := v[name]; ok {
		if err := json.Unmarshal(raw, &out); err != nil {
			panic(err)
		}
	}
	return out
}

// operationCall is one entry of operationCalls, which genclient generates
// into operation_calls_gen_test.go from the generated function signatures.
type operationCall struct {
	query string
	call  func(ctx context.Context, c *Client, v vars) (any, error)
}

// anyEvents adapts a typed subscription to the subscriptionCalls table.
func anyEvents[T any](seq iter.Seq2[*T, error]) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		for ev, err := range seq {
			if !yield(ev, err) {
				return
			}
		}
	}
}

// sentVariablesMatch reports whether the variables sent carry every fixture
// variable unchanged and nothing else but nulls.
func sentVariablesMatch(sent map[string]any, fixture vars) bool {
	for name, raw := range fixture {
		var want any
		_ = json.Unmarshal(raw, &want)
		if !jsonEqual(sent[name], want) {
			return false
		}
	}
	for name, v := range sent {
		if _, ok := fixture[name]; !ok && v != nil {
			return false
		}
	}
	return true
}

func TestGeneratedOperations(t *testing.T) {
	var fx operationsFixture
	loadFixture(t, "operations.json", &fx)

	names := []string{}
	for _, op := range fx.Operations {
		names = append(names, op.Name)
	}
	manifest := []string{}
	for name := range operations {
		manifest = append(manifest, name)
	}
	sort.Strings(names)
	sort.Strings(manifest)
	if !jsonEqual(names, manifest) {
		t.Fatalf("fixture operations %v differ from the manifest %v", names, manifest)
	}

	for _, op := range fx.Operations {
		t.Run(op.Name, func(t *testing.T) {
			if op.Kind == "subscription" {
				testSubscriptionOperation(t, op.Name, op.Variables, op.Response.Data)
				return
			}
			entry, ok := operationCalls[op.Name]
			if !ok {
				t.Fatalf("no call for %s in operationCalls", op.Name)
			}
			var sent struct {
				Query         string         `json:"query"`
				OperationName string         `json:"operationName"`
				Variables     map[string]any `json:"variables"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &sent)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": op.Response.Data})
			}))
			defer srv.Close()
			c, err := NewClient(ClientOptions{URL: srv.URL, DisableServerCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := entry.call(context.Background(), c, op.Variables)
			if err != nil {
				t.Fatalf("%s: %v", op.Name, err)
			}
			if sent.OperationName != op.Name || sent.Query != entry.query {
				t.Errorf("sent operation %q with a different document", sent.OperationName)
			}
			if !sentVariablesMatch(sent.Variables, op.Variables) {
				t.Errorf("sent variables %v, fixture %s", sent.Variables, mustJSON(op.Variables))
			}
			var want any
			_ = json.Unmarshal(op.Response.Data, &want)
			if !jsonEqual(resp, want) {
				t.Errorf("decoded response re-encodes as %s, want %s", mustJSON(resp), op.Response.Data)
			}
		})
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// testSubscriptionOperation subscribes through the operation's generated
// Subscribe function.
func testSubscriptionOperation(t *testing.T, name string, variables vars, data json.RawMessage) {
	t.Helper()
	var mu sync.Mutex
	var subscribed map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var msg struct {
				Type    string         `json:"type"`
				ID      string         `json:"id"`
				Payload map[string]any `json:"payload"`
			}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			switch msg.Type {
			case "connection_init":
				_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
			case "subscribe":
				mu.Lock()
				subscribed = msg.Payload
				mu.Unlock()
				_ = conn.WriteJSON(map[string]any{"id": msg.ID, "type": "next", "payload": map[string]any{"data": data}})
				_ = conn.WriteJSON(map[string]any{"id": msg.ID, "type": "complete"})
			}
		}
	}))
	defer srv.Close()
	sc, err := NewSubscriptionClient(SubscriptionOptions{URL: wsURL(srv.URL), Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	subscribe, ok := subscriptionCalls[name]
	if !ok {
		t.Fatalf("no call for %s in subscriptionCalls", name)
	}
	var events []any
	for ev, err := range subscribe(ctx, sc, variables) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	var want any
	_ = json.Unmarshal(data, &want)
	if len(events) != 1 || !jsonEqual(events[0], want) {
		t.Fatalf("events = %s, want [%s]", mustJSON(events), data)
	}
	mu.Lock()
	defer mu.Unlock()
	if subscribed["operationName"] != name {
		t.Errorf("subscribed with %v", subscribed["operationName"])
	}
}
