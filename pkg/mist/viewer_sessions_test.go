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

func TestSessionStreamNamesCoversEveryRuntimeNameOfTheStreams(t *testing.T) {
	live := []ViewerSession{
		{SessionID: "a", Stream: "live+s"},
		{SessionID: "b", Stream: "pull+other"},
		{SessionID: "c", Stream: "dvr+s"},
		{SessionID: "d", Stream: "live+elsewhere"},
	}
	got := SessionStreamNames([]string{"s", "live+other", " "}, live)
	want := []string{"s", "live+s", "pull+s", "live+other", "pull+other", "dvr+s"}
	if len(got) != len(want) {
		t.Fatalf("SessionStreamNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SessionStreamNames = %v, want %v", got, want)
		}
	}
}
