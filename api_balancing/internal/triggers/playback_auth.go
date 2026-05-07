package triggers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

const (
	// maxWebhookTimeoutMs caps the customer-supplied timeout server-side.
	maxWebhookTimeoutMs = 10000
	// defaultWebhookTimeoutMs is used when the policy doesn't specify one.
	defaultWebhookTimeoutMs = 5000
	// maxWebhookResponseBytes caps how much of the customer response we read
	// (we only need a status code + small reason field).
	maxWebhookResponseBytes = 4096
)

// enforcePlaybackPolicy is the policy-decision routine called from
// handleUserNew. Returns ("true", _) to allow MistServer to start the
// session, or ("false", _) to deny.
//
// Decision matrix:
//  1. Known public marker → allow without fetching full policy.
//  2. Known protected or unknown marker → fetch full policy.
//  3. Policy fetch error while protected/unknown → deny.
//  4. type=jwt → JWS structural check, ES256 verify, claim/aud check.
//  5. type=webhook → SSRF-guarded HMAC-signed POST to customer URL.
//
// Every deny returns a distinct log reason. Operators need this to
// distinguish "customer-misconfigured" from "actual attack" from
// "infrastructure error."
func (p *Processor) enforcePlaybackPolicy(ctx context.Context, internalName string, marker streamContext, userNew *pb.ViewerConnectTrigger) (string, error) {
	if marker.RequiresAuthKnown && !marker.RequiresAuth {
		return "true", nil
	}
	if p.commodoreClient == nil {
		p.logPlaybackDeny(internalName, userNew, "policy-marker-unknown", "commodore client not configured")
		return "false", nil
	}

	policy, err := p.commodoreClient.ResolvePlaybackPolicyByInternalName(ctx, internalName)
	if err != nil {
		reason := "policy-fetch-failed"
		if !marker.RequiresAuthKnown {
			reason = "policy-marker-unknown"
		}
		p.logPlaybackDeny(internalName, userNew, reason, err.Error())
		return "false", nil
	}

	return p.evaluatePlaybackPolicy(ctx, internalName, userNew, policy, p.commodoreClient), nil
}

// SigningKeyUseRecorder records successful viewer-token verification for key audit metadata.
type SigningKeyUseRecorder interface {
	RecordSigningKeyUse(ctx context.Context, tenantID, kid string) error
}

// EvaluatePlaybackPolicy applies a resolved playback policy to a viewer
// request. USER_NEW and resolve-time checks share this routine so protected
// playback denies before URL disclosure and again before media delivery.
func EvaluatePlaybackPolicy(ctx context.Context, logger logging.Logger, internalName string, userNew *pb.ViewerConnectTrigger, policy *pb.ResolvePlaybackPolicyResponse) string {
	return evaluatePlaybackPolicyWithRecorder(ctx, logger, internalName, userNew, policy, nil)
}

// EvaluatePlaybackPolicyWithRecorder applies a resolved policy and records successful JWT key use.
func EvaluatePlaybackPolicyWithRecorder(ctx context.Context, logger logging.Logger, internalName string, userNew *pb.ViewerConnectTrigger, policy *pb.ResolvePlaybackPolicyResponse, recorder SigningKeyUseRecorder) string {
	return evaluatePlaybackPolicyWithRecorder(ctx, logger, internalName, userNew, policy, recorder)
}

func evaluatePlaybackPolicyWithRecorder(ctx context.Context, logger logging.Logger, internalName string, userNew *pb.ViewerConnectTrigger, policy *pb.ResolvePlaybackPolicyResponse, recorder SigningKeyUseRecorder) string {
	p := &Processor{logger: logger}
	return p.evaluatePlaybackPolicy(ctx, internalName, userNew, policy, recorder)
}

func (p *Processor) evaluatePlaybackPolicy(ctx context.Context, internalName string, userNew *pb.ViewerConnectTrigger, policy *pb.ResolvePlaybackPolicyResponse, recorder SigningKeyUseRecorder) string {
	if policy == nil {
		p.logPlaybackDeny(internalName, userNew, "policy-empty", "")
		return "false"
	}
	switch strings.ToLower(policy.GetType()) {
	case "public":
		return "true"
	case "jwt":
		return p.enforceJWTPolicy(internalName, userNew, policy.GetTenantId(), policy.GetJwtPolicy(), recorder)
	case "webhook":
		return p.enforceWebhookPolicy(ctx, internalName, userNew, policy.GetWebhookPolicy())
	default:
		p.logPlaybackDeny(internalName, userNew, "policy-unknown-type", policy.GetType())
		return "false"
	}
}

