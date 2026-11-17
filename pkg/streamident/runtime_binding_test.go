package streamident

import (
	"strings"
	"testing"
)

func TestBoundRuntimeNamesSeparatePublisherAndRelay(t *testing.T) {
	const id = "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9"
	pub, err := BoundRuntimeName("stream", RuntimePublisher, id)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := BoundRuntimeName("stream", RuntimeRelay, id)
	if err != nil {
		t.Fatal(err)
	}
	if pub == relay || pub == "live+stream" || len(pub) > 100 {
		t.Fatalf("runtimes collide: %s %s", pub, relay)
	}
	for _, name := range []string{pub, relay} {
		binding, ok := ParseRuntimeBinding(name)
		if !ok || binding.InternalName != "stream" || binding.ExecutionID != id || Parse(name).Concrete != "stream" {
			t.Fatalf("binding did not round-trip: %+v %v", binding, ok)
		}
	}
	for _, bad := range []string{"live+stream", pub + "x", strings.ToUpper(pub), "vod+" + strings.TrimPrefix(pub, "live+")} {
		if _, ok := ParseRuntimeBinding(bad); ok {
			t.Fatalf("malformed binding accepted: %s", bad)
		}
	}
	for _, bad := range []string{"", "../stream", "has space", strings.Repeat("a", 58)} {
		if _, err := BoundRuntimeName(bad, RuntimePublisher, id); err == nil {
			t.Fatalf("invalid or truncated runtime accepted: %s", bad)
		}
	}
	if _, err := BoundRuntimeName("stream", RuntimePublisher, strings.ReplaceAll(id, "-", "")); err == nil {
		t.Fatal("noncanonical execution accepted")
	}
}
