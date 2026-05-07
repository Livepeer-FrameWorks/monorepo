package pullsource

import "testing"

func TestClassify_Public(t *testing.T) {
	publicURIs := []string{
		"https://ntv1.akamaized.net/hls/live/2014075/NASA-NTV1-HLS/master.m3u8",
		"rtsp://example.com/live",
		"srt://example.com:9000",
		"rist://example.com:8000",
		"dtsc://origin.example.com:4200",
		"https://example.com/live/stream.ts",
		"tsudp://example.com:9000",
		"https://example.com/live/stream.webm",
		"https://1.2.3.4/live.m3u8",
	}
	for _, uri := range publicURIs {
		t.Run(uri, func(t *testing.T) {
			class, err := Classify(uri)
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if class != ClassPublic {
				t.Fatalf("class = %s, want public", class)
			}
		})
	}
}

func TestClassify_Private(t *testing.T) {
	// Operator/self-host territory: cluster opt-in required to run the pull.
	privateURIs := []string{
		"tsudp://10.0.0.5:9000",        // RFC1918 unicast
		"tsudp://239.1.2.3:9000",       // global multicast (tsudp-only)
		"https://10.0.0.1/live.m3u8",   // private HTTPS HLS
		"rtsp://192.168.1.1/live",      // private RTSP
		"https://172.20.0.1/live.m3u8", // RFC1918
		"srt://fc00::1:9000",           // ULA
	}
	for _, uri := range privateURIs {
		t.Run(uri, func(t *testing.T) {
			class, err := Classify(uri)
			if err != nil {
				t.Fatalf("Classify private URI: %v", err)
			}
			if class != ClassPrivate {
				t.Fatalf("class = %s, want private", class)
			}
		})
	}
}

func TestClassify_Blocked(t *testing.T) {
	cases := []struct {
		uri     string
		because string
	}{
		{"https://example.com/live", "unsupported suffix"},
		{"ftp://example.com/live.m3u8", "unsupported scheme"},
		{"https://localhost/live.m3u8", "mDNS"},
		{"https://127.0.0.1/live.m3u8", "loopback"},
		{"https://169.254.169.254/latest/meta-data/live.m3u8", "cloud metadata"},
		{"https://something.frameworks.network/live.m3u8", "operator-internal"},
		{"https://something.internal/live.m3u8", "operator-internal"},
		{"tsudp://127.0.0.1:9000", "loopback on tsudp"},
		{"tsudp://224.0.0.1:9000", "link-local multicast"},
		{"https://224.0.0.1/live.m3u8", "multicast on non-tsudp"},
		{"rist://localhost:8000", "mDNS"},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			class, err := Classify(tc.uri)
			if class != ClassBlocked {
				t.Fatalf("expected blocked (%s), got %s", tc.because, class)
			}
			if err == nil {
				t.Fatal("blocked class must return non-nil error")
			}
		})
	}
}

func TestEligiblePullClusters(t *testing.T) {
	clusters := []ClusterCapability{
		{ID: "demo-media", AllowPrivatePullSources: false},
		{ID: "peer-media", AllowPrivatePullSources: false},
		{ID: "selfhost-edge", AllowPrivatePullSources: true},
	}

	t.Run("public allows every cluster", func(t *testing.T) {
		got := EligiblePullClusters(ClassPublic, clusters)
		if len(got) != 3 {
			t.Fatalf("got %d eligible clusters, want 3", len(got))
		}
	})

	t.Run("private restricts to opted-in clusters", func(t *testing.T) {
		got := EligiblePullClusters(ClassPrivate, clusters)
		if len(got) != 1 || got[0].ID != "selfhost-edge" {
			t.Fatalf("private eligibility = %+v, want [selfhost-edge]", got)
		}
	})

	t.Run("blocked yields empty set", func(t *testing.T) {
		got := EligiblePullClusters(ClassBlocked, clusters)
		if len(got) != 0 {
			t.Fatalf("blocked eligibility = %+v, want empty", got)
		}
	})

	t.Run("private + zero opted-in clusters = empty (caller rejects)", func(t *testing.T) {
		platformOnly := []ClusterCapability{
			{ID: "demo-media", AllowPrivatePullSources: false},
			{ID: "peer-media", AllowPrivatePullSources: false},
		}
		got := EligiblePullClusters(ClassPrivate, platformOnly)
		if len(got) != 0 {
			t.Fatalf("private + no opted-in = %+v, want empty", got)
		}
	})
}

func TestRedact(t *testing.T) {
	got := Redact("rtsp://user:pass@example.com/live")
	if got != "rtsp://example.com" {
		t.Fatalf("Redact = %q", got)
	}
}

func TestValidate_BackCompatBoolHelper(t *testing.T) {
	if !IsValid("https://example.com/live.m3u8") {
		t.Fatal("public URI should pass IsValid")
	}
	if IsValid("https://localhost/live.m3u8") {
		t.Fatal("blocked URI should not pass IsValid")
	}
	if !IsValid("tsudp://10.0.0.5:9000") {
		t.Fatal("private URI passes scheme/syntax — IsValid should return true; cluster eligibility is a separate decision")
	}
}