func (p *Processor) enforceJWTPolicy(internalName string, userNew *pb.ViewerConnectTrigger, tenantID string, policy *pb.PlaybackJwtPolicy, recorder SigningKeyUseRecorder) string {
	if policy == nil {
		p.logPlaybackDeny(internalName, userNew, "policy-jwt-empty", "")
		return "false"
	}
	token := userNew.GetViewerToken()
	if token == "" {
		p.logPlaybackDeny(internalName, userNew, "missing-token", "")
		return "false"
	}

	// Convert proto active keys to verifier keys.
	activeKeys := policy.GetActiveKeys()
	keys := make([]auth.SigningKey, 0, len(activeKeys))
	for _, k := range activeKeys {
		keys = append(keys, auth.SigningKey{Kid: k.GetKid(), PublicKeyPEM: k.GetPublicKeyPem()})
	}
	if len(keys) == 0 {
		p.logPlaybackDeny(internalName, userNew, "no-active-keys", "")
		return "false"
	}

	opts := auth.VerifyOptions{
		AllowedKids:      policy.GetAllowedKids(),
		RequiredAudience: policy.GetRequiredAudience(),
		RequiredClaims:   policy.GetRequiredClaimsJson(),
		// Allow up to 60s clock skew between the customer's mint server
		// and our verifier — matches typical JWT library defaults.
		SkewTolerance: 60 * time.Second,
	}

	if _, err := auth.VerifyViewerJWT(token, keys, opts); err != nil {
		p.logPlaybackDeny(internalName, userNew, jwtDenyReason(err), err.Error())
		return "false"
	}
	if kid, err := auth.ViewerJWTKid(token); err == nil {
		p.recordSigningKeyUse(recorder, tenantID, kid)
	}
	return "true"
}

func (p *Processor) recordSigningKeyUse(recorder SigningKeyUseRecorder, tenantID, kid string) {
	if recorder == nil || tenantID == "" || kid == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := recorder.RecordSigningKeyUse(ctx, tenantID, kid); err != nil && p.logger != nil {
			p.logger.WithError(err).WithFields(logging.Fields{
				"tenant_id": tenantID,
				"kid":       kid,
			}).Debug("record signing key use failed")
		}
	}()
}

func jwtDenyReason(err error) string {
	switch {
	case errors.Is(err, auth.ErrTokenNotJWS):
		return "jwt-not-a-jws"
	case errors.Is(err, auth.ErrMissingKid):
		return "jwt-missing-kid"
	case errors.Is(err, auth.ErrUnknownKid):
		return "jwt-unknown-kid"
	case errors.Is(err, auth.ErrWrongAlgorithm):
		return "jwt-wrong-alg"
	case errors.Is(err, auth.ErrSignatureFailed):
		return "jwt-sig-fail"
	case errors.Is(err, auth.ErrMissingExpiration):
		return "jwt-missing-exp"
	case errors.Is(err, auth.ErrTokenExpired):
		return "jwt-expired"
	case errors.Is(err, auth.ErrTokenNotYetValid):
		return "jwt-not-yet-valid"
	case errors.Is(err, auth.ErrAudienceMismatch):
		return "jwt-aud-mismatch"
	case errors.Is(err, auth.ErrRequiredClaimMiss):
		return "jwt-claim-mismatch"
	case errors.Is(err, auth.ErrInvalidPublicKey):
		return "jwt-bad-public-key"
	}
	return "jwt-verify-error"
}

