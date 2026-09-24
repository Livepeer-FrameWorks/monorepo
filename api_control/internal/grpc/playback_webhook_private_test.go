package grpc

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
)

// The playback webhook policy follows PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS
// while URL-import sources stay public-only regardless of it.
func TestPlaybackWebhookPrivateDestinationSetting(t *testing.T) {
	const receiver = "http://auth-receiver.internal:8080/playback"
	for _, allow := range []bool{false, true} {
		s := NewCommodoreServer(CommodoreServerConfig{
			Logger:                      logrus.New(),
			JWTSecret:                   []byte("test-only-jwt-legacy-secret"),
			FieldEncryptionKeyID:        "test",
			FieldEncryptionKey:          []byte("test-only-field-encryption-secret"),
			PlaybackWebhookAllowPrivate: allow,
		})
		policy := s.webhookDestinationPolicy
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
