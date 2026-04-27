package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tunnel is an active `ssh -N -L` local-port-forward. The forwarder is a child
// `ssh` process owned by this struct; Close() terminates it.
type Tunnel struct {
	LocalAddr  string // "127.0.0.1:<port>"
	RemoteHost string // remote bind interface (typically 127.0.0.1)
	RemotePort int

	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stderr  *bytes.Buffer
	closeMu sync.Mutex
	closed  bool
}

// LocalPort returns the local TCP port the tunnel is bound to.
func (t *Tunnel) LocalPort() int {
	_, port, err := net.SplitHostPort(t.LocalAddr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}

// Close terminates the ssh forwarder and waits for it to exit. Safe to call
// multiple times.
func (t *Tunnel) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	if t.cmd == nil {
		return nil
	}
	// Wait drains the child. A context-cancel teardown looks like a non-zero
	// exit, which is expected; surfacing it would give callers a noisy error
	// they cannot act on.
	_ = t.cmd.Wait() //nolint:errcheck // expected on context-cancel teardown
	return nil
}

// LocalForwardOptions configures a single SSH local-forward.
type LocalForwardOptions struct {
	// Config + Resolution describe the SSH target. When Resolution is zero,
	// it's derived from Config via NewClient's resolver. Callers that already
	// hold a resolved Client can pass its config + resolution directly.
	Config     *ConnectionConfig
	Resolution Resolution

	// RemoteHost is the bind interface on the remote side. Default 127.0.0.1.
	RemoteHost string
	// RemotePort is the port to forward to on the remote host. Required.
	RemotePort int

	// LocalPort is the local port to bind. When 0, a free ephemeral port is
	// chosen via net.Listen("tcp", "127.0.0.1:0").
	LocalPort int

	// ReadyTimeout bounds the post-spawn TCP probe. Default 5s.
	ReadyTimeout time.Duration
}

// tunnelReady is the readiness check applied after the ssh forwarder is
// spawned. It is a package var so tests can replace it; the default does a
// TCP dial loop until the local listener answers or the deadline elapses.
var tunnelReady = defaultTunnelReady

func defaultTunnelReady(ctx context.Context, addr string, deadline time.Time) error {
	const probeInterval = 50 * time.Millisecond
	var dialer net.Dialer
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		conn, err := dialer.DialContext(probeCtx, "tcp", addr)
		cancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tunnel %s not ready before deadline: %w", addr, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(probeInterval):
		}
	}
}

// LocalForward opens an `ssh -N -L 127.0.0.1:<local>:<remoteHost>:<remote>`
// child process and returns a handle once a TCP probe to the local port
// succeeds. The forwarder lives until Close() (or the parent ctx is canceled).
func LocalForward(ctx context.Context, opts LocalForwardOptions) (*Tunnel, error) {
	if opts.Config == nil {
		return nil, errors.New("LocalForward: Config is required")
	}
	if opts.RemotePort <= 0 || opts.RemotePort > 65535 {
		return nil, fmt.Errorf("LocalForward: RemotePort %d out of range", opts.RemotePort)
	}
	remoteHost := opts.RemoteHost
	if remoteHost == "" {
		remoteHost = "127.0.0.1"
	}

	resolution := opts.Resolution
	if resolution.Target == "" {
		resolveCtx, cancel := context.WithTimeout(ctx, resolveTimeout(opts.Config))
		defer cancel()
		res, err := (&DefaultResolver{}).Resolve(resolveCtx, opts.Config)
		if err != nil {
			return nil, fmt.Errorf("LocalForward: resolve target: %w", err)
		}
		resolution = res
	}

	localPort := opts.LocalPort
	if localPort == 0 {
		p, err := pickFreeLocalPort(ctx)
		if err != nil {
			return nil, fmt.Errorf("LocalForward: pick local port: %w", err)
		}
		localPort = p
	}
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)

	args := BuildSSHArgs(opts.Config, resolution)
	args = append(args,
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("%s:%d:%s:%d", "127.0.0.1", localPort, remoteHost, opts.RemotePort),
		resolution.Target,
	)

	// The forwarder must outlive this function. We derive a cancellable
	// context from the parent so cancel-from-above still tears it down.
	tunnelCtx, cancel := context.WithCancel(ctx)
	cmd := execCommandContext(tunnelCtx, "ssh", args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("LocalForward: start ssh: %w", err)
	}

	t := &Tunnel{
		LocalAddr:  localAddr,
		RemoteHost: remoteHost,
		RemotePort: opts.RemotePort,
		cmd:        cmd,
		cancel:     cancel,
		stderr:     stderr,
	}

	readyTimeout := opts.ReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = 5 * time.Second
	}
	deadline := time.Now().Add(readyTimeout)
	if err := tunnelReady(tunnelCtx, localAddr, deadline); err != nil {
		_ = t.Close()
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return nil, fmt.Errorf("LocalForward %s → %s:%d: %s: %w",
				localAddr, remoteHost, opts.RemotePort, stderrText, err)
		}
		return nil, fmt.Errorf("LocalForward %s → %s:%d: %w",
			localAddr, remoteHost, opts.RemotePort, err)
	}

	return t, nil
}

// pickFreeLocalPort asks the kernel for an ephemeral port and immediately
// releases it. There is a TOCTOU window between release and `ssh -L` binding,
// but for short-lived operator use this is acceptable; ssh would otherwise
// need privileged port-allocation APIs.
func pickFreeLocalPort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close() //nolint:errcheck // best-effort release of the probe socket
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("listener returned non-TCP addr %T", l.Addr())
	}
	return addr.Port, nil
}