// enforceWebhookPolicy POSTs to the customer URL with an HMAC-signed body.
// Allow only on 200; everything else (403, other 4xx, 5xx, timeout, network
// error, blocked SSRF target) denies.
func (p *Processor) enforceWebhookPolicy(ctx context.Context, internalName string, userNew *pb.ViewerConnectTrigger, policy *pb.PlaybackWebhookPolicy) string {
	if policy == nil || policy.GetUrl() == "" {
		p.logPlaybackDeny(internalName, userNew, "webhook-no-url", "")
		return "false"
	}
	timeoutMs := int(policy.GetTimeoutMs())
	if timeoutMs <= 0 {
		timeoutMs = defaultWebhookTimeoutMs
	}
	if timeoutMs > maxWebhookTimeoutMs {
		timeoutMs = maxWebhookTimeoutMs
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond

	// Build outbound payload. Customer signs against this exact body.
	body, err := json.Marshal(map[string]any{
		"streamName":  userNew.GetStreamName(),
		"sessionId":   userNew.GetSessionId(),
		"viewerIp":    userNew.GetHost(),
		"requestUrl":  userNew.GetRequestUrl(),
		"viewerToken": userNew.GetViewerToken(),
		"connector":   userNew.GetConnector(),
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		p.logPlaybackDeny(internalName, userNew, "webhook-encode-payload", err.Error())
		return "false"
	}

	mac := hmac.New(sha256.New, []byte(policy.GetSecretPt()))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	httpClient := newSSRFHardenedClient(timeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, policy.GetUrl(), bytes.NewReader(body))
	if err != nil {
		p.logPlaybackDeny(internalName, userNew, "webhook-build-request", err.Error())
		return "false"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Frameworks-Signature", "sha256="+sig)
	req.Header.Set("User-Agent", "FrameWorks-PlaybackPolicy/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		// Distinguish SSRF blocks from network errors so operators can tell
		// "customer misconfigured a private-IP webhook" from "their server
		// timed out."
		if errors.Is(err, errSSRFBlocked) {
			p.logPlaybackDeny(internalName, userNew, "webhook-blocked-ssrf", err.Error())
			return "false"
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			p.logPlaybackDeny(internalName, userNew, "webhook-timeout", err.Error())
			return "false"
		}
		p.logPlaybackDeny(internalName, userNew, "webhook-network", err.Error())
		return "false"
	}
	// Bound the body read so a malicious customer can't tarpit Foghorn.
	if _, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxWebhookResponseBytes)); copyErr != nil {
		// Best-effort drain; the status code drives the decision.
		p.logger.WithError(copyErr).Debug("webhook response body drain errored")
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		p.logger.WithError(closeErr).Debug("webhook response body close errored")
	}

	if resp.StatusCode == http.StatusOK {
		return "true"
	}
	if resp.StatusCode == http.StatusForbidden {
		p.logPlaybackDeny(internalName, userNew, "webhook-deny-403", "")
		return "false"
	}
	p.logPlaybackDeny(internalName, userNew, "webhook-error-status", fmt.Sprintf("%d", resp.StatusCode))
	return "false"
}

func (p *Processor) logPlaybackDeny(internalName string, userNew *pb.ViewerConnectTrigger, reason, detail string) {
	if p.metrics != nil {
		if p.metrics.PlaybackDenyTotal != nil {
			p.metrics.PlaybackDenyTotal.WithLabelValues(reason).Inc()
		}
		if p.metrics.PlaybackWebhookErrors != nil {
			if class, ok := strings.CutPrefix(reason, "webhook-"); ok {
				p.metrics.PlaybackWebhookErrors.WithLabelValues(class).Inc()
			}
		}
	}
	if p.logger == nil {
		return
	}
	fields := logging.Fields{
		"reason":        reason,
		"internal_name": internalName,
		"session_id":    userNew.GetSessionId(),
		"connector":     userNew.GetConnector(),
		"viewer_ip":     userNew.GetHost(),
	}
	if detail != "" {
		fields["detail"] = detail
	}
	p.logger.WithFields(fields).Info("Playback access denied")
}

// ----------------------------------------------------------------------------
// SSRF-hardened HTTP client
// ----------------------------------------------------------------------------

// errSSRFBlocked is returned by the dialer Control hook when a resolved IP
// falls in the blocklist. Wrapped in OpError by the net stack; we use
// errors.Is to detect via the sentinel.
var errSSRFBlocked = errors.New("dial blocked: target IP is in the SSRF deny-list")

func newSSRFHardenedClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: timeout,
		// Control runs after DNS, before connect. Re-validating here is the
		// only defense against DNS rebinding (host returns a public IP at
		// create-time validation, then a private IP at dial-time).
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			if isBlockedDialIP(ip) {
				return errSSRFBlocked
			}
			return nil
		},
	}
	transport := &http.Transport{
		DialContext: dialer.DialContext,
		// Disable connection reuse to force re-resolution on every webhook.
		// Customer DNS might rotate; safer to dial fresh than reuse a
		// previously-validated connection.
		DisableKeepAlives: true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// Don't follow redirects — customers should return 200 directly. A
		// redirect target could re-resolve to a blocked IP.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// isBlockedDialIP enforces the SSRF blocklist at dial time.
// Mirrors the create-time validator in api_control/internal/grpc/playback_access_control.go.
func isBlockedDialIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsPrivate() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip.Is4() {
		v4 := ip.As4()
		// 100.64.0.0/10 (CGNAT).
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		// 0.0.0.0/8.
		if v4[0] == 0 {
			return true
		}
	}
	if ip.Is4In6() {
		return isBlockedDialIP(ip.Unmap())
	}
	return false
}
