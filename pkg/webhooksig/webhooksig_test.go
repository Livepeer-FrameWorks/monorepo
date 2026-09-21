package webhooksig

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Vectors from the Standard Webhooks reference library
// (standard-webhooks/libraries/go/webhook_test.go).
const (
	vectorSecret    = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	vectorMsgID     = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	vectorTimestamp = 1614265330
	vectorPayload   = `{"test": 2432232314}`
	vectorSignature = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
	foreignSig      = "v1,Ceo5qEr07ixe2NLpvHk3FH9bwy/WavXrAFQ/9tdO6mc="
)

func vectorKey(t *testing.T) Key {
	t.Helper()
	key, err := ParseSecret(vectorSecret)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSignMatchesPublishedVector(t *testing.T) {
	key := vectorKey(t)
	if got := key.Sign(vectorMsgID, time.Unix(vectorTimestamp, 0), []byte(vectorPayload)); got != vectorSignature {
		t.Fatalf("Sign = %s, want %s", got, vectorSignature)
	}
	unprefixed, err := ParseSecret(strings.TrimPrefix(vectorSecret, SecretPrefix))
	if err != nil {
		t.Fatal(err)
	}
	if got := unprefixed.Sign(vectorMsgID, time.Unix(vectorTimestamp, 0), []byte(vectorPayload)); got != vectorSignature {
		t.Fatalf("unprefixed secret Sign = %s, want %s", got, vectorSignature)
	}
	if key.String() != vectorSecret {
		t.Fatalf("String = %s, want %s", key.String(), vectorSecret)
	}
}

func TestVerifyReferenceCases(t *testing.T) {
	key := vectorKey(t)
	now := time.Unix(vectorTimestamp, 0)
	valid := func(ts time.Time) http.Header {
		h, err := Headers(vectorMsgID, ts, []byte(vectorPayload), key)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	cases := []struct {
		name   string
		header func() http.Header
		want   error
	}{
		{"valid signature is valid", func() http.Header { return valid(now) }, nil},
		{"missing id", func() http.Header { h := valid(now); h.Del(HeaderID); return h }, ErrMissingHeaders},
		{"missing timestamp", func() http.Header { h := valid(now); h.Del(HeaderTimestamp); return h }, ErrMissingHeaders},
		{"missing signature", func() http.Header { h := valid(now); h.Del(HeaderSignature); return h }, ErrMissingHeaders},
		{"invalid signature", func() http.Header { h := valid(now); h.Set(HeaderSignature, foreignSig); return h }, ErrNoMatchingSignature},
		{"partial signature", func() http.Header { h := valid(now); h.Set(HeaderSignature, "v1,"); return h }, ErrNoMatchingSignature},
		{"old timestamp", func() http.Header { return valid(now.Add(-DefaultTolerance - time.Second)) }, ErrTimestampOutOfRange},
		{"new timestamp", func() http.Header { return valid(now.Add(DefaultTolerance + time.Second)) }, ErrTimestampOutOfRange},
		{"non-numeric timestamp", func() http.Header { h := valid(now); h.Set(HeaderTimestamp, "yesterday"); return h }, ErrInvalidTimestamp},
		{"valid multi signature", func() http.Header {
			h := valid(now)
			h.Set(HeaderSignature, strings.Join([]string{foreignSig, "v2," + strings.TrimPrefix(foreignSig, "v1,"), h.Get(HeaderSignature), foreignSig}, " "))
			return h
		}, nil},
		{"the right MAC under another version is ignored", func() http.Header {
			h := valid(now)
			h.Set(HeaderSignature, "v2,"+strings.TrimPrefix(h.Get(HeaderSignature), "v1,"))
			return h
		}, ErrNoMatchingSignature},
	}
	for _, tc := range cases {
		err := key.Verify([]byte(vectorPayload), tc.header(), now, 0)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: Verify = %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := key.Verify([]byte(vectorPayload+" "), valid(now), now, 0); !errors.Is(err, ErrNoMatchingSignature) {
		t.Fatalf("a modified body must not verify: %v", err)
	}
}

func TestHeadersCarryOneSignaturePerKeyDuringRotation(t *testing.T) {
	current, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	previous := vectorKey(t)
	now := time.Unix(vectorTimestamp, 0)
	h, err := Headers(vectorMsgID, now, []byte(vectorPayload), current, previous)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(h.Get(HeaderSignature), "v1,"); got != 2 {
		t.Fatalf("signature header %q carries %d signatures, want 2", h.Get(HeaderSignature), got)
	}
	if !strings.Contains(h.Get(HeaderSignature), vectorSignature) {
		t.Fatalf("previous key's signature missing from %q", h.Get(HeaderSignature))
	}
	for _, key := range []Key{current, previous} {
		if err := key.Verify([]byte(vectorPayload), h, now, 0); err != nil {
			t.Fatalf("receiver holding one of the rotating secrets must verify: %v", err)
		}
	}
	if _, err := Headers(vectorMsgID, now, nil); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("Headers without a key = %v, want ErrEmptySecret", err)
	}
}

func TestParseSecretRejectsEmpty(t *testing.T) {
	for _, secret := range []string{"", SecretPrefix} {
		if _, err := ParseSecret(secret); !errors.Is(err, ErrEmptySecret) {
			t.Errorf("ParseSecret(%q) = %v, want ErrEmptySecret", secret, err)
		}
	}
	if _, err := ParseSecret("whsec_***"); err == nil {
		t.Error("non-base64 secret must fail")
	}
}
