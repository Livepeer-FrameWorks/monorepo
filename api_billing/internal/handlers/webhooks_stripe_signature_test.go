package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// signFor computes the Stripe-style v1 signature over the documented signed
// payload: <timestamp> + "." + <body>.
func signFor(secret string, ts int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyStripeSignature is hand-rolled HMAC-SHA256 verification (not the
// stripe-go helper): it parses t=/v1= elements, enforces a ±300s timestamp
// tolerance, and constant-time compares against any provided v1 signature.
func TestVerifyStripeSignature(t *testing.T) {
	const secret = "whsec_test"
	payload := []byte(`{"id":"evt_1","type":"invoice.paid"}`)
	s := &Service{logger: logrus.New()}

	t.Run("valid signature within tolerance", func(t *testing.T) {
		ts := time.Now().Unix()
		header := fmt.Sprintf("t=%d,v1=%s", ts, signFor(secret, ts, payload))
		if !s.verifyStripeSignature(payload, header, secret) {
			t.Fatal("expected valid signature to verify")
		}
	})

	t.Run("one of several v1 values matches", func(t *testing.T) {
		ts := time.Now().Unix()
		header := fmt.Sprintf("t=%d,v1=deadbeef,v1=%s", ts, signFor(secret, ts, payload))
		if !s.verifyStripeSignature(payload, header, secret) {
			t.Fatal("expected verification to succeed when any v1 matches")
		}
	})

	t.Run("timestamp at the edge of tolerance", func(t *testing.T) {
		ts := time.Now().Unix() - 299
		header := fmt.Sprintf("t=%d,v1=%s", ts, signFor(secret, ts, payload))
		if !s.verifyStripeSignature(payload, header, secret) {
			t.Fatal("expected signature 299s old to verify")
		}
	})

	rejects := []struct {
		name   string
		header func() string
		secret string
	}{
		{
			name: "empty secret",
			header: func() string {
				ts := time.Now().Unix()
				return fmt.Sprintf("t=%d,v1=%s", ts, signFor(secret, ts, payload))
			},
			secret: "",
		},
		{
			name:   "empty signature header",
			header: func() string { return "" },
			secret: secret,
		},
		{
			name:   "missing timestamp element",
			header: func() string { ts := time.Now().Unix(); return "v1=" + signFor(secret, ts, payload) },
			secret: secret,
		},
		{
			name:   "missing v1 element",
			header: func() string { return fmt.Sprintf("t=%d", time.Now().Unix()) },
			secret: secret,
		},
		{
			name:   "non-numeric timestamp",
			header: func() string { return "t=notanumber,v1=" + signFor(secret, 0, payload) },
			secret: secret,
		},
		{
			name: "timestamp too far in the future",
			header: func() string {
				ts := time.Now().Unix() + 400
				return fmt.Sprintf("t=%d,v1=%s", ts, signFor(secret, ts, payload))
			},
			secret: secret,
		},
		{
			name: "timestamp too far in the past",
			header: func() string {
				ts := time.Now().Unix() - 400
				return fmt.Sprintf("t=%d,v1=%s", ts, signFor(secret, ts, payload))
			},
			secret: secret,
		},
		{
			name: "wrong secret",
			header: func() string {
				ts := time.Now().Unix()
				return fmt.Sprintf("t=%d,v1=%s", ts, signFor("whsec_other", ts, payload))
			},
			secret: secret,
		},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			if s.verifyStripeSignature(payload, tc.header(), tc.secret) {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}

	t.Run("tampered payload is rejected", func(t *testing.T) {
		ts := time.Now().Unix()
		header := fmt.Sprintf("t=%d,v1=%s", ts, signFor(secret, ts, payload))
		tampered := []byte(`{"id":"evt_1","type":"invoice.payment_failed"}`)
		if s.verifyStripeSignature(tampered, header, secret) {
			t.Fatal("expected tampered payload to fail verification")
		}
	})
}
