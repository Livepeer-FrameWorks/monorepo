package triggers

import (
	"errors"
	"net"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

// PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS opens private receivers to the
// playback webhook dialer; loopback and cloud metadata stay blocked either way.
func TestPlaybackWebhookAllowPrivateDestinations(t *testing.T) {
	t.Cleanup(func() { SetPlaybackWebhookAllowPrivateDestinations(false) })
	for _, allow := range []bool{false, true} {
		SetPlaybackWebhookAllowPrivateDestinations(allow)
		dial := playbackWebhookDestinationPolicy().DialControl()
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
