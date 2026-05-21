package dns

import "testing"

func TestEdgeNodeLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc", "edge-abc"},
		{"edge-abc", "edge-abc"},
		{"EDGE-foo", "edge-foo"},
		{"Foo_Bar", "edge-foo-bar"},
		{"", "edge-default"},
		{"edge-", "edge-edge"},
	}
	for _, c := range cases {
		got := EdgeNodeLabel(c.in)
		if got != c.want {
			t.Errorf("EdgeNodeLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEdgeNodeFQDN(t *testing.T) {
	got := EdgeNodeFQDN("us-1", "media-us-1", "frameworks.network")
	want := "edge-us-1.media-us-1.frameworks.network"
	if got != want {
		t.Errorf("EdgeNodeFQDN = %q, want %q", got, want)
	}
	got = EdgeNodeFQDN("edge-eu-1", "eu-west-1", "example.com")
	want = "edge-eu-1.eu-west-1.example.com"
	if got != want {
		t.Errorf("EdgeNodeFQDN double-prefix guard failed: got %q, want %q", got, want)
	}
}
