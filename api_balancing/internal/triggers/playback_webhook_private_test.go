package triggers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

// PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS opens private receivers to the
// playback webhook dialer; loopback and cloud metadata stay blocked either way.
func TestPlaybackWebhookAllowPrivateDestinations(t *testing.T) {
	t.Cleanup(func() { SetPlaybackWebhookAllowPrivateDestinations(false) })
	for _, allow := range []bool{false, true} {
		SetPlaybackWebhookAllowPrivateDestinations(allow)
		policy := playbackWebhookDestinationPolicy()
		if policy.PlatformAddress == nil {
			t.Fatalf("allow=%v: playback webhook policy does not refuse platform addresses", allow)
		}
		// The test host's own routes (a developer VPN) must not decide.
		policy.PlatformAddress = func(net.IP) bool { return false }
		dial := policy.DialControl()
		privateErr := dial("tcp", net.JoinHostPort("10.20.0.5", "443"), nil)
		if blocked := errors.Is(privateErr, restream.ErrDialBlocked); blocked == allow {
			t.Fatalf("allow=%v: private receiver dial err = %v", allow, privateErr)
		}
		for _, addr := range []string{"127.0.0.1", "169.254.169.254"} {
			if err := dial("tcp", net.JoinHostPort(addr, "443"), nil); !errors.Is(err, restream.ErrDialBlocked) {
				t.Fatalf("allow=%v: %s dial err = %v, want blocked", allow, addr, err)
			}
		}
	}
}

// With private destinations allowed, a playback webhook aimed at this host's
// own address (where platform services listen) is refused at connect time and
// the receiver never sees the request.
func TestPlaybackWebhookPrivateDestinationsRefusePlatformHost(t *testing.T) {
	t.Cleanup(func() { SetPlaybackWebhookAllowPrivateDestinations(false) })
	SetPlaybackWebhookAllowPrivateDestinations(true)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var local net.IP
	for _, addr := range addrs {
		if prefix, ok := addr.(*net.IPNet); ok && prefix.IP.To4() != nil && !prefix.IP.IsLoopback() && !prefix.IP.IsLinkLocalUnicast() {
			local = prefix.IP.To4()
			break
		}
	}
	if local == nil {
		t.Skip("host has no non-loopback IPv4 address")
	}
	var hits atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(local.String(), "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", local, err)
	}
	srv.Listener = listener
	srv.Start()
	defer srv.Close()

	client := newPlaybackWebhookClient(2*time.Second, playbackWebhookDestinationPolicy())
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/playback", nil)
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, restream.ErrDialBlocked) || hits.Load() != 0 {
		t.Fatalf("playback webhook to this host's own address %s: err = %v, hits = %d; want ErrDialBlocked and no request", local, err, hits.Load())
	}
}
