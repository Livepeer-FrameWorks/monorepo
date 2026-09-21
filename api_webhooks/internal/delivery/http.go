// Package delivery sends webhook requests: it renders the event body, signs
// it with the Standard Webhooks scheme, makes the HTTP call through a client
// that applies the destination policy to every connection, and runs the
// worker pool that claims, sends, and settles deliveries.
package delivery

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/webhooksig"
)

const (
	// AttemptTimeout bounds one HTTP attempt, including connect and reading
	// the response.
	AttemptTimeout = 10 * time.Second
	// maxResponseRead is how much of a response body is read.
	maxResponseRead = 4 << 10
	// UserAgent identifies Bosun's requests.
	UserAgent = "FrameWorks-Webhooks/1.0"
)

// Error classes stored with failed attempts.
const (
	ClassHTTPStatus         = "http_status"
	ClassRedirect           = "redirect"
	ClassTimeout            = "timeout"
	ClassConnection         = "connection"
	ClassTLS                = "tls"
	ClassDNS                = "dns"
	ClassBlockedDestination = "blocked_destination"
	// ClassInternal marks a delivery Bosun could not send for a reason of its
	// own (an unusable signing secret, an event it cannot render). No HTTP
	// attempt is recorded and the endpoint's failure streak is not touched.
	ClassInternal = "internal"
)

// ClientOptions configures NewHTTPClient.
type ClientOptions struct {
	// Policy is applied to the address of every connection. Production uses
	// the zero policy: public destinations only, no exceptions.
	Policy restream.DestinationPolicy
	// RootCAs replaces the system roots; nil uses the system roots.
	RootCAs *x509.CertPool
	// OnlyAddress, when set, refuses every connection to another ip:port in
	// addition to the policy. Tests pin their client to one local receiver
	// with it; production leaves it empty.
	OnlyAddress string
}

// NewHTTPClient returns the client every delivery uses: no proxy, no
// redirects, no connection reuse (each request resolves and dials afresh, so
// the policy sees the current DNS answer), TLS 1.2 or later, and the destination
// policy enforced in the dialer's Control hook after DNS resolution.
func NewHTTPClient(opts ClientOptions) *http.Client {
	policyControl := opts.Policy.DialControl()
	control := func(network, address string, c syscall.RawConn) error {
		if opts.OnlyAddress != "" && address != opts.OnlyAddress {
			return fmt.Errorf("%w: %s is not the pinned address", restream.ErrDialBlocked, address)
		}
		return policyControl(network, address, c)
	}
	dialer := &net.Dialer{Timeout: AttemptTimeout, Control: control}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   AttemptTimeout,
		ResponseHeaderTimeout: AttemptTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.RootCAs},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   AttemptTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Sender makes signed webhook requests.
type Sender struct {
	HTTP *http.Client
	Now  func() time.Time
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Send posts body to url with webhook-id msgID and one signature per key, and
// classifies the result. A 2xx response is a success; any other response or
// transport error is a failure.
func (s *Sender) Send(ctx context.Context, url, msgID string, body []byte, keys []webhooksig.Key) ledger.Outcome {
	started := s.now()
	out := ledger.Outcome{AttemptedAt: started.UTC()}
	headers, err := webhooksig.Headers(msgID, started, body, keys...)
	if err != nil {
		out.ErrorClass = ClassConnection
		return out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		out.ErrorClass = ClassConnection
		return out
	}
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.HTTP.Do(req)
	out.Latency = s.now().Sub(started)
	if err != nil {
		out.ErrorClass = classify(err)
		return out
	}
	defer func() { _ = resp.Body.Close() }()
	// The status code decides the outcome; a body that fails partway keeps
	// the bytes read so far as the excerpt.
	read, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseRead))
	out.Latency = s.now().Sub(started)
	out.StatusCode = resp.StatusCode
	out.Excerpt = string(read)
	if readErr != nil && len(read) == 0 {
		out.Excerpt = "(the response body could not be read)"
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out.Success = true
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		out.ErrorClass = ClassRedirect
	default:
		out.ErrorClass = ClassHTTPStatus
	}
	return out
}

// classify maps a transport error to an error class.
func classify(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var recordErr tls.RecordHeaderError
	switch {
	case errors.Is(err, restream.ErrDialBlocked):
		return ClassBlockedDestination
	case errors.As(err, &dnsErr):
		return ClassDNS
	case errors.As(err, &certErr), errors.As(err, &unknownAuthority), errors.As(err, &hostnameErr), errors.As(err, &recordErr):
		return ClassTLS
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return ClassTimeout
	case strings.Contains(err.Error(), "tls:"):
		return ClassTLS
	default:
		return ClassConnection
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
