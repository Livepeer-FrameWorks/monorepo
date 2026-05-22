package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/credentials"
	"frameworks/cli/internal/ux"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	loginDefaultBridgeURL = "https://bridge.frameworks.network"
	loginClientID         = "cli"
	loginScope            = "account"
)

var (
	loginHTTPClient  = &http.Client{Timeout: 15 * time.Second}
	loginOpenBrowser = openBrowser
)

func newLoginCmd() *cobra.Command {
	var tokenFlag string
	var bridgeURLFlag string
	var noBrowser bool
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the FrameWorks platform",
		Long: `Authenticate with the FrameWorks platform using browser handoff.

The CLI starts a device-code login, opens the FrameWorks webapp, and stores
the resulting user session in the OS credential store (macOS Keychain, or an
XDG data-dir file with mode 0600 on other platforms). The tray reads the same
Keychain entry on macOS.

Use --token only for automation or recovery when you already have a
FrameWorks API token or session token.

The platform SERVICE_TOKEN is not a user credential — it is loaded from
your manifest env_files (gitops). There is no 'login --service-account'.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := credentials.DefaultStore()

			existing, err := store.Get(credentials.AccountUserSession)
			if err != nil {
				return fmt.Errorf("read credential store (%s): %w", store.Name(), err)
			}
			if existing != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Already logged in (%s is set in %s).\n", credentials.AccountUserSession, store.Name())
				fmt.Fprint(cmd.OutOrStdout(), "Replace existing credential? [y/N]: ")
				reader := bufio.NewReader(os.Stdin)
				confirm, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
					fmt.Fprintln(cmd.OutOrStdout(), "Keeping existing credential.")
					return nil
				}
			}

			if tokenFlag != "" {
				return saveLoginTokens(cmd, store, strings.TrimSpace(tokenFlag), "")
			}

			bridgeURL, err := resolveLoginBridgeURL(bridgeURLFlag)
			if err != nil {
				return err
			}
			return runDeviceLogin(cmd.Context(), cmd, store, bridgeURL, noBrowser, timeout)
		},
	}

	cmd.Flags().StringVar(&tokenFlag, "token", "", "existing FrameWorks API token or session token to store")
	cmd.Flags().StringVar(&bridgeURLFlag, "bridge-url", "", "Bridge URL to use for browser login (defaults to active context, then hosted)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the verification URL without opening a browser")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "maximum time to wait for browser approval")

	return cmd
}

func runDeviceLogin(ctx context.Context, cmd *cobra.Command, store credentials.Store, bridgeURL string, noBrowser bool, timeout time.Duration) error {
	start, err := startDeviceAuthorization(ctx, bridgeURL)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Open this URL to sign in:\n\n  %s\n\n", start.VerificationURIComplete)
	fmt.Fprintf(out, "Enter code: %s\n\n", start.UserCode)

	if !noBrowser && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := loginOpenBrowser(start.VerificationURIComplete); err != nil {
			ux.Warn(out, fmt.Sprintf("Could not open browser automatically: %v", err))
		}
	}

	pollTimeout := timeout
	if start.ExpiresIn > 0 {
		expires := time.Duration(start.ExpiresIn) * time.Second
		if pollTimeout <= 0 || expires < pollTimeout {
			pollTimeout = expires
		}
	}
	if pollTimeout <= 0 {
		pollTimeout = 5 * time.Minute
	}

	pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	tokens, err := pollDeviceAuthorization(pollCtx, bridgeURL, start)
	if err != nil {
		return err
	}
	return saveLoginTokens(cmd, store, tokens.AccessToken, tokens.RefreshToken)
}

func saveLoginTokens(cmd *cobra.Command, store credentials.Store, accessToken, refreshToken string) error {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return fmt.Errorf("no token provided")
	}
	if err := store.Set(credentials.AccountUserSession, accessToken); err != nil {
		return fmt.Errorf("save credential (%s): %w", store.Name(), err)
	}
	if strings.TrimSpace(refreshToken) != "" {
		if err := store.Set(credentials.AccountUserRefresh, strings.TrimSpace(refreshToken)); err != nil {
			return fmt.Errorf("save refresh credential (%s): %w", store.Name(), err)
		}
	} else if err := store.Delete(credentials.AccountUserRefresh); err != nil {
		return fmt.Errorf("clear stale refresh credential (%s): %w", store.Name(), err)
	}

	out := cmd.OutOrStdout()
	ux.Success(out, fmt.Sprintf("Saved %s to %s (service=%s)", credentials.AccountUserSession, store.Name(), credentials.ServiceName))

	persona := activePersona()
	ux.PrintNextSteps(out, loginNextSteps(persona))
	return nil
}

func resolveLoginBridgeURL(flagValue string) (string, error) {
	if v := strings.TrimRight(strings.TrimSpace(flagValue), "/"); v != "" {
		return v, nil
	}

	cfg, err := fwcfg.Load()
	if err != nil {
		return "", fmt.Errorf("load CLI config: %w", err)
	}
	active, err := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if err != nil {
		return "", err
	}
	if v := strings.TrimRight(strings.TrimSpace(active.Endpoints.BridgeURL), "/"); v != "" {
		return v, nil
	}
	return loginDefaultBridgeURL, nil
}

type deviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type deviceTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type deviceErrorResponse struct {
	Error     string `json:"error"`
	ErrorCode string `json:"error_code"`
}

func startDeviceAuthorization(ctx context.Context, bridgeURL string) (deviceStartResponse, error) {
	var out deviceStartResponse
	if err := postJSON(ctx, bridgeURL+"/auth/device/start", map[string]string{
		"client_id": loginClientID,
		"scope":     loginScope,
	}, &out); err != nil {
		return deviceStartResponse{}, fmt.Errorf("start device login: %w", err)
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURIComplete == "" {
		return deviceStartResponse{}, fmt.Errorf("start device login: incomplete response")
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return out, nil
}

func pollDeviceAuthorization(ctx context.Context, bridgeURL string, start deviceStartResponse) (deviceTokenResponse, error) {
	interval := time.Duration(start.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}

	firstPoll := true
	for {
		if firstPoll {
			firstPoll = false
		} else {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return deviceTokenResponse{}, fmt.Errorf("device login timed out")
			case <-timer.C:
			}
		}

		var out deviceTokenResponse
		statusCode, errBody, err := postJSONWithStatus(ctx, bridgeURL+"/auth/device/poll", map[string]string{
			"device_code": start.DeviceCode,
			"client_id":   loginClientID,
		}, &out)
		if err != nil {
			return deviceTokenResponse{}, fmt.Errorf("poll device login: %w", err)
		}
		if statusCode >= 200 && statusCode < 300 {
			if strings.TrimSpace(out.AccessToken) == "" {
				return deviceTokenResponse{}, fmt.Errorf("poll device login: missing access token")
			}
			return out, nil
		}

		var pollErr deviceErrorResponse
		_ = json.Unmarshal(errBody, &pollErr)
		switch pollErr.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return deviceTokenResponse{}, fmt.Errorf("device login denied")
		case "expired_token":
			return deviceTokenResponse{}, fmt.Errorf("device login expired")
		default:
			msg := strings.TrimSpace(pollErr.Error)
			if msg == "" {
				msg = strings.TrimSpace(string(errBody))
			}
			if msg == "" {
				msg = http.StatusText(statusCode)
			}
			return deviceTokenResponse{}, fmt.Errorf("device login failed: HTTP %d: %s", statusCode, msg)
		}
	}
}

func postJSON(ctx context.Context, url string, body any, out any) error {
	statusCode, raw, err := postJSONWithStatus(ctx, url, body, out)
	if err != nil {
		return err
	}
	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", statusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func postJSONWithStatus(ctx context.Context, url string, body any, out any) (int, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "FrameWorks-CLI/1.0")

	resp, err := loginHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, raw, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, raw, nil
}

func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
		args = []string{url}
	case "windows":
		name = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		name = "xdg-open"
		args = []string{url}
	}
	return exec.Command(name, args...).Start()
}

// activePersona reports the persona of the active context, or "" if no
// context is configured.
func activePersona() fwcfg.Persona {
	cfg, err := fwcfg.Load()
	if err != nil {
		return ""
	}
	active, mErr := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if mErr != nil {
		return ""
	}
	return active.Persona
}

func loginNextSteps(persona fwcfg.Persona) []ux.NextStep {
	switch persona {
	case fwcfg.PersonaUser, fwcfg.PersonaEdge:
		return []ux.NextStep{
			{Cmd: "frameworks menu", Why: "Open account, insights, and hosted workflows."},
		}
	case fwcfg.PersonaSelfHosted:
		return []ux.NextStep{
			{Cmd: "frameworks edge deploy --ssh <user>@<host>", Why: "Deploy or update your self-hosted edge node."},
		}
	case fwcfg.PersonaPlatform:
		return []ux.NextStep{
			{Cmd: "frameworks cluster provision", Why: "Provision infra, services, init, static seeds, and bootstrap state."},
		}
	default:
		return []ux.NextStep{
			{Cmd: "frameworks setup", Why: "No active context — run setup to pick a persona first."},
		}
	}
}
