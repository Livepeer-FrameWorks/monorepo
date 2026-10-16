package server

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// ListenAddrs binds port on each address in addrs and returns one listener
// that accepts from all of them, or binds every interface when addrs holds no
// address. The addresses bind with FreeBind: provisioning includes the host's
// WireGuard address, which does not exist until Privateer brings the mesh up,
// and a service must not depend on that order.
func ListenAddrs(addrs []string, port string) (net.Listener, error) {
	return listenAddrs(nil, addrs, port, true)
}

// listenAddrs is listen once per address in addrs, merged into one listener;
// with no address it is listen on every interface with the given freeBind.
func listenAddrs(existing net.Listener, addrs []string, port string, freeBind bool) (net.Listener, error) {
	addrs = trimAddrs(addrs)
	if existing != nil || len(addrs) == 0 {
		return listen(existing, "", port, freeBind)
	}
	if len(addrs) == 1 {
		return listen(nil, addrs[0], port, true)
	}
	listeners := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		lis, err := listen(nil, addr, port, true)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("bind %s: %w", addr, err)
		}
		listeners = append(listeners, lis)
	}
	return newMultiListener(listeners), nil
}

func trimAddrs(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr = strings.TrimSpace(addr); addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

type acceptResult struct {
	conn net.Conn
	err  error
}

// multiListener accepts from several bound sockets as one net.Listener, so a
// single HTTP or gRPC server serves every address it binds and its graceful
// stop closes them together. Addr reports the first socket.
type multiListener struct {
	listeners []net.Listener
	results   chan acceptResult
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func newMultiListener(listeners []net.Listener) *multiListener {
	m := &multiListener{listeners: listeners, results: make(chan acceptResult), done: make(chan struct{})}
	for _, lis := range listeners {
		m.wg.Add(1)
		go m.acceptLoop(lis)
	}
	return m
}

func (m *multiListener) acceptLoop(lis net.Listener) {
	defer m.wg.Done()
	for {
		conn, err := lis.Accept()
		select {
		case m.results <- acceptResult{conn: conn, err: err}:
		case <-m.done:
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		// The server decides whether an accept error is fatal; a closed
		// socket ends this loop.
		if errors.Is(err, net.ErrClosed) {
			return
		}
	}
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case r := <-m.results:
		return r.conn, r.err
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *multiListener) Close() error {
	var errs []error
	m.closeOnce.Do(func() {
		close(m.done)
		for _, lis := range m.listeners {
			if err := lis.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		m.wg.Wait()
	})
	return errors.Join(errs...)
}

func (m *multiListener) Addr() net.Addr {
	return m.listeners[0].Addr()
}

// Addrs reports every bound socket's address.
func (m *multiListener) Addrs() []net.Addr {
	out := make([]net.Addr, 0, len(m.listeners))
	for _, lis := range m.listeners {
		out = append(out, lis.Addr())
	}
	return out
}
