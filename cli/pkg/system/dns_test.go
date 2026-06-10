package system

import (
	"strings"
	"testing"
)

// GenerateSystemdResolvedConfig renders the drop-in that points the host stub
// resolver at the local Privateer DNS on the given port while keeping the
// ~internal routing domain. The port must be interpolated, not hard-coded.
func TestGenerateSystemdResolvedConfig(t *testing.T) {
	out, err := GenerateSystemdResolvedConfig(5353)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"[Resolve]",
		"DNS=127.0.0.1:5353",
		"Domains=~internal",
		"DNSStubListener=yes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config missing %q\n---\n%s", want, out)
		}
	}
}

func TestGenerateSystemdResolvedConfigInterpolatesPort(t *testing.T) {
	out, err := GenerateSystemdResolvedConfig(53)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "DNS=127.0.0.1:53") {
		t.Fatalf("port not interpolated: %s", out)
	}
	if strings.Contains(out, "{{") {
		t.Fatalf("template left unrendered: %s", out)
	}
}

// ConfigureSystemdResolved must embed the supplied config content inside the
// heredoc it writes to the drop-in path, so the generated script is the
// transport for GenerateSystemdResolvedConfig output.
func TestConfigureSystemdResolvedEmbedsConfig(t *testing.T) {
	cfg := "[Resolve]\nDNS=127.0.0.1:53\n"
	script := ConfigureSystemdResolved(cfg)
	if !strings.Contains(script, cfg) {
		t.Errorf("script does not embed config content\n---\n%s", script)
	}
	if !strings.Contains(script, "/etc/systemd/resolved.conf.d/frameworks-privateer.conf") {
		t.Errorf("script missing drop-in path")
	}
	if !strings.Contains(script, "systemctl restart systemd-resolved") {
		t.Errorf("script missing restart")
	}
}

// These return fixed shell snippets; assert the load-bearing fragments so an
// accidental edit to the command is caught.
func TestDNSScriptConstants(t *testing.T) {
	if !strings.Contains(DetectSystemdResolved(), "systemctl is-active systemd-resolved") {
		t.Errorf("DetectSystemdResolved: %q", DetectSystemdResolved())
	}
	// Upstream capture must exclude loopback nameservers.
	cap := CaptureUpstreamNameservers()
	if !strings.Contains(cap, "nameserver") || !strings.Contains(cap, "127") {
		t.Errorf("CaptureUpstreamNameservers: %q", cap)
	}
	if !strings.Contains(ConfigureResolvConf(), "nameserver 127.0.0.1") {
		t.Errorf("ConfigureResolvConf: %q", ConfigureResolvConf())
	}
}
