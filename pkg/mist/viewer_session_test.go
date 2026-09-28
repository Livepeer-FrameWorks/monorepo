package mist

import "testing"

func TestViewerSessionIDAcceptsOnlyIssuedSessions(t *testing.T) {
	issued, err := NewViewerSessionID()
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		url  string
		want string
	}{
		"issued session":            {"https://edge/hls/s/index.m3u8?tkn=" + issued, issued},
		"mist-generated token":      {"https://edge/hls/s/0.ts?tkn=3735928559", ""},
		"processing read token":     {"https://edge/s.mkv?tkn=" + ProcessingReadSessionPrefix + issued, ""},
		"uuid of another version":   {"https://edge/s.mp4?tkn=6ba7b810-9dad-11d1-80b4-00c04fd430c8", ""},
		"no token":                  {"https://edge/hls/s/index.m3u8", ""},
		"issued session in fwcid":   {"https://edge/hls/s/index.m3u8?fwcid=" + issued, ""},
		"unparseable request URL":   {"https://edge/%zz?tkn=" + issued, ""},
		"issued session among more": {"https://edge/s.mp4?rate=0&tkn=" + issued + "&fwjwt=x", issued},
	} {
		if got := ViewerSessionID(test.url); got != test.want {
			t.Errorf("%s: ViewerSessionID(%q) = %q, want %q", name, test.url, got, test.want)
		}
	}
}

func TestViewerJWTPrefersFwjwtAndNeverReturnsASession(t *testing.T) {
	issued, err := NewViewerSessionID()
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		url, reported, want string
	}{
		"fwjwt on the URL":                   {"https://edge/s.mp4?tkn=" + issued + "&fwjwt=eyJ.a.b", issued, "eyJ.a.b"},
		"direct jwt reported by Mist":        {"https://edge/s.mp4?jwt=eyJ.c.d", "eyJ.c.d", "eyJ.c.d"},
		"the request's session is not a JWT": {"https://edge/s.mp4?tkn=" + issued, issued, ""},
		"a presented token shaped like one":  {"viewer://content", issued, issued},
		"fwjwt wins over the reported token": {"https://edge/s.mp4?fwjwt=eyJ.e.f", "eyJ.c.d", "eyJ.e.f"},
		"nothing presented":                  {"https://edge/s.mp4", "", ""},
	} {
		if got := ViewerJWT(test.url, test.reported); got != test.want {
			t.Errorf("%s: ViewerJWT = %q, want %q", name, got, test.want)
		}
	}
}
