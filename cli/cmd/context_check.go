package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/internal/platformauth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type checkResult struct {
	Service  string `json:"service"`
	Endpoint string `json:"endpoint"`
	OK       bool   `json:"ok"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// personaResult is a single assertion in the persona/auth preflight section.
// Distinct from checkResult so reachability and persona output stay separable
// in JSON consumers.
type personaResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// errPersonaPreflight signals that one or more persona/auth assertions failed.
// RunE returns this so cobra produces a nonzero exit code suitable for CI gating.
// Reachability failures don't propagate to exit (existing UX); a CI script
// that needs strict reachability gating should consume the JSON output.
var errPersonaPreflight = errors.New("persona/auth preflight failed")

func newContextCheckCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check reachability and persona/auth invariants for the current context",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := fwcfg.Load()
			if err != nil {
				return err
			}
			rt := fwcfg.GetRuntimeOverrides()
			ctx, err := fwcfg.ResolveActiveContext(rt, fwcfg.OSEnv{}, cfg)
			if err != nil {
				return err
			}
			results := runReachabilityChecks(cmd.Context(), ctx, timeout)
			personas := runPersonaChecks(cmd.Context(), ctx, cfg, fwcfg.OSEnv{}, fwcredentials.DefaultStore())

			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(struct {
					Reachability []checkResult   `json:"reachability"`
					Persona      []personaResult `json:"persona"`
				}{Reachability: results, Persona: personas}); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Service Reachability:")
				for _, r := range results {
					mark := "✗"
					if r.OK {
						mark = "✓"
					}
					if r.Error != "" {
						fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %-40s %s (%s)\n", mark, r.Service+":", r.Endpoint, r.Status, r.Error)
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %-40s %s\n", mark, r.Service+":", r.Endpoint, r.Status)
					}
				}
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), "Persona / Auth:")
				for _, p := range personas {
					mark := "✗"
					if p.OK {
						mark = "✓"
					}
					detail := p.Detail
					if p.Error != "" {
						if detail != "" {
							detail = detail + " (" + p.Error + ")"
						} else {
							detail = p.Error
						}
					}
					fmt.Fprintf(cmd.OutOrStdout(), " %s %-20s %s\n", mark, p.Name+":", detail)
				}
			}

			for _, p := range personas {
				if !p.OK {
					return errPersonaPreflight
				}
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Second, "per-endpoint timeout")
	return cmd
}

// runPersonaChecks asserts that the active context has the auth material its
// persona requires. Field presence and resolvability only — no transport
// security inferred from port (SSH/mesh access modes legitimately resolve to
// plaintext targets and would false-positive any TLS-by-port rule).
func runPersonaChecks(ctx context.Context, c fwcfg.Context, cfg fwcfg.Config, env fwcfg.Env, store fwcredentials.Store) []personaResult {
	switch {
	case c.Persona == fwcfg.PersonaPlatform:
		return runPlatformPersonaChecks(ctx, c, cfg)
	case c.Persona == fwcfg.PersonaSelfHosted:
		return runJWTPersonaCheck("owner_jwt", env, store)
	case c.Persona.IsUser():
		return runJWTPersonaCheck("user_jwt", env, store)
	case c.Persona == "":
		return []personaResult{{Name: "persona", OK: false, Error: "no persona configured for this context (run 'frameworks setup')"}}
	default:
		return []personaResult{{Name: "persona", OK: false, Error: fmt.Sprintf("unknown persona %q", c.Persona)}}
	}
}

func runPlatformPersonaChecks(ctx context.Context, c fwcfg.Context, cfg fwcfg.Config) []personaResult {
	out := make([]personaResult, 0, 2)
	if strings.TrimSpace(c.SystemTenantID) == "" {
		out = append(out, personaResult{Name: "system_tenant_id", OK: false, Error: "platform context missing system_tenant_id (run 'frameworks setup' or 'cluster bootstrap')"})
	} else {
		out = append(out, personaResult{Name: "system_tenant_id", OK: true, Detail: c.SystemTenantID})
	}
	if _, err := platformauth.ResolveManifestServiceToken(ctx, c, cfg); err != nil {
		out = append(out, personaResult{Name: "service_token", OK: false, Error: err.Error()})
	} else {
		out = append(out, personaResult{Name: "service_token", OK: true, Detail: "resolved from manifest"})
	}
	return out
}

func runJWTPersonaCheck(name string, env fwcfg.Env, store fwcredentials.Store) []personaResult {
	jwt, err := fwcredentials.ResolveUserAuth(env, store)
	if err != nil {
		return []personaResult{{Name: name, OK: false, Error: err.Error()}}
	}
	if strings.TrimSpace(jwt) == "" {
		return []personaResult{{Name: name, OK: false, Error: "no JWT found (run 'frameworks login')"}}
	}
	return []personaResult{{Name: name, OK: true, Detail: "present"}}
}

func runReachabilityChecks(parent context.Context, c fwcfg.Context, timeout time.Duration) []checkResult {
	ep := c.Endpoints
	var res []checkResult
	httpClient := &http.Client{Timeout: timeout}

	// helper to check http health
	checkHTTP := func(name, base string) checkResult {
		if base == "" {
			return checkResult{Service: name, Endpoint: "(unset)", OK: false, Status: "skip"}
		}
		u, _ := url.Parse(base)
		// Derive HTTP URL if ws/wss
		if u.Scheme == "ws" {
			u.Scheme = "http"
		}
		if u.Scheme == "wss" {
			u.Scheme = "https"
		}
		// Append /health if no path specified
		if u.Path == "" || u.Path == "/" {
			u.Path = "/health"
		}
		endpoint := u.String()
		r := checkResult{Service: name, Endpoint: endpoint}
		ctx, cancel := contextWithTimeout(parent, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			r.OK = false
			r.Status = "invalid request"
			r.Error = err.Error()
			return r
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			r.OK = false
			r.Status = "unreachable"
			r.Error = err.Error()
			return r
		}
		defer func() {
			_ = resp.Body.Close()
		}()
		r.OK = resp.StatusCode == 200
		r.Status = fmt.Sprintf("http %d", resp.StatusCode)
		return r
	}

	// helper to check grpc health
	checkGRPC := func(name string, target controlplane.Endpoint) checkResult {
		r := checkResult{Service: name, Endpoint: target.Address}
		if strings.TrimSpace(target.Address) == "" {
			r.Status = "skip"
			return r
		}
		ctx, cancel := contextWithTimeout(parent, timeout)
		defer cancel()
		transport, err := grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
			ServerName:    target.ServerName,
			AllowInsecure: target.AllowInsecure,
		}, nil)
		if err != nil {
			r.OK = false
			r.Status = "tls config error"
			r.Error = err.Error()
			return r
		}
		conn, err := grpc.NewClient(target.Address, transport)
		if err != nil {
			r.OK = false
			r.Status = "dial error"
			r.Error = err.Error()
			return r
		}
		defer func() {
			_ = conn.Close()
		}()
		hc := healthpb.NewHealthClient(conn)
		if _, err := hc.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
			r.OK = false
			r.Status = "unhealthy"
			r.Error = err.Error()
			return r
		}
		r.OK = true
		r.Status = "grpc ok"
		return r
	}

	// HTTP services
	res = append(res, checkHTTP("bridge", ep.BridgeURL))
	res = append(res, checkHTTP("signalman", ep.SignalmanWSURL))

	resolver := controlplane.NewResolver(c)
	defer resolver.Close()

	for _, svc := range []struct {
		label string
		id    string
	}{
		{"commodore-grpc", "commodore"},
		{"quartermaster-grpc", "quartermaster"},
		{"purser-grpc", "purser"},
		{"periscope-grpc", "periscope"},
		{"signalman-grpc", "signalman"},
		{"foghorn-grpc", "foghorn"},
		{"decklog-grpc", "decklog"},
		{"navigator-grpc", "navigator"},
	} {
		resolveCtx, cancel := contextWithTimeout(parent, timeout)
		target, err := resolver.ResolveGRPC(resolveCtx, svc.id)
		cancel()
		if err != nil {
			res = append(res, checkResult{
				Service:  svc.label,
				Endpoint: "(resolve failed)",
				OK:       false,
				Status:   "resolve error",
				Error:    err.Error(),
			})
			continue
		}
		r := checkGRPC(svc.label, target)
		res = append(res, r)
	}

	return res
}

func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, d)
}
