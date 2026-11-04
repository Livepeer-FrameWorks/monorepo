// Package latencyproxy supplies a bounded TCP delay for real-engine tests.
package latencyproxy

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy delays both directions by half the configured round trip. Changing the
// delay affects existing connections, so fixtures can warm a pool before a test.
type Proxy struct {
	listener net.Listener
	target   string
	delay    atomic.Int64
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func Start(ctx context.Context, target string) (*Proxy, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &Proxy{listener: listener, target: target, ctx: ctx, cancel: cancel}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

func (p *Proxy) Address() string { return p.listener.Addr().String() }

func (p *Proxy) SetRoundTrip(delay time.Duration) { p.delay.Store(int64(max(delay, 0) / 2)) }

func (p *Proxy) Close() error {
	p.cancel()
	err := p.listener.Close()
	p.wg.Wait()
	return err
}

func (p *Proxy) serve() {
	defer p.wg.Done()
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer client.Close()
			server, err := (&net.Dialer{}).DialContext(p.ctx, "tcp", p.target)
			if err != nil {
				return
			}
			defer server.Close()
			stop := context.AfterFunc(p.ctx, func() {
				_ = client.Close()
				_ = server.Close()
			})
			defer stop()
			done := make(chan struct{})
			go func() {
				p.copy(server, client)
				_ = server.Close()
				close(done)
			}()
			p.copy(client, server)
			_ = client.Close()
			<-done
		}()
	}
}

func (p *Proxy) copy(dst io.Writer, src io.Reader) {
	buffer := make([]byte, 64*1024)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if delay := time.Duration(p.delay.Load()); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-p.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
