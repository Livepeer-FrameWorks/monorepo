package updater

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// s6Controller drives the peer services inside the single-image edge
// container. Helmsman runs unprivileged there, so it cannot use the
// root-owned s6-svc control FIFOs: Caddy is restarted by asking it to exit
// via its own admin API (s6-supervise relaunches it), and MistController is
// signaled directly (same uid).
type s6Controller struct{}

var (
	caddyRestartGrace   = time.Second
	caddyRestartTimeout = 30 * time.Second
	caddyReadyPollEvery = 500 * time.Millisecond
)

func (s6Controller) SignalMistUSR1(ctx context.Context) error {
	signaled, errs := signalMistControllerProcesses()
	if signaled {
		return nil
	}
	if err := runCommand(ctx, "pkill", "-USR1", "-f", "MistController"); err == nil {
		return nil
	} else {
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return fmt.Errorf("no MistController process found")
	}
	return errors.Join(errs...)
}

func (s6Controller) RestartCaddy(ctx context.Context) error {
	client, baseURL := caddyAdminHTTPClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/stop", nil)
	if err != nil {
		return err
	}
	// A transport error here means Caddy is already down and s6-supervise is
	// relaunching it, which is the state /stop was driving at anyway — only
	// the readiness poll below decides success.
	if resp, stopErr := client.Do(req); stopErr == nil {
		_ = resp.Body.Close()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(caddyRestartGrace):
	}
	return waitCaddyAdminReady(ctx, client, baseURL)
}

func waitCaddyAdminReady(ctx context.Context, client *http.Client, baseURL string) error {
	deadline := time.Now().Add(caddyRestartTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/config/", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(caddyReadyPollEvery):
		}
	}
	return fmt.Errorf("caddy admin endpoint did not come back after restart: %w", lastErr)
}

// caddyAdminHTTPClient mirrors the admin endpoint resolution used by the
// config manager: CADDY_ADMIN_SOCKET (unix socket) wins over CADDY_ADMIN_URL,
// with the Caddy default TCP admin address as fallback.
func caddyAdminHTTPClient() (*http.Client, string) {
	if socketPath := os.Getenv("CADDY_ADMIN_SOCKET"); socketPath != "" {
		client := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
				DisableKeepAlives: true,
			},
		}
		return client, "http://caddy"
	}
	baseURL := "http://localhost:2019"
	if adminURL := os.Getenv("CADDY_ADMIN_URL"); adminURL != "" {
		baseURL = strings.TrimRight(adminURL, "/")
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}, baseURL
}
