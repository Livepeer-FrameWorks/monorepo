package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// upstreamBounds limits each phase of a platform upstream fetch (S3 presigned
// GETs and peer-relay pulls).
type upstreamBounds struct {
	dial           time.Duration
	tlsHandshake   time.Duration
	responseHeader time.Duration
	// readStall is how long one body read may wait for a byte.
	readStall time.Duration
}

// relayUpstreamBounds are the bounds New gives the platform upstream client.
// The response-header bound matches relayWriteStallTimeout: a peer relay
// answers a block only once its own cold fetch produced the first byte. The
// read-stall bound is half of Mist's source read budget: Mist abandons a read
// that got no data for mistSourceReadTimeout, so a stalled fetch is given up
// and resumed early enough for the resumed bytes to reach Mist before it does.
var relayUpstreamBounds = upstreamBounds{
	dial:           tenantSourceDialTimeout,
	tlsHandshake:   tenantSourceTLSDialTimeout,
	responseHeader: relayWriteStallTimeout,
	readStall:      mistSourceReadTimeout / 2,
}

// upstreamTarget is where an artifact's bytes are fetched from: the upstream
// URL, the peer-relay grant sent with it (empty for S3), and the client for it.
type upstreamTarget struct {
	url     string
	grantID string
	client  *http.Client
}

// errUpstreamStalled is the read error of an upstream body that produced no
// byte within the read-stall bound.
var errUpstreamStalled = errors.New("upstream read stalled")

// newRelayUpstreamClient returns the client for S3 and peer-relay fetches.
func newRelayUpstreamClient(b upstreamBounds) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	var tr *http.Transport
	if ok {
		tr = base.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	tr.DialContext = (&net.Dialer{Timeout: b.dial, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = b.tlsHandshake
	tr.ResponseHeaderTimeout = b.responseHeader
	return &http.Client{Transport: &stallBoundTransport{base: tr, readStall: b.readStall}}
}

// stallBoundTransport ends a response whose body stops producing bytes: every
// body Read that waits longer than readStall cancels the request, and that
// Read returns errUpstreamStalled. Time the caller spends between Reads (a
// slow client write) does not count.
type stallBoundTransport struct {
	base      http.RoundTripper
	readStall time.Duration
}

func (t *stallBoundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	if t.readStall > 0 {
		resp.Body = &stallBoundBody{body: resp.Body, ctx: ctx, cancel: cancel, stall: t.readStall}
	} else {
		resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelCauseFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel(nil)
	return err
}

type stallBoundBody struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	stall  time.Duration

	mu    sync.Mutex
	timer *time.Timer
}

func (b *stallBoundBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.timer == nil {
		b.timer = time.AfterFunc(b.stall, func() { b.cancel(errUpstreamStalled) })
	} else {
		b.timer.Reset(b.stall)
	}
	b.mu.Unlock()
	n, err := b.body.Read(p)
	b.mu.Lock()
	b.timer.Stop()
	b.mu.Unlock()
	if err != nil && errors.Is(context.Cause(b.ctx), errUpstreamStalled) {
		return n, fmt.Errorf("%w: no body byte for %s", errUpstreamStalled, b.stall)
	}
	return n, err
}

func (b *stallBoundBody) Close() error {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
	err := b.body.Close()
	b.cancel(nil)
	return err
}
