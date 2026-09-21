package version

import "testing"

func TestChannelForTag(t *testing.T) {
	cases := []struct {
		tag     string
		want    Channel
		wantErr bool
	}{
		{tag: "v0.3.4", want: ChannelStable},
		{tag: "v1.0.0+build.7", want: ChannelStable},
		{tag: "v0.3.5-rc1", want: ChannelRC},
		{tag: "v0.3.5-rc.2", want: ChannelRC},
		{tag: "v0.3.5-beta.2", want: ChannelRC},
		{tag: "v0.3.5-alpha", want: ChannelRC},
		{tag: "v0.3.5-rc1+sha.abc", want: ChannelRC},
		{tag: "0.3.4", wantErr: true},
		{tag: "v0.3", wantErr: true},
		{tag: "v0.3.5-", wantErr: true},
		{tag: "v0.3.5-rc..1", wantErr: true},
		{tag: "v0.3.5-01", wantErr: true},
		{tag: "v01.3.5", wantErr: true},
		{tag: " v0.3.4", wantErr: true},
		{tag: "latest", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ChannelForTag(tc.tag)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ChannelForTag(%q) = %q, want error", tc.tag, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ChannelForTag(%q) = %q, %v; want %q", tc.tag, got, err, tc.want)
		}
	}
}

func TestChannelImageTracksAndParsing(t *testing.T) {
	if got, err := ParseChannel("candidate"); err != nil || got != ChannelCandidate {
		t.Fatalf("candidate channel = %q, %v", got, err)
	}
	if ChannelStable.ImageTrack() != "latest" || ChannelRC.ImageTrack() != "rc" {
		t.Fatalf("image tracks = %q/%q", ChannelStable.ImageTrack(), ChannelRC.ImageTrack())
	}
	for _, c := range Channels() {
		parsed, err := ParseChannel(string(c))
		if err != nil || parsed != c {
			t.Fatalf("ParseChannel(%q) = %q, %v", c, parsed, err)
		}
	}
	if _, err := ParseChannel("beta"); err == nil {
		t.Fatal("beta is not a release channel")
	}
}
