package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	fwcfg "frameworks/cli/internal/config"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type checkResult struct {
	Service  string `json:"service"`
	Endpoint string `json:"endpoint"`
	OK       bool   `json:"ok"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

func newContextCheckCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check reachability of services in current context",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := fwcfg.Load()
			if err != nil {
				return err
			}
			ctx := fwcfg.GetCurrent(cfg)
			results := runReachabilityChecks(ctx, timeout)
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(results)
			}
			// text output
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
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Second, "per-endpoint timeout")
	return cmd
}

func runReachabilityChecks(c fwcfg.Context, timeout time.Duration) []checkResult {
	ep := c.Endpoints
	var res []checkResult
	httpClient := &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

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
		req, _ := http.NewRequest("GET", endpoint, nil)
		resp, err := httpClient.Do(req)
		if err != nil {
			r.OK = false
			r.Status = "unreachable"
			r.Error = err.Error()
			return r
		}
		defer resp.Body.Close()
		r.OK = resp.StatusCode == 200
		r.Status = fmt.Sprintf("http %d", resp.StatusCode)
		return r
	}

	// helper to check grpc health
	checkGRPC := func(name, addr string) checkResult {
		r := checkResult{Service: name, Endpoint: addr}
		if strings.TrimSpace(addr) == "" {
			r.Status = "skip"
			return r
		}
		ctx, cancel := contextWithTimeout(timeout)
		defer cancel()
		conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			r.OK = false
			r.Status = "dial error"
			r.Error = err.Error()
			return r
		}
		defer conn.Close()
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
	res = append(res, checkHTTP("gateway", ep.GatewayURL))
	res = append(res, checkHTTP("quartermaster", ep.QuartermasterURL))
	res = append(res, checkHTTP("control", ep.ControlURL))
	res = append(res, checkHTTP("foghorn-http", ep.FoghornHTTPURL))
	res = append(res, checkHTTP("periscope-query", ep.PeriscopeQueryURL))
	res = append(res, checkHTTP("periscope-ingest", ep.PeriscopeIngestURL))
	res = append(res, checkHTTP("purser", ep.PurserURL))
	res = append(res, checkHTTP("signalman", ep.SignalmanWSURL))

	// gRPC services
	res = append(res, checkGRPC("decklog-grpc", ep.DecklogGRPCAddr))

	return res
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
