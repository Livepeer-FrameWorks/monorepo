package grpc

import (
	"context"
	"net"
	"net/url"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

func TestParseVODImportSource(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		ok        bool
	}{
		{"https://cdn.example/v/talk.mp4?sig=abc#t=10", "https://cdn.example/v/talk.mp4?sig=abc", true},
		{"http://cdn.example/talk.mp4", "http://cdn.example/talk.mp4", true},
		{"https://user:pass@cdn.example/v.mp4", "", false},
		{"ipfs://bafybeigdyrzt/clip.mp4", "", false},
		{"ar://Tx123abc", "", false},
		{"ftp://cdn.example/v.mp4", "", false},
		{"https:///v.mp4", "", false},
		{"", "", false},
	} {
		got, err := parseVODImportSource(tc.raw)
		if (err == nil) != tc.ok {
			t.Errorf("parseVODImportSource(%q) err = %v, want ok=%v", tc.raw, err, tc.ok)
			continue
		}
		if tc.ok && got.String() != tc.want {
			t.Errorf("parseVODImportSource(%q) = %q, want %q", tc.raw, got.String(), tc.want)
		}
	}
}

func TestVODImportFilename(t *testing.T) {
	for _, tc := range []struct {
		requested, raw, want string
		ok                   bool
	}{
		{"", "https://cdn.example/a/talk.mov", "talk.mov", true},
		{"clip.MKV", "https://gw.example/ipfs/bafy", "clip.MKV", true},
		{"", "https://gw.example/ipfs/bafy", "", false},
		// m4v is not relay-readable: Mist opens it only from a local file.
		{"", "https://cdn.example/a/talk.m4v", "", false},
		{"slides.pdf", "https://cdn.example/talk.mp4", "", false},
	} {
		source, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := vodImportFilename(tc.requested, source)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("vodImportFilename(%q, %q) = %q, %v; want %q ok=%v", tc.requested, tc.raw, got, err, tc.want, tc.ok)
		}
	}
}

func TestValidatePublicDestinationHostForImport(t *testing.T) {
	policy := restream.PublicDestinationPolicy()
	policy.LookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "public.example":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "mixed.example":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}, nil
		default:
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"https://public.example/v.mp4", true},
		{"https://mixed.example/v.mp4", false},
		{"http://127.0.0.1:8080/v.mp4", false},
		{"http://169.254.169.254/latest/meta-data", false},
		{"https://foghorn.frameworks.network/v.mp4", false},
		{"https://db.internal/v.mp4", false},
		{"https://unknown.example/v.mp4", false},
	} {
		source, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validatePublicDestinationHost(context.Background(), policy, source); (err == nil) != tc.ok {
			t.Errorf("validatePublicDestinationHost(%q) = %v, want ok=%v", tc.raw, err, tc.ok)
		}
	}
}
