package control

import (
	"errors"
	"testing"
)

func TestIsolatedDrainRequiresPhysicalRuntimeAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		err             error
		absent, unknown bool
	}{
		{name: "still online", body: `{"active_streams":{"live+old":{"status":"Online"}}}`},
		{name: "still shutting down", body: `{"active_streams":{"live+old":{"status":"Shutting down"}}}`},
		{name: "another generation remains", body: `{"active_streams":{"live+new":{"status":"Online"}}}`, absent: true},
		{name: "empty authoritative inventory", body: `{"active_streams":null}`, absent: true},
		{name: "malformed inventory", body: `{}`, unknown: true},
		{name: "unavailable", err: errors.New("unreachable controller"), unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeActiveStreams{body: tc.body, err: tc.err}
			absent, err := isolatedRuntimeAbsent(t.Context(), reader, "live+old")
			if absent != tc.absent || (err != nil) != tc.unknown || len(reader.asked) != 1 || reader.asked[0] != "live+old" {
				t.Fatalf("stop proof: absent=%v err=%v queried=%v", absent, err, reader.asked)
			}
		})
	}
}
