package restream

import "testing"

func TestNormalizePlatform(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: " Twitch ", want: "twitch", ok: true},
		{input: "", want: "custom", ok: true},
		{input: "unknown", want: "custom", ok: false},
	}
	for _, test := range tests {
		got, ok := NormalizePlatform(test.input)
		if got != test.want || ok != test.ok {
			t.Fatalf("NormalizePlatform(%q) = (%q, %v), want (%q, %v)", test.input, got, ok, test.want, test.ok)
		}
	}
}
