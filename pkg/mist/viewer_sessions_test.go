package mist

import "testing"

func TestParseViewerSessionsKeepsViewersOnly(t *testing.T) {
	response := map[string]interface{}{"clients": map[string]interface{}{
		"fields": []interface{}{"sessid", "host", "stream", "protocol"},
		"data": []interface{}{
			[]interface{}{"abc", "::ffff:192.0.2.10", "live+s1", "HLS"},
			[]interface{}{"Iinput", "127.0.0.1", "live+s1", "INPUT:Buffer"},
			[]interface{}{"Ooutput", "127.0.0.1", "live+s1", "OUTPUT:DTSC"},
		},
	}}
	sessions, err := parseViewerSessions(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0] != (ViewerSession{SessionID: "abc", Host: "::ffff:192.0.2.10", Stream: "live+s1", Protocol: "HLS"}) {
		t.Fatalf("sessions = %+v", sessions)
	}
	if _, err := parseViewerSessions(map[string]interface{}{"clients": map[string]interface{}{"fields": []interface{}{"host"}}}); err == nil {
		t.Fatal("a response without the session id field was accepted")
	}
}
