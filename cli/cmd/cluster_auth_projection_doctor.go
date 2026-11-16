package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

func doctorAuthProjection(ctx context.Context, pool *fwssh.Pool, host inventory.Host, svc inventory.ServiceConfig, deploy string, port int) *health.CheckResult {
	result := &health.CheckResult{Name: "auth_session_projection", CheckedAt: time.Now(), Metadata: map[string]string{"access": "ssh-local-metrics"}}
	if pool == nil {
		result.Status = "unknown"
		result.Error = "auth projection could not be verified: ssh pool is nil"
		return result
	}
	runner, err := pool.Get(&fwssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 30 * time.Second,
	})
	if err != nil {
		result.Status = "unknown"
		result.Error = fmt.Sprintf("auth projection ssh: %v", err)
		return result
	}
	address := doctorServiceProbeAddress(svc)
	if deploy == "foghorn" {
		address = "127.0.0.1"
		port = servicedefs.FoghornInternalHTTPPort
	}
	return doctorAuthProjectionWithRunner(ctx, runner, address, port, result)
}

func doctorAuthProjectionWithRunner(ctx context.Context, runner fwssh.Runner, address string, port int, result *health.CheckResult) *health.CheckResult {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := runner.Run(probeCtx, doctorHTTPProbeCommandAt(address, port, "/metrics"))
	if err != nil {
		result.Status = "unknown"
		result.Error = fmt.Sprintf("auth projection probe: %v", err)
		return result
	}
	body, code, err := splitCurlStatus(response.Stdout)
	if err != nil || response.ExitCode != 0 || code != 200 {
		result.Status = "unknown"
		result.Error = "auth projection metrics could not be read"
		return result
	}
	if err := validateAuthProjectionMetrics(body); err != nil {
		result.Status = "unhealthy"
		result.Error = err.Error()
		return result
	}
	result.OK = true
	result.Status = "healthy"
	result.Message = "replica revocation event source connected and complete authority projection fresh"
	return result
}

func validateAuthProjectionMetrics(body string) error {
	required := map[string]bool{"auth_session_revocation_feed_connected": false, "auth_session_revocation_snapshot_synchronized": false, "auth_session_revocation_projection_ready": false}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, ok := required[fields[0]]; !ok {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || value != 1 {
			return fmt.Errorf("%s is not healthy; inspect this replica's Kafka and authority snapshot delivery", fields[0])
		}
		required[fields[0]] = true
	}
	for _, name := range []string{"auth_session_revocation_feed_connected", "auth_session_revocation_snapshot_synchronized", "auth_session_revocation_projection_ready"} {
		if !required[name] {
			return fmt.Errorf("auth projection unverified: metric %s is missing", name)
		}
	}
	return nil
}
