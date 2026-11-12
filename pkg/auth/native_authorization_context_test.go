package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func nativeAuthorizationFixture() NativeAuthorizationRequest {
	digest := sha256.Sum256([]byte("native-request-verifier-with-sufficient-entropy"))
	return NativeAuthorizationRequest{ClientID: "tray-mac", RedirectURI: "http://127.0.0.1:54321/callback", CodeChallenge: base64.RawURLEncoding.EncodeToString(digest[:]), CodeChallengeMethod: "S256", Scope: "account", State: "native-state", DeviceLabel: "Original workstation", ClientVersion: "tray macOS arm64", IP: "203.0.113.14", UserAgent: "FrameWorks-Tray/test"}
}

func TestNativeAuthorizationContextBindsObservedRequest(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	key := []byte("native-request-context-test-key")
	request := nativeAuthorizationFixture()
	token, err := SignNativeAuthorizationContext(request, key, now)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := VerifyNativeAuthorizationContext(token, key, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	request.Version = 1
	request.RequestedAt = now.Unix()
	request.ExpiresAt = now.Add(NativeAuthorizationContextTTL).Unix()
	if decoded != request {
		t.Fatalf("observed request changed: got %+v want %+v", decoded, request)
	}
	if _, err = ValidateInteractiveJWT(token, key); err == nil {
		t.Fatal("consent context authenticated as a user bearer")
	}
	if _, err = VerifyNativeAuthorizationContext(token, key, now.Add(NativeAuthorizationContextTTL-time.Second)); err != nil {
		t.Fatal("context expired prematurely", err)
	}
	for _, test := range []struct {
		name  string
		token string
		key   []byte
		now   time.Time
	}{
		{"wrong-key", token, []byte("other-key"), now},
		{"no-key", token, nil, now},
		{"expired", token, key, now.Add(NativeAuthorizationContextTTL)},
		{"future", token, key, now.Add(-31 * time.Second)},
		{"missing-signature", strings.Split(token, ".")[0], key, now},
		{"oversized", strings.Repeat("x", 8193), key, now},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, verifyErr := VerifyNativeAuthorizationContext(test.token, test.key, test.now); verifyErr == nil {
				t.Fatal("invalid context accepted")
			}
		})
	}
	payload, signature, _ := strings.Cut(token, ".")
	bytes, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	tampered := base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(bytes), "Original workstation", "Forged workstation", 1))) + "." + signature
	if _, err = VerifyNativeAuthorizationContext(tampered, key, now); err == nil {
		t.Fatal("altered consent metadata accepted")
	}
}

func TestNativeAuthorizationContextRefusesInvalidBindings(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*NativeAuthorizationRequest)
	}{
		{"unknown-client", func(r *NativeAuthorizationRequest) { r.ClientID = "unknown-client" }},
		{"remote-redirect", func(r *NativeAuthorizationRequest) { r.RedirectURI = "https://evil.example/callback" }},
		{"named-loopback", func(r *NativeAuthorizationRequest) { r.RedirectURI = "http://localhost:54321/callback" }},
		{"user-info", func(r *NativeAuthorizationRequest) { r.RedirectURI = "http://user@127.0.0.1:54321/callback" }},
		{"callback-query", func(r *NativeAuthorizationRequest) { r.RedirectURI = "http://127.0.0.1:54321/callback?next=evil" }},
		{"missing-port", func(r *NativeAuthorizationRequest) { r.RedirectURI = "http://127.0.0.1/callback" }},
		{"wrong-path", func(r *NativeAuthorizationRequest) { r.RedirectURI = "http://127.0.0.1:54321/other" }},
		{"plain-challenge", func(r *NativeAuthorizationRequest) { r.CodeChallengeMethod = "plain" }},
		{"short-challenge", func(r *NativeAuthorizationRequest) { r.CodeChallenge = "short" }},
		{"unknown-scope", func(r *NativeAuthorizationRequest) { r.Scope = "admin" }},
		{"missing-state", func(r *NativeAuthorizationRequest) { r.State = "" }},
		{"invalid-address", func(r *NativeAuthorizationRequest) { r.IP = "spoofed" }},
		{"large-label", func(r *NativeAuthorizationRequest) { r.DeviceLabel = strings.Repeat("x", 201) }},
		{"invalid-utf8", func(r *NativeAuthorizationRequest) { r.DeviceLabel = "bad\xff" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := nativeAuthorizationFixture()
			test.change(&request)
			if _, err := SignNativeAuthorizationContext(request, []byte("test-key"), time.Now()); err == nil {
				t.Fatal("invalid binding was signed")
			}
		})
	}
}
