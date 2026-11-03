package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// freezableProxy forwards TCP connections to target. freeze makes every
// connection open at that moment drop its bytes in both directions without
// closing, the way a path to an object store goes silent; connections accepted
// later forward normally.
type freezableProxy struct {
	ln      net.Listener
	target  string
	mu      sync.Mutex
	conns   []*atomic.Bool
	accepts atomic.Int32
}

func newFreezableProxy(t *testing.T, target string) *freezableProxy {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &freezableProxy{ln: ln, target: target}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			client, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			p.accepts.Add(1)
			server, dialErr := (&net.Dialer{}).DialContext(context.Background(), "tcp", target)
			if dialErr != nil {
				_ = client.Close()
				continue
			}
			frozen := &atomic.Bool{}
			p.mu.Lock()
			p.conns = append(p.conns, frozen)
			p.mu.Unlock()
			pipe := func(dst, src net.Conn) {
				buf := make([]byte, 32*1024)
				for {
					n, readErr := src.Read(buf)
					if n > 0 && !frozen.Load() {
						if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
							return
						}
					}
					if readErr != nil {
						_ = dst.Close()
						return
					}
				}
			}
			go pipe(server, client)
			go pipe(client, server)
		}
	}()
	return p
}

func (p *freezableProxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, frozen := range p.conns {
		frozen.Store(true)
	}
}

// TestRelayUpstreamClientDoesNotReuseAStalledConnection: the path to the
// object store goes silent while a block streams, so the read stalls and the
// relay resumes. The resumed request must not travel on the connection that
// went silent: on a reused connection it waits out the response header bound
// and the viewer's read fails.
func TestRelayUpstreamClientDoesNotReuseAStalledConnection(t *testing.T) {
	body := patternBody(4 << 20)
	var requests atomic.Int32
	firstBytes := make(chan struct{})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Length", "4194304")
		if n == 1 {
			_, _ = w.Write(body[:64<<10])
			w.(http.Flusher).Flush()
			close(firstBytes)
			_, _ = w.Write(body[64<<10:])
			return
		}
		_, _ = w.Write(body)
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	proxy := newFreezableProxy(t, upstream.Listener.Addr().String())

	client := newRelayUpstreamClient(testUpstreamBounds)
	tr := client.Transport.(*stallBoundTransport).base.(*http.Transport)
	tr.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	url := "https://" + proxy.ln.Addr().String() + "/block"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-firstBytes
	proxy.freeze()
	_, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if !errors.Is(err, errUpstreamStalled) {
		t.Fatalf("first read error = %v, want the stall", err)
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request after a stalled read failed (proxy accepted %d connection(s)): %v", proxy.accepts.Load(), err)
	}
	got, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("request after a stalled read read %d of %d bytes: %v", len(got), len(body), err)
	}
}
