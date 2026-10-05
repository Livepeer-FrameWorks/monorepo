package grpc

import (
	"context"
	"net"
	"testing"

	"github.com/sirupsen/logrus"
)

func playbackWebhookTestServer(allow bool) *CommodoreServer {
	return NewCommodoreServer(CommodoreServerConfig{
		Logger:                      logrus.New(),
		JWTSecret:                   []byte("test-only-jwt-legacy-secret"),
		FieldEncryptionKeyID:        "test",
		FieldEncryptionKey:          []byte("test-only-field-encryption-secret"),
		PlaybackWebhookAllowPrivate: allow,
	})
}

// The playback webhook policy follows PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS
// while URL-import sources stay public-only regardless of it.
func TestPlaybackWebhookPrivateDestinationSetting(t *testing.T) {
	const receiver = "http://auth-receiver.lan:8080/playback"
	for _, allow := range []bool{false, true} {
		s := playbackWebhookTestServer(allow)
		policy := s.webhookDestinationPolicy
		if policy.PlatformAddress == nil {
			t.Fatalf("allowPrivate=%v: playback webhook policy does not refuse platform addresses", allow)
		}
		// The test host's own routes (a developer VPN) must not decide.
		policy.PlatformAddress = func(net.IP) bool { return false }
		policy.LookupIP = resolvingPolicy("10.20.0.5").LookupIP
		err := validateWebhookURL(context.Background(), policy, receiver)
		if allow != (err == nil) {
			t.Fatalf("allowPrivate=%v: private receiver err = %v", allow, err)
		}
		if s.importDestinationPolicy.AllowPrivate || s.importDestinationPolicy.LookupIP == nil {
			t.Fatalf("allowPrivate=%v: URL-import policy = %+v, want public-only", allow, s.importDestinationPolicy)
		}
	}
}

// Allowing private destinations opens customer networks, never the platform:
// a mesh service name is refused when the policy is saved.
func TestPlaybackWebhookPrivateDestinationsRefusePlatformNames(t *testing.T) {
	policy := playbackWebhookTestServer(true).webhookDestinationPolicy
	policy.LookupIP = resolvingPolicy("10.89.0.2").LookupIP
	for _, raw := range []string{"http://quartermaster.internal:18002/rc18-ssrf-probe", "https://commodore.internal/x"} {
		if err := validateWebhookURL(context.Background(), policy, raw); err == nil {
			t.Errorf("validateWebhookURL(%q) with private destinations allowed accepted a platform name", raw)
		}
	}
}
