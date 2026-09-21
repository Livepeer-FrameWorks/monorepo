// Package webhooksig signs and verifies webhook requests with the Standard
// Webhooks scheme (https://www.standardwebhooks.com): the signed content is
// "<webhook-id>.<webhook-timestamp>.<body>", signed with HMAC-SHA256, and each
// signature is sent as "v1,<base64>" in the space-separated webhook-signature
// header. Secrets are written as "whsec_<base64 key>".
package webhooksig

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header names defined by the Standard Webhooks specification.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// SecretPrefix precedes the base64 key in a secret's text form.
const SecretPrefix = "whsec_"

// SecretBytes is the length of a generated signing key.
const SecretBytes = 32

// DefaultTolerance is how far a timestamp may differ from the verifier's clock.
const DefaultTolerance = 5 * time.Minute

var (
	ErrEmptySecret         = errors.New("webhook secret is empty")
	ErrMissingHeaders      = errors.New("webhook-id, webhook-timestamp, and webhook-signature are required")
	ErrInvalidTimestamp    = errors.New("webhook-timestamp is not a Unix time in seconds")
	ErrTimestampOutOfRange = errors.New("webhook-timestamp is outside the tolerance")
	ErrNoMatchingSignature = errors.New("no webhook-signature matches")
)

// Key is one signing key.
type Key []byte

// ParseSecret decodes a "whsec_<base64>" secret. The prefix is optional.
func ParseSecret(secret string) (Key, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(secret), SecretPrefix))
	if err != nil {
		return nil, fmt.Errorf("decode webhook secret: %w", err)
	}
	if len(key) == 0 {
		return nil, ErrEmptySecret
	}
	return key, nil
}

// String renders the key in its "whsec_<base64>" form.
func (k Key) String() string {
	return SecretPrefix + base64.StdEncoding.EncodeToString(k)
}

// GenerateKey returns a new random key of SecretBytes bytes.
func GenerateKey() (Key, error) {
	key := make([]byte, SecretBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate webhook secret: %w", err)
	}
	return key, nil
}

// Sign returns the "v1,<base64>" signature of body for the message ID and
// timestamp.
func (k Key) Sign(id string, timestamp time.Time, body []byte) string {
	return "v1," + base64.StdEncoding.EncodeToString(k.mac(id, timestamp.Unix(), body))
}

func (k Key) mac(id string, unix int64, body []byte) []byte {
	h := hmac.New(sha256.New, k)
	h.Write([]byte(id))
	h.Write([]byte{'.'})
	h.Write([]byte(strconv.FormatInt(unix, 10)))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// Headers returns the three request headers for body, with one signature per
// key. Several keys are sent while a secret rotates, so a receiver holding
// either secret verifies the request.
func Headers(id string, timestamp time.Time, body []byte, keys ...Key) (http.Header, error) {
	if len(keys) == 0 {
		return nil, ErrEmptySecret
	}
	signatures := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) == 0 {
			return nil, ErrEmptySecret
		}
		signatures = append(signatures, key.Sign(id, timestamp, body))
	}
	h := http.Header{}
	h.Set(HeaderID, id)
	h.Set(HeaderTimestamp, strconv.FormatInt(timestamp.Unix(), 10))
	h.Set(HeaderSignature, strings.Join(signatures, " "))
	return h, nil
}

// Verify checks the headers of a received request against body. It accepts
// the request when any v1 signature in webhook-signature matches, and rejects
// a timestamp more than tolerance away from now. A zero tolerance uses
// DefaultTolerance.
func (k Key) Verify(body []byte, headers http.Header, now time.Time, tolerance time.Duration) error {
	if len(k) == 0 {
		return ErrEmptySecret
	}
	id := headers.Get(HeaderID)
	rawTimestamp := headers.Get(HeaderTimestamp)
	rawSignatures := headers.Get(HeaderSignature)
	if id == "" || rawTimestamp == "" || rawSignatures == "" {
		return ErrMissingHeaders
	}
	unix, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		return ErrInvalidTimestamp
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	sent := time.Unix(unix, 0)
	if now.Sub(sent) > tolerance || sent.Sub(now) > tolerance {
		return ErrTimestampOutOfRange
	}
	expected := k.mac(id, unix, body)
	for _, versioned := range strings.Split(rawSignatures, " ") {
		version, encoded, ok := strings.Cut(versioned, ",")
		if !ok || version != "v1" {
			continue
		}
		got, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			continue
		}
		if hmac.Equal(got, expected) {
			return nil
		}
	}
	return ErrNoMatchingSignature
}
