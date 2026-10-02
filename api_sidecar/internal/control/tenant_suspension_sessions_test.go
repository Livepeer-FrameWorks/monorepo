package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// sessionMist models the part of Mist's session table stop_sessions acts on:
// each session belongs to one exact stream name, the clients listing leaves
// out input and output sessions, and stop_sessions ends every session of each
// exactly named stream.
type sessionMist struct {
	mu       sync.Mutex
	sessions map[string]string // session id -> Mist stream name
}

func (m *sessionMist) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		if err := json.Unmarshal([]byte(r.URL.Query().Get("command")), &command); err != nil {
			t.Errorf("Mist command: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case command["authorize"] != nil:
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		case command["clients"] != nil:
			var rows [][]string
			for id, stream := range m.sessions {
				rows = append(rows, []string{id, "192.0.2.1", stream, "HLS"})
			}
			buf, _ := json.Marshal(map[string]any{"clients": map[string]any{"fields": []string{"sessid", "host", "stream", "protocol"}, "data": rows}})
			_, _ = w.Write(buf)
			return
		case command["stop_sessions"] != nil:
			streams, ok := command["stop_sessions"].(map[string]any)
			if !ok {
				t.Errorf("stop_sessions payload %#v is not stream name -> connector", command["stop_sessions"])
			}
			for id, stream := range m.sessions {
				if _, stop := streams[stream]; stop {
					delete(m.sessions, id)
				}
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (m *sessionMist) remaining() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Foghorn's suspension names the tenant's streams by internal name. The edge
// stops them under every runtime name they run as, which ends their viewers
// and the publisher's input session, and leaves other tenants' streams alone.
func TestStopSessionsForSuspendedTenantStopsItsStreamsOnly(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{
		"viewer-push":   "live+suspended",
		"Iinput-push":   "live+suspended",
		"viewer-pull":   "pull+pulled",
		"Iinput-pull":   "pull+pulled",
		"viewer-other":  "live+other-tenant",
		"Iinput-other":  "live+other-tenant",
		"viewer-native": "native",
	}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})

	handleStopSessions(logging.NewLogger(), &ipcpb.StopSessionsRequest{
		TenantId: "tenant-suspended", Reason: "insufficient_balance",
		StreamNames: []string{"suspended", "pulled", "native"},
	})

	if got := m.remaining(); !slices.Equal(got, []string{"Iinput-other", "viewer-other"}) {
		t.Fatalf("sessions left after suspension = %v, want only the other tenant's", got)
	}
}
