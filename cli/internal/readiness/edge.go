package readiness

import (
	"fmt"
	"strings"
)

// EdgeCheck is the outcome of a single check the edge doctor has already run.
type EdgeCheck struct {
	Name   string
	OK     bool
	Detail string
}

// EdgeInputs is the observed state an edge doctor / status command has
// collected; EdgeReadiness turns it into adaptive remediation.
// ServiceProbeErr is non-empty when the probe itself failed (distinct from
// a service being down): the operator needs to fix the probe, not the
// services.
type EdgeInputs struct {
	HasEnv          bool
	Domain          string
	Mode            string
	HostChecks      []EdgeCheck
	ServiceChecks   []EdgeCheck
	StreamChecks    []EdgeCheck
	ServiceProbeErr string
	HTTPSStatus     int
	HTTPSError      string
}

// EdgeReadiness produces a Report whose warnings carry remediations tied
// to the specific failures in the inputs.
func EdgeReadiness(in EdgeInputs) Report {
	r := Report{Checked: true}

	if !in.HasEnv {
		r.Warnings = append(r.Warnings, Warning{
			Subject: "edge.not-enrolled",
			Detail:  ".edge.env missing — this host has not been enrolled.",
			Remediation: Remediation{
				Cmd: "frameworks edge deploy --ssh <host> --email <you>",
				Why: "Enroll this host to create .edge.env and start the stack.",
			},
		})
		return r
	}

	if in.ServiceProbeErr != "" {
		why := "confirm the docker daemon is running and docker-compose.edge.yml is present"
		if in.Mode == "native" {
			why = "confirm frameworks-caddy/helmsman/mistserver units exist and the current user can query them"
		}
		r.Warnings = append(r.Warnings, Warning{
			Subject:     "edge.service-probe",
			Detail:      "could not probe service state: " + in.ServiceProbeErr,
			Remediation: Remediation{Why: why},
		})
	}

	if in.HTTPSError != "" {
		r.Warnings = append(r.Warnings, Warning{
			Subject: "edge.https",
			Detail:  fmt.Sprintf("HTTPS health check failed: %s", in.HTTPSError),
			Remediation: Remediation{
				Cmd: "frameworks edge logs caddy",
				Why: "Check Caddy logs; ensure ports 80/443 are reachable and DNS A/AAAA records point at this host.",
			},
		})
	} else if in.HTTPSStatus != 0 && in.HTTPSStatus != 200 {
		r.Warnings = append(r.Warnings, Warning{
			Subject: "edge.https",
			Detail:  fmt.Sprintf("HTTPS health check returned HTTP %d.", in.HTTPSStatus),
			Remediation: Remediation{
				Cmd: "frameworks edge logs helmsman",
				Why: "Helmsman /health replied with a non-200 status.",
			},
		})
	}

	for _, c := range in.ServiceChecks {
		if c.OK {
			continue
		}
		r.Warnings = append(r.Warnings, Warning{
			Subject: fmt.Sprintf("edge.service.%s", c.Name),
			Detail:  c.Detail,
			Remediation: Remediation{
				Cmd: fmt.Sprintf("frameworks edge logs %s", c.Name),
				Why: fmt.Sprintf("Service %s is not running; inspect its logs.", c.Name),
			},
		})
	}

	for _, c := range in.HostChecks {
		if c.OK {
			continue
		}
		r.Warnings = append(r.Warnings, Warning{
			Subject:     fmt.Sprintf("edge.host.%s", c.Name),
			Detail:      c.Detail,
			Remediation: hostRemediation(c.Name),
		})
	}

	for _, c := range in.StreamChecks {
		if c.OK {
			continue
		}
		why := "Run a deep stream analysis to pinpoint which segment or manifest is broken."
		if in.Domain != "" {
			why = fmt.Sprintf("Run a deep stream analysis against https://%s to pinpoint which segment or manifest is broken.", in.Domain)
		}
		r.Warnings = append(r.Warnings, Warning{
			Subject: fmt.Sprintf("edge.stream.%s", c.Name),
			Detail:  c.Detail,
			Remediation: Remediation{
				Cmd: fmt.Sprintf("frameworks edge diagnose media --stream %s", c.Name),
				Why: why,
			},
		})
	}

	return r
}

func hostRemediation(name string) Remediation {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "ulimit"), strings.Contains(n, "sysctl"), strings.Contains(n, "shm"):
		return Remediation{
			Cmd: "frameworks edge tune --write",
			Why: "Apply the recommended kernel limits and sysctls (reboot may be required).",
		}
	case strings.Contains(n, "docker"):
		return Remediation{Why: "Install Docker or Docker Desktop on this host."}
	case strings.Contains(n, "dns"):
		return Remediation{Why: "Point the A/AAAA record for this edge domain at the host's public IP."}
	case strings.Contains(n, "port"):
		return Remediation{Why: "Free up the port or stop the process currently listening."}
	case strings.Contains(n, "disk"):
		return Remediation{Why: "Free disk space on the affected mount."}
	}
	return Remediation{}
}
