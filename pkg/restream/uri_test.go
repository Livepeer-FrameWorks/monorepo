package restream

import "testing"

func TestMaskTargetURI(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "multi segment rtmp", raw: "rtmp://live.example/app/secret/suffix", want: "rtmp://live.example/redacted"},
		{name: "short path", raw: "rtmp://live.example/k", want: "rtmp://live.example/redacted"},
		{name: "userinfo", raw: "rtmps://user:password@live.example/app/key", want: "rtmps://live.example/redacted"},
		{name: "srt query and fragment", raw: "srt://media.example:9710?streamid=secret#passphrase=secret", want: "srt://media.example:9710"},
		{name: "ipv6", raw: "srt://[2001:db8::1]:9000/live/key", want: "srt://[2001:db8::1]:9000/redacted"},
		{name: "escaped secret", raw: "rtmp://live.example/app/secret%2Fstill-secret", want: "rtmp://live.example/redacted"},
		{name: "no path", raw: "rtmp://live.example", want: "rtmp://live.example"},
		{name: "invalid", raw: "://invalid", want: "****"},
		{name: "missing host", raw: "rtmp:secret", want: "****"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MaskTargetURI(tt.raw); got != tt.want {
				t.Fatalf("MaskTargetURI(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
