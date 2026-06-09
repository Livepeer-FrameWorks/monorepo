package cmd

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	"github.com/spf13/cobra"
)

// --- admin validators -------------------------------------------------------

func TestValidateUUID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		wantErr bool
	}{
		{in: "123e4567-e89b-12d3-a456-426614174000", wantErr: false},
		{in: "123E4567-E89B-12D3-A456-426614174000", wantErr: false},
		{in: "123e4567e89b12d3a456426614174000", wantErr: false}, // hyphenless is allowed by design
		{in: "not-a-uuid", wantErr: true},
		{in: "", wantErr: true},
		{in: "123e4567-e89b-12d3-a456-42661417400g", wantErr: true}, // non-hex char
	}
	for _, tc := range cases {
		if err := validateUUID(tc.in); (err != nil) != tc.wantErr {
			t.Fatalf("validateUUID(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}

func TestValidateTokenName(t *testing.T) {
	t.Parallel()
	if err := validateTokenName("  "); err == nil {
		t.Fatal("expected error for blank name")
	}
	if err := validateTokenName(strings.Repeat("x", 257)); err == nil {
		t.Fatal("expected error for over-long name")
	}
	if err := validateTokenName("ci-token"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBootstrapTokenKind(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"edge_node", "service", "infrastructure_node"} {
		if err := validateBootstrapTokenKind(ok); err != nil {
			t.Fatalf("kind %q should be valid: %v", ok, err)
		}
	}
	if err := validateBootstrapTokenKind("admin"); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestParseStructJSON(t *testing.T) {
	t.Parallel()
	blank, err := parseStructJSON("   ")
	if err != nil || blank != nil {
		t.Fatalf("blank input should be (nil,nil), got (%v,%v)", blank, err)
	}
	if _, errBad := parseStructJSON("{not json"); errBad == nil {
		t.Fatal("expected error for invalid JSON")
	}
	valid, err := parseStructJSON(`{"a":1,"b":"x"}`)
	if err != nil || valid == nil {
		t.Fatalf("valid JSON should parse: s=%v err=%v", valid, err)
	}
}

// --- optional flag helpers --------------------------------------------------

func TestOptionalFlags(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("s", "def", "")
	cmd.Flags().Int("i", 7, "")
	cmd.Flags().Bool("b", false, "")

	// Not changed → all nil.
	if p := optionalStringFlag(cmd, "s", "def"); p != nil {
		t.Fatalf("unchanged string flag should be nil, got %v", *p)
	}
	if p := optionalInt32Flag(cmd, "i", 7); p != nil {
		t.Fatalf("unchanged int flag should be nil, got %v", *p)
	}
	if p := optionalBoolFlag(cmd, "b", false); p != nil {
		t.Fatalf("unchanged bool flag should be nil, got %v", *p)
	}

	// Changed → returns pointer to the supplied value.
	_ = cmd.Flags().Set("s", "v")
	_ = cmd.Flags().Set("i", "9")
	_ = cmd.Flags().Set("b", "true")
	if p := optionalStringFlag(cmd, "s", "v"); p == nil || *p != "v" {
		t.Fatalf("changed string flag = %v", p)
	}
	if p := optionalInt32Flag(cmd, "i", 9); p == nil || *p != 9 {
		t.Fatalf("changed int flag = %v", p)
	}
	if p := optionalBoolFlag(cmd, "b", true); p == nil || *p != true {
		t.Fatalf("changed bool flag = %v", p)
	}
}

func TestBoolFlag(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().Bool("yes", false, "")
	if boolFlag(cmd, "missing") {
		t.Fatal("missing flag should be false")
	}
	if boolFlag(cmd, "yes") {
		t.Fatal("default false flag should be false")
	}
	_ = cmd.Flags().Set("yes", "true")
	if !boolFlag(cmd, "yes") {
		t.Fatal("set flag should be true")
	}
}

func TestJSONEncode(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{Use: "x"}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := jsonEncode(cmd, map[string]int{"a": 1}); err != nil {
		t.Fatalf("jsonEncode: %v", err)
	}
	if !strings.Contains(buf.String(), `"a": 1`) {
		t.Fatalf("expected indented JSON, got %q", buf.String())
	}
}

// --- small string transforms ----------------------------------------------

func TestMajorVersion(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"v1.2.3": "1",
		"2.0.0":  "2",
		"v10.4":  "10",
		"v3":     "3",
		"":       "",
	}
	for in, want := range cases {
		if got := majorVersion(in); got != want {
			t.Fatalf("majorVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsValidChannel(t *testing.T) {
	t.Parallel()
	if !isValidChannel("stable") || !isValidChannel("rc") {
		t.Fatal("stable and rc must be valid")
	}
	if isValidChannel("nightly") || isValidChannel("") {
		t.Fatal("unknown channels must be invalid")
	}
}

func TestParseServiceID(t *testing.T) {
	t.Parallel()
	svc, id, err := parseServiceID("commodore.abc-123")
	if err != nil || svc != "commodore" || id != "abc-123" {
		t.Fatalf("got (%q,%q,%v)", svc, id, err)
	}
	// Splits on the FIRST dot.
	svc, id, _ = parseServiceID("a.b.c")
	if svc != "a" || id != "b.c" {
		t.Fatalf("first-dot split failed: (%q,%q)", svc, id)
	}
	for _, bad := range []string{"nodot", ".id", "svc.", ""} {
		if _, _, err := parseServiceID(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestTerminalNodeStatus(t *testing.T) {
	t.Parallel()
	if terminalNodeStatus("evict") != "evicted" {
		t.Fatal("evict → evicted")
	}
	if terminalNodeStatus("retire") != "retired" || terminalNodeStatus("anything") != "retired" {
		t.Fatal("non-evict → retired")
	}
}

func TestCommandVerbTitle(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"evict": "Evict", "retire": "Retire", "": "", "x": "X"}
	for in, want := range cases {
		if got := commandVerbTitle(in); got != want {
			t.Fatalf("commandVerbTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- audit / status labels --------------------------------------------------

func TestRevisionLabel(t *testing.T) {
	t.Parallel()
	if revisionLabel("") != "-" {
		t.Fatal("empty → -")
	}
	if revisionLabel("abc123") != "abc123" {
		t.Fatal("short rev unchanged")
	}
	long := "0123456789abcdef"
	if revisionLabel(long) != "0123456789ab" {
		t.Fatalf("long rev should truncate to 12, got %q", revisionLabel(long))
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	t.Parallel()
	if severityLabel(auditOK) != "ok" || severityLabel(auditInfo) != "info" ||
		severityLabel(auditWarn) != "warn" || severityLabel(auditError) != "ERROR" {
		t.Fatal("severityLabel mapping wrong")
	}
	if statusKeyMatch(auditOK) != "match" || statusKeyMatch(auditError) != "MISMATCH" {
		t.Fatal("statusKeyMatch mapping wrong")
	}
	if livenessLabel(livenessFresh) != "live" || livenessLabel(livenessStale) != "stale" ||
		livenessLabel(livenessUnknown) != "-" {
		t.Fatal("livenessLabel mapping wrong")
	}
}

func TestDashIfEmpty(t *testing.T) {
	t.Parallel()
	if dashIfEmpty("") != "-" || dashIfEmpty("x") != "x" {
		t.Fatal("dashIfEmpty wrong")
	}
}

// --- finalize step membership ----------------------------------------------

func TestFinalizeStepsContain(t *testing.T) {
	t.Parallel()
	steps := []clusterFinalizeStep{clusterFinalizeStepAssignments}
	if finalizeStepsContain(steps, clusterFinalizeStepQuartermaster) {
		t.Fatal("should not contain quartermaster")
	}
	if !finalizeStepsContain(steps, clusterFinalizeStepAssignments) {
		t.Fatal("should contain assignments")
	}
	if finalizeStepsContainBootstrap(steps) {
		t.Fatal("assignments-only is not a bootstrap set")
	}
	if !finalizeStepsContainBootstrap([]clusterFinalizeStep{clusterFinalizeStepCommodore}) {
		t.Fatal("commodore is a bootstrap step")
	}
}

// --- geoip resolution -------------------------------------------------------

func TestEffectiveGeoIPSource(t *testing.T) {
	t.Parallel()
	if effectiveGeoIPSource(nil, "file") != "file" {
		t.Fatal("explicit wins")
	}
	m := &inventory.Manifest{GeoIP: &inventory.GeoIPConfig{Source: "custom"}}
	if effectiveGeoIPSource(m, "") != "custom" {
		t.Fatal("manifest source used when no explicit")
	}
	if effectiveGeoIPSource(nil, "") != "maxmind" {
		t.Fatal("default is maxmind")
	}
}

func TestEffectiveGeoIPFilePath(t *testing.T) {
	t.Parallel()
	m := &inventory.Manifest{GeoIP: &inventory.GeoIPConfig{File: "geo.mmdb"}}
	if got := effectiveGeoIPFilePath(m, "", "/cfg"); got != "/cfg/geo.mmdb" {
		t.Fatalf("relative manifest path should join manifestDir, got %q", got)
	}
	if got := effectiveGeoIPFilePath(m, "/abs/x.mmdb", "/cfg"); got != "/abs/x.mmdb" {
		t.Fatalf("explicit absolute path should pass through, got %q", got)
	}
	if got := effectiveGeoIPFilePath(&inventory.Manifest{GeoIP: &inventory.GeoIPConfig{File: "/already/abs"}}, "", "/cfg"); got != "/already/abs" {
		t.Fatalf("absolute manifest path should not be joined, got %q", got)
	}
}

// --- postgres doctor user ---------------------------------------------------

func TestPostgresDoctorUser(t *testing.T) {
	t.Parallel()
	if postgresDoctorUser(nil) != "postgres" {
		t.Fatal("nil → postgres")
	}
	if postgresDoctorUser(&inventory.PostgresConfig{Engine: "yugabyte"}) != "yugabyte" {
		t.Fatal("yugabyte engine → yugabyte")
	}
	if postgresDoctorUser(&inventory.PostgresConfig{Engine: "postgres"}) != "postgres" {
		t.Fatal("postgres engine → postgres")
	}
}

// --- desired edge component versions ---------------------------------------

func TestDesiredEdgeComponentVersionsFromJSON(t *testing.T) {
	t.Parallel()
	raw := `{"helmsman":{"version":" v1.0.0 "},"mistserver":{"version":"v2"},"config_schema":{"version":"ignored"}}`
	got, err := desiredEdgeComponentVersionsFromJSON(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["helmsman"] != "v1.0.0" {
		t.Fatalf("version should be trimmed, got %q", got["helmsman"])
	}
	if _, ok := got["config_schema"]; ok {
		t.Fatal("config_schema must be skipped")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 components, got %d", len(got))
	}
	if _, err := desiredEdgeComponentVersionsFromJSON("{bad"); err == nil {
		t.Fatal("expected error on bad JSON")
	}
}

// --- log target helpers -----------------------------------------------------

func TestBuildLogCommand(t *testing.T) {
	t.Parallel()
	got, err := buildLogCommand("commodore", "docker", true, 100)
	if err != nil {
		t.Fatalf("docker: %v", err)
	}
	for _, want := range []string{"docker compose logs", "--tail=100", "--follow", "/opt/frameworks/commodore"} {
		if !strings.Contains(got, want) {
			t.Fatalf("docker cmd %q missing %q", got, want)
		}
	}
	got, err = buildLogCommand("commodore", "native", false, 0)
	if err != nil {
		t.Fatalf("native: %v", err)
	}
	if !strings.Contains(got, "journalctl -u frameworks-commodore") || strings.Contains(got, "-n ") {
		t.Fatalf("native cmd wrong: %q", got)
	}
	if _, err := buildLogCommand("x", "bogus", false, 0); err == nil {
		t.Fatal("unknown mode should error")
	}
}

func TestServiceHostNames(t *testing.T) {
	t.Parallel()
	// Hosts slice wins and is deduped.
	got := serviceHostNames(inventory.ServiceConfig{Hosts: []string{"a", "a", "b"}})
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Hosts dedupe failed: %v", got)
	}
	// Falls back to single Host.
	if got := serviceHostNames(inventory.ServiceConfig{Host: "solo"}); !slices.Equal(got, []string{"solo"}) {
		t.Fatalf("Host fallback failed: %v", got)
	}
	if got := serviceHostNames(inventory.ServiceConfig{}); got != nil {
		t.Fatalf("empty config should be nil, got %v", got)
	}
}

func TestKafkaLogHosts(t *testing.T) {
	t.Parallel()
	cfg := &inventory.KafkaConfig{
		Controllers: []inventory.KafkaController{{Host: "ctrl1"}},
		Brokers:     []inventory.KafkaBroker{{Host: "brk1"}},
	}
	got := kafkaLogHosts(cfg)
	if !slices.Contains(got, "ctrl1") || !slices.Contains(got, "brk1") {
		t.Fatalf("expected controller+broker hosts, got %v", got)
	}
}

func TestDedupeStrings(t *testing.T) {
	t.Parallel()
	if got := dedupeStrings(nil); got != nil {
		t.Fatalf("nil in → nil out, got %v", got)
	}
	got := dedupeStrings([]string{" a ", "a", "", "b"})
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("dedupeStrings trims/dedupes/drops-empty, got %v", got)
	}
}

func TestSplitSSHTarget(t *testing.T) {
	t.Parallel()
	u, h, err := splitSSHTarget("deploy@host1")
	if err != nil || u != "deploy" || h != "host1" {
		t.Fatalf("got (%q,%q,%v)", u, h, err)
	}
	u, h, err = splitSSHTarget("host2")
	if err != nil || u != "root" || h != "host2" {
		t.Fatalf("default user should be root, got (%q,%q,%v)", u, h, err)
	}
	for _, bad := range []string{"", "  ", "@host", "user@"} {
		if _, _, err := splitSSHTarget(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestSafeSnapshotFilename(t *testing.T) {
	t.Parallel()
	if safeSnapshotFilename("") != "host" {
		t.Fatal("empty → host")
	}
	if got := safeSnapshotFilename("edge-1.example.com"); got != "edge-1.example.com" {
		t.Fatalf("safe chars preserved, got %q", got)
	}
	if got := safeSnapshotFilename("a/b c:d"); got != "a_b_c_d" {
		t.Fatalf("unsafe chars → underscore, got %q", got)
	}
}

// --- diff/apply pure helpers -----------------------------------------------

func TestDiffsAreEnvOnly(t *testing.T) {
	t.Parallel()
	if diffsAreEnvOnly(nil) {
		t.Fatal("empty is not env-only")
	}
	if !diffsAreEnvOnly([]orchestrator.DiffKind{orchestrator.DiffEnv}) {
		t.Fatal("single env should be env-only")
	}
	if diffsAreEnvOnly([]orchestrator.DiffKind{orchestrator.DiffEnv, orchestrator.DiffBinary}) {
		t.Fatal("mixed kinds are not env-only")
	}
}

func TestSetFromSlice(t *testing.T) {
	t.Parallel()
	if setFromSlice(nil) != nil {
		t.Fatal("empty → nil")
	}
	got := setFromSlice([]string{" a ", "", "b"})
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("setFromSlice wrong: %v", got)
	}
}

func TestDedupeSorted(t *testing.T) {
	t.Parallel()
	if dedupeSorted(nil) != nil {
		t.Fatal("empty → nil")
	}
	got := dedupeSorted([]string{"c", "a", "c", "b"})
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("dedupeSorted wrong: %v", got)
	}
}

func TestSummarizeClusterDiff(t *testing.T) {
	t.Parallel()
	entries := []clusterDiffEntry{
		{Kinds: nil}, // skipped
		{Kinds: []orchestrator.DiffKind{orchestrator.DiffUnknown}},
		{Kinds: []orchestrator.DiffKind{orchestrator.DiffBinary}},
		{Kinds: []orchestrator.DiffKind{orchestrator.DiffUnknown, orchestrator.DiffEnv}},
	}
	s := summarizeClusterDiff(entries)
	if s.Total != 4 {
		t.Fatalf("Total = %d, want 4", s.Total)
	}
	if s.Unknown != 2 {
		t.Fatalf("Unknown = %d, want 2", s.Unknown)
	}
	if s.Changed != 2 {
		t.Fatalf("Changed = %d, want 2", s.Changed)
	}
}

func TestRenderClusterDiffText(t *testing.T) {
	t.Parallel()
	rep := clusterDiffReport{
		Cluster: "edge-eu",
		Entries: []clusterDiffEntry{
			{Host: "h1", Service: "commodore", Kinds: []orchestrator.DiffKind{orchestrator.DiffEnv},
				Details: map[orchestrator.DiffKind]string{orchestrator.DiffEnv: "VAR changed"}},
		},
		Summary: clusterDiffSummary{Total: 1, Changed: 1},
	}
	var buf bytes.Buffer
	renderClusterDiffText(&buf, rep)
	out := buf.String()
	for _, want := range []string{"edge-eu", "HOST", "commodore", "env", "1 changed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff text missing %q:\n%s", want, out)
		}
	}
}

func TestRenderExecuteResult(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderExecuteResult(&buf, orchestrator.ExecuteResult{})
	if !strings.Contains(buf.String(), "WAVE") {
		t.Fatalf("expected header row, got %q", buf.String())
	}
}

func TestInferApplyPhase(t *testing.T) {
	t.Parallel()
	m := &inventory.Manifest{
		Interfaces:    map[string]inventory.ServiceConfig{"chartroom": {}},
		Observability: map[string]inventory.ServiceConfig{"grafana": {}},
	}
	if inferApplyPhase(clusterApplyService{Service: "chartroom"}, m) != orchestrator.PhaseInterfaces {
		t.Fatal("interface service → PhaseInterfaces")
	}
	if inferApplyPhase(clusterApplyService{Service: "grafana"}, m) != orchestrator.PhaseInterfaces {
		t.Fatal("observability service → PhaseInterfaces")
	}
	if inferApplyPhase(clusterApplyService{Service: "commodore"}, m) != orchestrator.PhaseApplications {
		t.Fatal("default → PhaseApplications")
	}
}

func TestSSHConfigFor(t *testing.T) {
	t.Parallel()
	cfg := sshConfigFor(inventory.Host{Name: "h1", ExternalIP: "1.2.3.4", User: "deploy"})
	if cfg.Address != "1.2.3.4" || cfg.User != "deploy" || cfg.HostName != "h1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Port != 22 || cfg.Timeout != 30*time.Second {
		t.Fatalf("expected port 22 / 30s timeout, got %d / %v", cfg.Port, cfg.Timeout)
	}
}

// --- diagnose ports ---------------------------------------------------------

func TestBuildStandardPorts(t *testing.T) {
	t.Parallel()
	ports := buildStandardPorts()
	if ports[53] != "privateer-dns" || ports[18019] != "foghorn-control" {
		t.Fatalf("hardcoded ports missing/wrong: %v", ports[53])
	}
	if len(ports) <= 3 {
		t.Fatalf("expected servicedefs ports to be added, got %d entries", len(ports))
	}
}

func TestMediaDiagnosticPorts(t *testing.T) {
	t.Parallel()
	ports := mediaDiagnosticPorts()
	if len(ports) == 0 {
		t.Fatal("expected some media ports")
	}
	if !slices.IsSorted(ports) {
		t.Fatalf("ports must be sorted: %v", ports)
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if seen[p] {
			t.Fatalf("duplicate port %d", p)
		}
		seen[p] = true
	}
}

// --- service / migration resolution ----------------------------------------

func TestResolveDeployName(t *testing.T) {
	t.Parallel()
	if _, err := resolveDeployName("commodore", inventory.ServiceConfig{}); err != nil {
		t.Fatalf("known service should resolve: %v", err)
	}
	if _, err := resolveDeployName("not-a-service", inventory.ServiceConfig{}); err == nil {
		t.Fatal("unknown service id should error")
	}
}

func TestResolveMigrationTargetExplicit(t *testing.T) {
	t.Parallel()
	rc := &resolvedCluster{}
	got, err := resolveMigrationTarget(rc, "v1.2.3")
	if err != nil || got != "v1.2.3" {
		t.Fatalf("concrete explicit should pass through: got %q err %v", got, err)
	}
	if _, err := resolveMigrationTarget(rc, "stable"); err == nil {
		t.Fatal("channel name as explicit target must be rejected")
	}
}

// --- setup helpers ----------------------------------------------------------

func TestSuggestedContextName(t *testing.T) {
	t.Parallel()
	cases := map[fwcfg.Persona]string{
		fwcfg.PersonaPlatform:   "platform-prod",
		fwcfg.PersonaSelfHosted: "my-edge",
		fwcfg.PersonaUser:       "my-account",
		fwcfg.Persona("weird"):  "default",
	}
	for p, want := range cases {
		if got := suggestedContextName(p); got != want {
			t.Fatalf("suggestedContextName(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestContains(t *testing.T) {
	t.Parallel()
	if !contains([]string{"a", "b"}, "b") || contains([]string{"a"}, "z") || contains(nil, "x") {
		t.Fatal("contains wrong")
	}
}

func TestSetupResultFields(t *testing.T) {
	t.Parallel()
	// Non-platform persona: no control-plane / gitops fields.
	user := setupResultFields(fwcfg.Context{
		Name:      "acct",
		Persona:   fwcfg.PersonaUser,
		Endpoints: fwcfg.Endpoints{BridgeURL: "https://bridge"},
	})
	if !hasFieldKey(user, "context") || !hasFieldKey(user, "bridge url") {
		t.Fatalf("missing base fields: %+v", user)
	}
	if hasFieldKey(user, "control plane") {
		t.Fatal("non-platform persona should not have control-plane field")
	}

	// Platform persona with gitops: adds control plane + gitops fields.
	plat := setupResultFields(fwcfg.Context{
		Name:      "prod",
		Persona:   fwcfg.PersonaPlatform,
		Endpoints: fwcfg.Endpoints{BridgeURL: "https://bridge"},
		Gitops:    &fwcfg.Gitops{Source: fwcfg.GitopsLocal},
	})
	if !hasFieldKey(plat, "control plane") || !hasFieldKey(plat, "gitops") {
		t.Fatalf("platform persona should have control-plane + gitops fields: %+v", plat)
	}
}

func TestLooksLikeGitopsRoot(t *testing.T) {
	t.Parallel()
	if looksLikeGitopsRoot("") {
		t.Fatal("empty dir is not a gitops root")
	}
	dir := t.TempDir()
	if looksLikeGitopsRoot(dir) {
		t.Fatal("bare temp dir is not a gitops root")
	}
	mustMkdir(t, dir+"/clusters")
	mustWrite(t, dir+"/.sops.yaml", "creation_rules: []")
	if !looksLikeGitopsRoot(dir) {
		t.Fatal("clusters/ + .sops.yaml should look like a gitops root")
	}
}

func TestHasFrameworksEnv(t *testing.T) {
	t.Parallel()
	if (remoteEdgeEnv{}).HasFrameworksEnv() {
		t.Fatal("empty env should be false")
	}
	if !(remoteEdgeEnv{NodeID: "n1"}).HasFrameworksEnv() {
		t.Fatal("any populated field → true")
	}
}

// --- local test helpers -----------------------------------------------------

func hasFieldKey(fields []ux.ResultField, key string) bool {
	for _, f := range fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
