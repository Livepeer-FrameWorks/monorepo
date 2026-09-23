package frameworks

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type connectionScript struct {
	OnInit      string            `json:"onInit"`
	CloseCode   int               `json:"closeCode"`
	CloseReason string            `json:"closeReason"`
	Next        []json.RawMessage `json:"next"`
	Error       json.RawMessage   `json:"error"`
	Then        string            `json:"then"`
	DropCode    int               `json:"dropCode"`
}

type subscriptionsFixture struct {
	MaxReconnects int `json:"maxReconnects"`
	Cases         []struct {
		Name        string             `json:"name"`
		Operation   string             `json:"operation"`
		Variables   vars               `json:"variables"`
		Tokens      []*string          `json:"tokens"`
		Connections []connectionScript `json:"connections"`
		Expect      struct {
			Connections   int               `json:"connections"`
			Authorization []*string         `json:"authorization"`
			EventIDs      []string          `json:"eventIds"`
			Events        []json.RawMessage `json:"events"`
			Subscribed    []map[string]any  `json:"subscribed"`
			Error         map[string]any    `json:"error"`
		} `json:"expect"`
	} `json:"cases"`
}

// withoutNulls returns v with every null object member removed, at any depth.
func withoutNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, member := range t {
			if member != nil {
				out[k] = withoutNulls(member)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = withoutNulls(item)
		}
		return out
	default:
		return v
	}
}

var testUpgrader = websocket.Upgrader{
	Subprotocols: []string{"graphql-transport-ws"},
	CheckOrigin:  func(*http.Request) bool { return true },
}

// scriptedWSServer plays connection scripts in order and records the
// Authorization value of every connection_init and the payload of every
// subscribe message.
type scriptedWSServer struct {
	mu            sync.Mutex
	scripts       []connectionScript
	connections   int
	authorization []*string
	subscribed    []map[string]any
}

func (s *scriptedWSServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := testUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	s.mu.Lock()
	var script *connectionScript
	if s.connections < len(s.scripts) {
		script = &s.scripts[s.connections]
	}
	s.connections++
	s.mu.Unlock()
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type    string         `json:"type"`
			ID      string         `json:"id"`
			Payload map[string]any `json:"payload"`
		}
		_ = json.Unmarshal(raw, &msg)
		switch msg.Type {
		case "connection_init":
			var auth *string
			if a, ok := msg.Payload["Authorization"].(string); ok {
				auth = &a
			}
			s.mu.Lock()
			s.authorization = append(s.authorization, auth)
			s.mu.Unlock()
			if script != nil && script.OnInit == "reset" {
				// Linger 0 makes the close send a TCP RST instead of a FIN.
				if tcp, ok := conn.NetConn().(*net.TCPConn); ok {
					_ = tcp.SetLinger(0)
				}
				_ = conn.NetConn().Close()
				return
			}
			if script == nil || script.OnInit == "close" {
				code, reason := 1000, "terminated"
				if script != nil && script.CloseCode != 0 {
					code, reason = script.CloseCode, script.CloseReason
				}
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
		case "subscribe":
			s.mu.Lock()
			s.subscribed = append(s.subscribed, msg.Payload)
			s.mu.Unlock()
			for _, data := range script.Next {
				_ = conn.WriteJSON(map[string]any{"id": msg.ID, "type": "next", "payload": map[string]any{"data": data}})
			}
			switch {
			case len(script.Error) > 0:
				_ = conn.WriteJSON(map[string]any{"id": msg.ID, "type": "error", "payload": script.Error})
			case script.Then == "complete":
				_ = conn.WriteJSON(map[string]any{"id": msg.ID, "type": "complete"})
			case script.Then == "drop":
				time.Sleep(20 * time.Millisecond)
				code := script.DropCode
				if code == 0 {
					code = 1001
				}
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, "going away"))
				return
			}
		}
	}
}

func wsURL(httpURL string) string { return "ws" + strings.TrimPrefix(httpURL, "http") }

func TestSubscriptions(t *testing.T) {
	var fx subscriptionsFixture
	loadFixture(t, "subscriptions.json", &fx)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			server := &scriptedWSServer{scripts: tc.Connections}
			srv := httptest.NewServer(server)
			defer srv.Close()
			var tokenMu sync.Mutex
			tokens := append([]*string(nil), tc.Tokens...)
			sc, err := NewSubscriptionClient(SubscriptionOptions{
				URL: wsURL(srv.URL),
				TokenFunc: func(context.Context) (string, error) {
					tokenMu.Lock()
					defer tokenMu.Unlock()
					if len(tokens) == 0 || tokens[0] == nil {
						if len(tokens) > 0 {
							tokens = tokens[1:]
						}
						return "", nil
					}
					tok := *tokens[0]
					tokens = tokens[1:]
					return tok, nil
				},
				MaxReconnects:  fx.MaxReconnects,
				ReconnectDelay: func(int) time.Duration { return 0 },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			operation := tc.Operation
			if operation == "" {
				operation = "TenantEvents"
			}
			subscribe, ok := subscriptionCalls[operation]
			if !ok {
				t.Fatalf("no generated subscription %s", operation)
			}
			events := []any{}
			eventIDs := []string{}
			var subErr error
			for ev, err := range subscribe(ctx, sc, tc.Variables) {
				if err != nil {
					subErr = err
					break
				}
				events = append(events, ev)
				if te, ok := ev.(*TenantEventsResponse); ok {
					eventIDs = append(eventIDs, te.TenantEvents.Id)
				}
			}
			if tc.Expect.EventIDs != nil && !jsonEqual(eventIDs, tc.Expect.EventIDs) {
				t.Errorf("event ids = %v, want %v", eventIDs, tc.Expect.EventIDs)
			}
			if tc.Expect.Events != nil && !jsonEqual(events, tc.Expect.Events) {
				t.Errorf("events = %s, want %s", mustJSON(events), mustJSON(tc.Expect.Events))
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if tc.Expect.Subscribed != nil {
				sent := make([]any, len(server.subscribed))
				for i, payload := range server.subscribed {
					sent[i] = withoutNulls(map[string]any{"operationName": payload["operationName"], "variables": payload["variables"]})
				}
				if !jsonEqual(sent, tc.Expect.Subscribed) {
					t.Errorf("subscribed = %s, want %s", mustJSON(sent), mustJSON(tc.Expect.Subscribed))
				}
			}
			if server.connections != tc.Expect.Connections {
				t.Errorf("connections = %d, want %d", server.connections, tc.Expect.Connections)
			}
			if !jsonEqual(server.authorization, tc.Expect.Authorization) {
				t.Errorf("authorization = %v, want %v", server.authorization, tc.Expect.Authorization)
			}
			if tc.Expect.Error != nil {
				checkError(t, subErr, tc.Expect.Error)
			} else if subErr != nil {
				t.Errorf("unexpected error: %v", subErr)
			}
		})
	}
}
