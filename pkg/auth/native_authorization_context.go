package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// NativeAuthorizationContextTTL bounds the browser approval window independently of code redemption.
const NativeAuthorizationContextTTL = 5 * time.Minute
const nativeAuthorizationContextDomain = "frameworks/native-authorization/v1\x00"

// NativeAuthorizationRequest binds observed native-client metadata to a PKCE
// request. It carries no user identity and cannot authenticate an API request.
type NativeAuthorizationRequest struct {
	Version             int    `json:"version"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	DeviceLabel         string `json:"device_label"`
	ClientVersion       string `json:"client_version"`
	IP                  string `json:"request_ip"`
	UserAgent           string `json:"user_agent"`
	RequestedAt         int64  `json:"requested_at"`
	ExpiresAt           int64  `json:"expires_at"`
}

func validateNativeAuthorizationRequest(r NativeAuthorizationRequest) error {
	if r.ClientID != "tray-mac" || r.CodeChallengeMethod != "S256" || r.Scope != "account" || len(r.State) == 0 || len(r.State) > 1024 {
		return errors.New("invalid native authorization request")
	}
	challenge, err := base64.RawURLEncoding.DecodeString(r.CodeChallenge)
	if err != nil || len(challenge) != sha256.Size {
		return errors.New("invalid PKCE challenge")
	}
	if len(r.RedirectURI) > 2048 {
		return errors.New("invalid loopback redirect")
	}
	target, err := url.Parse(r.RedirectURI)
	if err != nil || target.Scheme != "http" || target.User != nil || target.Path != "/callback" || target.RawQuery != "" || target.Fragment != "" || target.Opaque != "" {
		return errors.New("invalid loopback redirect")
	}
	if target.Hostname() != "127.0.0.1" && target.Hostname() != "::1" {
		return errors.New("invalid loopback host")
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid loopback port")
	}
	if r.IP != "" && net.ParseIP(r.IP) == nil {
		return errors.New("invalid request address")
	}
	for _, hint := range []struct {
		value string
		limit int
	}{{r.DeviceLabel, 200}, {r.ClientVersion, 100}, {r.UserAgent, 1024}} {
		if !utf8.ValidString(hint.value) || utf8.RuneCountInString(hint.value) > hint.limit {
			return errors.New("invalid native device metadata")
		}
	}
	return nil
}

// SignNativeAuthorizationContext authenticates request provenance, without issuing a user credential.
func SignNativeAuthorizationContext(request NativeAuthorizationRequest, key []byte, now time.Time) (string, error) {
	if len(key) == 0 {
		return "", errors.New("authorization context signing key unavailable")
	}
	if request.Scope == "" {
		request.Scope = "account"
	}
	if err := validateNativeAuthorizationRequest(request); err != nil {
		return "", err
	}
	request.Version = 1
	request.RequestedAt = now.Unix()
	request.ExpiresAt = now.Add(NativeAuthorizationContextTTL).Unix()
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	if len(payload) > 4096 {
		return "", errors.New("native authorization context too large")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(nativeAuthorizationContextDomain + encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyNativeAuthorizationContext rejects changed metadata and expired or incompatible bindings.
func VerifyNativeAuthorizationContext(token string, key []byte, now time.Time) (NativeAuthorizationRequest, error) {
	var request NativeAuthorizationRequest
	invalid := errors.New("invalid or expired native authorization context")
	if len(key) == 0 || len(token) > 8192 {
		return request, invalid
	}
	encoded, signature, ok := strings.Cut(token, ".")
	if !ok {
		return request, invalid
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return request, invalid
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(nativeAuthorizationContextDomain + encoded))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return request, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) > 4096 {
		return request, invalid
	}
	if err = json.Unmarshal(payload, &request); err != nil {
		return NativeAuthorizationRequest{}, invalid
	}
	if request.Version != 1 || request.ExpiresAt-request.RequestedAt != int64(NativeAuthorizationContextTTL/time.Second) || request.RequestedAt > now.Unix()+30 || now.Unix() >= request.ExpiresAt {
		return NativeAuthorizationRequest{}, invalid
	}
	if err = validateNativeAuthorizationRequest(request); err != nil {
		return NativeAuthorizationRequest{}, invalid
	}
	return request, nil
}
