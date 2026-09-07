package mist

import "testing"

func TestParsePushEndStatus(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		succeeded bool
		duration  int64
		bytes     uint64
		observed  bool
	}{
		{name: "empty legacy success", succeeded: true},
		{name: "numeric success", raw: "0", succeeded: true},
		{name: "numeric failure", raw: "7"},
		{name: "malformed json", raw: "{nope"},
		{name: "unrecognized json object", raw: `{"status":"failed"}`},
		{name: "json stats", raw: `{"duration_ms":1234,"bytes_sent":5678}`, succeeded: true, duration: 1234, bytes: 5678, observed: true},
		{name: "mist push status", raw: `{"mediatime":8123,"media_tx":8000,"active_seconds":8,"active_ms":8123,"current_target":"rtmp://example.invalid/redacted","bytes":5678}`, succeeded: true, duration: 8123, bytes: 5678, observed: true},
		{name: "mist seconds fallback", raw: `{"active_seconds":"2.5","bytes":42}`, succeeded: true, duration: 2500, bytes: 42, observed: true},
		{name: "json zero bytes", raw: `{"duration_ms":1234,"bytes_sent":0}`, succeeded: true, duration: 1234, observed: true},
		{name: "json seconds and string bytes", raw: `{"duration":2.5,"bytes":"42"}`, succeeded: true, duration: 2500, bytes: 42, observed: true},
		{name: "malformed preferred value falls back", raw: `{"active_ms":"bad","duration_ms":1234,"bytes":"bad","sent_bytes":42}`, succeeded: true, duration: 1234, bytes: 42, observed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ParsePushEndStatus(test.raw)
			if got.Succeeded != test.succeeded || got.DurationMS != test.duration || got.BytesSent != test.bytes || got.BytesObserved != test.observed {
				t.Fatalf("ParsePushEndStatus(%q) = %+v", test.raw, got)
			}
		})
	}
}
