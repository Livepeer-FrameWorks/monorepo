// Package preflight wires the cluster-side state queries that drive the
// upgrade gates. It bridges releases.DataMigrationRequirement (catalog) and
// datamigrate.Requirement (preflight library) and implements the StateSource
// that fans out service status queries over SSH.
package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/exec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/datamigrate"
)

// CatalogRequirements translates every required data migration declared by
// releases up to and including targetVersion into datamigrate.Requirement.
// Empty result is honest — gates should report "no required data migrations
// declared up to vX.Y.Z" rather than passing silently.
func CatalogRequirements(catalog []releases.Release, targetVersion string) []datamigrate.Requirement {
	var out []datamigrate.Requirement
	for _, rel := range catalog {
		if releases.CompareSemver(rel.Version, targetVersion) > 0 {
			continue
		}
		for _, req := range rel.RequiredDataMigrations {
			out = append(out, datamigrate.Requirement{
				ID:                    req.ID,
				Service:               req.Service,
				IntroducedIn:          req.IntroducedIn,
				RequiredBeforePhase:   req.RequiredBeforePhase,
				RequiredBeforeVersion: req.RequiredBeforeVersion,
			})
		}
	}
	return out
}

// HostResolver returns the host running serviceName for state queries.
type HostResolver func(service string) (inventory.Host, bool)

// RuntimeResolver returns the remote binary/container slug for serviceName.
type RuntimeResolver func(service string) string

// SSHStateSource builds a datamigrate.StateSource that, for each
// (service, id), resolves the host, detects the deployment mode, then runs
// `<runtime> data-migrations status <id> --format json` over SSH.
//
// fail-closed semantics: a service with no host, missing adoption marker,
// unreachable binary, or exec failure is reported as a blocker — never a pass.
func SSHStateSource(pool *ssh.Pool, hostFor HostResolver, runtimeFor RuntimeResolver) datamigrate.StateSource {
	return func(ctx context.Context, service, id string) datamigrate.LiveStatus {
		live := datamigrate.LiveStatus{ID: id, Service: service}

		host, ok := hostFor(service)
		if !ok {
			live.FetchError = fmt.Errorf("no host found for service %q", service)
			return live
		}

		runtime := service
		if runtimeFor != nil {
			runtime = runtimeFor(service)
		}
		adopted, err := dataMigrationAdoptionMarkerPresent(ctx, pool, host, runtime)
		if err != nil {
			live.FetchError = fmt.Errorf("check data-migrations adoption for %s: %w", runtime, err)
			return live
		}
		if !adopted {
			live.NotAdopted = true
			return live
		}
		detector := detect.NewDetector(pool, host)
		state, err := detector.Detect(ctx, runtime)
		if err != nil {
			live.FetchError = fmt.Errorf("detect %s: %w", runtime, err)
			return live
		}
		mode := exec.Mode(state.Mode)
		if mode != exec.ModeDocker {
			mode = exec.ModeNative
		}

		cmd, err := exec.Command(exec.Spec{Mode: mode, ContainerName: state.Metadata["container_name"], BinaryName: runtime}, []string{"data-migrations", "status", id, "--format", "json"})
		if err != nil {
			live.FetchError = err
			return live
		}

		runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		cfg := &ssh.ConnectionConfig{
			Address:  host.ExternalIP,
			Port:     22,
			User:     host.User,
			HostName: host.Name,
			Timeout:  30 * time.Second,
		}
		result, err := pool.Run(runCtx, cfg, cmd)
		if err != nil {
			live.FetchError = fmt.Errorf("ssh run: %w", err)
			return live
		}
		if result.ExitCode == 127 || strings.Contains(result.Stderr, "command not found") {
			live.NotAdopted = true
			return live
		}
		if result.ExitCode != 0 && strings.Contains(result.Stderr, "unknown command") {
			live.NotAdopted = true
			return live
		}
		if result.ExitCode != 0 && strings.Contains(result.Stderr, "data-migrations") &&
			strings.Contains(result.Stderr, "unknown") {
			live.NotAdopted = true
			return live
		}
		if result.ExitCode != 0 {
			live.FetchError = fmt.Errorf("status exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
			return live
		}

		var payload struct {
			ID            string             `json:"id"`
			Status        datamigrate.Status `json:"status"`
			NotRegistered bool               `json:"not_registered"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			live.FetchError = fmt.Errorf("parse status output: %w; raw: %s", err, strings.TrimSpace(result.Stdout))
			return live
		}
		if payload.NotRegistered {
			live.NotRegistered = true
			return live
		}
		live.Status = payload.Status
		return live
	}
}

func dataMigrationAdoptionMarkerPresent(ctx context.Context, pool *ssh.Pool, host inventory.Host, runtime string) (bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cfg := &ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
	cmd := "test -f " + exec.ShellQuote(datamigrate.AdoptionMarkerPath(runtime))
	result, err := pool.Run(runCtx, cfg, cmd)
	if err != nil {
		return false, fmt.Errorf("ssh run: %w", err)
	}
	return result.ExitCode == 0, nil
}
