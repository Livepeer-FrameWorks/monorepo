package mist

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestSessionsStoppedRequiresCompleteCurrentCounts(t *testing.T) {
	for _, tc := range []struct {
		name, response     string
		stopped, wantError bool
	}{
		{"buffer without sessions", `{"active_streams":{"live+owned":{"viewers":0,"inputs":0,"outputs":0}}}`, true, false},
		{"absent stream", `{"active_streams":{}}`, true, false},
		{"empty Mist result", `{"active_streams":null}`, true, false},
		{"live input", `{"active_streams":{"live+owned":{"viewers":0,"inputs":1,"outputs":0}}}`, false, false},
		{"live viewer", `{"active_streams":{"live+owned":{"viewers":1,"inputs":0,"outputs":0}}}`, false, false},
		{"live output", `{"active_streams":{"live+owned":{"viewers":0,"inputs":0,"outputs":1}}}`, false, false},
		{"foreign activity", `{"active_streams":{"live+foreign":{"viewers":2,"inputs":1,"outputs":0}}}`, true, false},
		{"missing counts", `{"active_streams":{"live+owned":{}}}`, false, true},
		{"missing response", `{}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := fakeMistAPI(t, tc.response)
			client := NewClient(logging.NewLogger(), ClientConfig{BaseURL: server.URL})
			stopped, err := client.SessionsStopped(t.Context(), []string{"live+owned"})
			if stopped != tc.stopped || (err != nil) != tc.wantError {
				t.Fatalf("stopped=%v error=%v, want stopped=%v error=%v", stopped, err, tc.stopped, tc.wantError)
			}
		})
	}
}
