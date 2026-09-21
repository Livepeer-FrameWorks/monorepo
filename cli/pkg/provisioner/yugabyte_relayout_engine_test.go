//go:build schema_verify

package provisioner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"frameworks/cli/internal/releases"
	fwssh "frameworks/cli/pkg/ssh"
	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const ybRelayoutDumpDir = "/tmp/frameworks-relayout"

// dockerRunner stands in for the SSH runner production uses. It runs each script through a POSIX shell inside a
// container and reports the result through fwssh.CompleteRun, as the SSH client does: output trimmed, and a non-zero
// exit returned as an error. The tests therefore drive SSHYugabyteNode with the semantics it has over SSH.
type dockerRunner struct {
	container string
}

func (d dockerRunner) Run(ctx context.Context, command string) (*fwssh.CommandResult, error) {
	return d.run(ctx, "", "exec", d.container, "sh", "-c", command)
}

func (d dockerRunner) RunScript(ctx context.Context, script string) (*fwssh.CommandResult, error) {
	return d.run(ctx, script, "exec", "-i", d.container, "sh", "-s")
}

func (d dockerRunner) run(ctx context.Context, stdin string, args ...string) (*fwssh.CommandResult, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return fwssh.CompleteRun(&fwssh.CommandResult{Command: strings.Join(args, " ")}, d.container, strings.Join(args, " "), stdout.Bytes(), stderr.Bytes(), err)
}

func (dockerRunner) Upload(context.Context, fwssh.UploadOptions) error {
	return errors.New("relayout does not upload files")
}

func (dockerRunner) Close() error { return nil }

func ybRelayoutNode(container, host string) *SSHYugabyteNode {
	return &SSHYugabyteNode{NodeName: container, Runner: dockerRunner{container: container}, Host: host, Port: 5433}
}

func ybRelayoutEngine(database, ownerRole, runtimeRole string, nodes ...*SSHYugabyteNode) *YugabyteRelayout {
	// Every invocation is a distinct owner, as in production: revoking one must never refuse another.
	owner := fmt.Sprintf("schema-verify:%d:%d", os.Getpid(), time.Now().UnixNano())
	all := make([]YugabyteNode, len(nodes))
	for i, node := range nodes {
		node.ApplicationName = RelayoutApplicationName(owner)
		all[i] = node
	}
	return &YugabyteRelayout{
		Primary: nodes[0], Nodes: all, YSQLHost: nodes[0].Host, YSQLPort: 5433,
		Database: database, Roles: []string{ownerRole, runtimeRole}, DumpDir: ybRelayoutDumpDir,
		LeaseOwner: owner, LeaseTTL: 30 * time.Second, SettleDelay: 500 * time.Millisecond,
	}
}

func ybNodeApply(t *testing.T, node *SSHYugabyteNode, database, sql string) string {
	t.Helper()
	out, err := node.Query(context.Background(), database, sql)
	if err != nil {
		t.Fatalf("apply SQL to %s on %s: %v", database, node.Name(), err)
	}
	return strings.TrimSpace(out)
}

// ybLeased runs one relayout step under a renewed lease, the way the relayout command does, and logs its duration.
func ybLeased(t *testing.T, relayout *YugabyteRelayout, label string, step func(context.Context) error) time.Duration {
	t.Helper()
	holdCtx, stop := relayout.Hold(context.Background())
	started := time.Now()
	err := step(holdCtx)
	if err != nil && holdCtx.Err() != nil {
		err = fmt.Errorf("%w (%w)", err, context.Cause(holdCtx))
	}
	stop()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	elapsed := time.Since(started)
	t.Logf("yugabyte relayout: %s %s took %s", relayout.Database, label, elapsed.Round(time.Millisecond))
	return elapsed
}

// ybRelayoutSource builds a database shaped like an existing production database: created distributed with its owner,
// loaded from the baseline, granted to the operator analytics role and the runtime role, carrying the migration and
// data-migration ledgers and demo data, plus drift the baseline does not declare.
func ybRelayoutSource(t *testing.T, node *SSHYugabyteNode, source, logical, runtimeRole string) *DatabaseLayout {
	t.Helper()
	layout := ybDatabaseLayout(t, logical)
	ybNodeApply(t, node, "yugabyte", fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s LOGIN; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s LOGIN; END IF;
END $$;`, relayoutLiteral(logical), relayoutIdentifier(logical), relayoutLiteral(runtimeRole), relayoutIdentifier(runtimeRole)))
	ybNodeApply(t, node, "yugabyte", fmt.Sprintf("CREATE DATABASE %s OWNER %s", relayoutIdentifier(source), relayoutIdentifier(logical)))
	baseline, err := dbsql.Content.ReadFile("schema/" + logical + ".sql")
	if err != nil {
		t.Fatalf("read %s baseline: %v", logical, err)
	}
	ybNodeApply(t, node, source, string(baseline))
	if analytics, readErr := dbsql.Content.ReadFile("seeds/static/analytics_ro_" + logical + ".sql"); readErr == nil {
		ybNodeApply(t, node, source, string(analytics))
	}
	ybNodeApply(t, node, source, ybRuntimeGrantSQL(source, logical, runtimeRole))
	// The data-migration runner creates its ledgers unqualified through the service's connection, whose search_path
	// ("$user", public) resolves to the service schema, so production holds them there rather than in public.
	ybNodeApply(t, node, source, YugabyteMigrationLedgerDDL+`
INSERT INTO _migrations (version, phase, seq, filename, checksum) VALUES ('v0.3.0', 'expand', 1, '001_relayout_probe.sql', 'checksum-probe');
SET search_path TO `+relayoutIdentifier(logical)+`, public;
`+datamigrate.SchemaSQL+`
INSERT INTO _data_migrations (id, release_version, status) VALUES ('relayout_probe', 'v0.3.0', 'completed');
RESET search_path;`)
	if got := ybNodeApply(t, node, source, "SELECT to_regclass("+relayoutLiteral(logical+"._data_migrations")+") IS NOT NULL"); got != "t" {
		t.Fatalf("fixture did not create %s._data_migrations", logical)
	}
	if seedPath, ok := demoSeeds[logical]; ok {
		seed, readErr := dbsql.Content.ReadFile(seedPath)
		if readErr != nil {
			t.Fatalf("read %s demo seed: %v", logical, readErr)
		}
		ybNodeApply(t, node, source, string(seed))
	}
	ybNodeApply(t, node, source, fmt.Sprintf(`
CREATE INDEX relayout_drift_applied_at ON public._migrations (applied_at);
GRANT SELECT ON public._migrations TO %s;`, relayoutIdentifier(runtimeRole)))
	// Production functions created by the migrate role and later handed to the service role keep grants recorded as
	// made by the superuser, which no dump can restore under that grantor. The copy must still count as faithful.
	ybNodeApply(t, node, source, fmt.Sprintf(`
CREATE FUNCTION public.relayout_handed_over() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
GRANT EXECUTE ON FUNCTION public.relayout_handed_over() TO %[1]s;
ALTER FUNCTION public.relayout_handed_over() OWNER TO %[1]s;
GRANT EXECUTE ON FUNCTION public.relayout_handed_over() TO %[2]s;`, relayoutIdentifier(logical), relayoutIdentifier(runtimeRole)))
	if acl := ybNodeApply(t, node, source, "SELECT proacl::text FROM pg_proc WHERE proname = 'relayout_handed_over'"); !strings.Contains(acl, "/yugabyte") {
		t.Fatalf("fixture function ACL %s carries no superuser-granted entry", acl)
	}
	// Grantability is per privilege: the runtime role may pass CREATE on, but not CONNECT.
	ybNodeApply(t, node, "yugabyte", fmt.Sprintf(`GRANT CREATE ON DATABASE %s TO %s WITH GRANT OPTION;
COMMENT ON DATABASE %s IS %s;
ALTER DATABASE %s CONNECTION LIMIT %d;`, relayoutIdentifier(source), relayoutIdentifier(runtimeRole), relayoutIdentifier(source), relayoutLiteral(ybRelayoutSourceComment),
		relayoutIdentifier(source), ybRelayoutSourceConnectionLimit))
	return layout
}

const ybRelayoutSourceConnectionLimit = 250

const ybRelayoutSourceComment = "relayout source: keep this comment"

// ybRequireDatabaseAccess proves database carries the runtime role's per-privilege grants and the source comment.
func ybRequireDatabaseAccess(t *testing.T, node *SSHYugabyteNode, database, runtimeRole string) {
	t.Helper()
	grants := ybNodeApply(t, node, "yugabyte", fmt.Sprintf(`SELECT string_agg(a.privilege_type || ':' || a.is_grantable::text, ',' ORDER BY a.privilege_type)
  FROM pg_database d, aclexplode(d.datacl) a
 WHERE d.datname = %s AND a.grantee = %s::regrole`, relayoutLiteral(database), relayoutLiteral(runtimeRole)))
	if grants != "CONNECT:false,CREATE:true" {
		t.Fatalf("%s grants %s on %s = %q, want CONNECT without and CREATE with grant option", database, runtimeRole, database, grants)
	}
	comment := ybNodeApply(t, node, "yugabyte", fmt.Sprintf("SELECT coalesce(shobj_description(oid, 'pg_database'), '') FROM pg_database WHERE datname = %s", relayoutLiteral(database)))
	if comment != ybRelayoutSourceComment {
		t.Fatalf("%s comment = %q, want %q", database, comment, ybRelayoutSourceComment)
	}
	limit := ybNodeApply(t, node, "yugabyte", "SELECT datconnlimit FROM pg_database WHERE datname = "+relayoutLiteral(database))
	if limit != strconv.Itoa(ybRelayoutSourceConnectionLimit) {
		t.Fatalf("%s connection limit = %s, want %d", database, limit, ybRelayoutSourceConnectionLimit)
	}
}

// TestYugabyteRelayoutRestoresADefaultACL fences a database nobody ever granted on, so its ACL is NULL, and rolls it
// back. Fencing revokes the owner's implicit privileges along with PUBLIC's; the rollback must hand all of them back.
func TestYugabyteRelayoutRestoresADefaultACL(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-acl-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	ybNodeApply(t, node, "yugabyte", `DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'acl_owner') THEN CREATE ROLE acl_owner LOGIN; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'acl_runtime') THEN CREATE ROLE acl_runtime LOGIN; END IF;
END $$;`)
	ybNodeApply(t, node, "yugabyte", "CREATE DATABASE acl_probe OWNER acl_owner")
	if got := ybNodeApply(t, node, "yugabyte", "SELECT datacl IS NULL FROM pg_database WHERE datname = 'acl_probe'"); got != "t" {
		t.Fatalf("acl_probe starts with datacl set (%q); the test needs a default ACL", got)
	}

	ctx := context.Background()
	relayout := ybRelayoutEngine("acl_probe", "acl_owner", "acl_runtime", node)
	if _, err := relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// acl_probe declares no layout, so the test records prepared directly: it exercises only fence and rollback.
	ybLeased(t, relayout, "mark prepared", func(ctx context.Context) error {
		return relayout.advance(ctx, []RelayoutState{RelayoutPlanned}, RelayoutPrepared, nil)
	})
	ybLeased(t, relayout, "fence", relayout.Fence)
	privileges := func() string {
		return ybNodeApply(t, node, "yugabyte", `SELECT concat_ws(',',
  has_database_privilege('acl_owner', 'acl_probe', 'CREATE'), has_database_privilege('acl_owner', 'acl_probe', 'CONNECT'),
  has_database_privilege('acl_owner', 'acl_probe', 'TEMPORARY'), has_database_privilege('public', 'acl_probe', 'CONNECT'),
  has_database_privilege('public', 'acl_probe', 'TEMPORARY'))`)
	}
	if got := privileges(); got != "f,f,f,f,f" {
		t.Fatalf("privileges while fenced = %s, want every one revoked", got)
	}
	ybLeased(t, relayout, "rollback", relayout.Rollback)
	if got := privileges(); got != "t,t,t,t,t" {
		t.Fatalf("privileges after rollback = %s, want the owner's and PUBLIC's default privileges back", got)
	}
	if err := relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func ybCapabilityProbes(database string) []string {
	var probes []string
	for _, binary := range pkgdatabase.CapabilityServices() {
		if owned, ok := releases.ServiceDatabaseLookup(binary); ok && owned == database {
			for _, capability := range pkgdatabase.CapabilitiesFor(binary, pkgdatabase.EnginePostgres) {
				probes = append(probes, capability.Probe)
			}
		}
	}
	return probes
}

func ybRequireRuntimeConnects(t *testing.T, node *SSHYugabyteNode, database, runtimeRole string, allowed bool) {
	t.Helper()
	container := node.Runner.(dockerRunner).container
	_, err := docker(t, "", "exec", container, "ysqlsh", "-X", "-h", node.Host, "-U", runtimeRole, "-d", database, "-v", "ON_ERROR_STOP=1", "-tAc", "SELECT current_database()")
	switch {
	case allowed && err != nil:
		t.Fatalf("%s cannot connect to %s: %v", runtimeRole, database, dockerFailure(err))
	case !allowed && err == nil:
		t.Fatalf("%s connected to fenced database %s", runtimeRole, database)
	}
}

func dockerFailure(err error) error {
	var failure *dockerError
	if errors.As(err, &failure) {
		return fmt.Errorf("%w: %s", failure.err, strings.TrimSpace(failure.stderr))
	}
	return err
}

func ybDatabaseCount(t *testing.T, node *SSHYugabyteNode, database string) string {
	t.Helper()
	return ybNodeApply(t, node, "yugabyte", "SELECT count(*) FROM pg_database WHERE datname = "+relayoutLiteral(database))
}

func ybAnalyticsGrantCount(t *testing.T, node *SSHYugabyteNode, database string) int {
	t.Helper()
	count, _ := strconv.Atoi(ybNodeApply(t, node, database, "SELECT count(*) FROM information_schema.role_table_grants WHERE grantee = 'frameworks_analytics_ro'"))
	return count
}

func ybRequireRelaidDatabase(t *testing.T, node *SSHYugabyteNode, relayout *YugabyteRelayout, layout *DatabaseLayout, before *RelayoutEvidence, runtimeRole string, analyticsGrants int) {
	t.Helper()
	database := relayout.Database
	container := node.Runner.(dockerRunner).container
	if got := ybNodeApply(t, node, database, "SELECT yb_is_database_colocated()"); got != "t" {
		t.Fatalf("%s colocated after cutover = %q", database, got)
	}
	ybRequireDeclaredPlacement(t, container, database, layout, false)
	ybRequireRuntimeConnects(t, node, database, runtimeRole, true)
	ybRequireDatabaseAccess(t, node, database, runtimeRole)
	after, err := relayout.CollectEvidence(context.Background(), database)
	if err != nil {
		t.Fatalf("collect relaid evidence: %v", err)
	}
	if differences := CompareRelayoutEvidence(before, after); len(differences) > 0 {
		t.Fatalf("relaid %s differs from the original:\n%s", database, strings.Join(differences, "\n"))
	}
	if got := ybNodeApply(t, node, database, "SELECT count(*) FROM pg_indexes WHERE indexname = 'relayout_drift_applied_at'"); got != "1" {
		t.Fatalf("relaid %s lost the drift index the source carried", database)
	}
	if got := ybAnalyticsGrantCount(t, node, database); got != analyticsGrants {
		t.Fatalf("relaid %s has %d analytics grants, source had %d", database, got, analyticsGrants)
	}
}

func TestYugabyteRelayoutMovesDatabaseIntoDeclaredLayout(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	const logical, runtimeRole = "purser", "purser_runtime"
	layout := ybRelayoutSource(t, node, logical, logical, runtimeRole)

	ctx := context.Background()
	relayout := ybRelayoutEngine(logical, logical, runtimeRole, node)
	analyticsGrants := ybAnalyticsGrantCount(t, node, logical)
	if analyticsGrants == 0 {
		t.Fatal("source carries no analytics grants; the test would not prove they are copied")
	}
	if _, err := relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ybLeased(t, relayout, "prepare", func(ctx context.Context) error { return relayout.Prepare(ctx, layout) })
	ybRequireRuntimeConnects(t, node, logical, runtimeRole, true)
	if got := ybNodeApply(t, node, relayout.ShadowName(), "SELECT count(*) FROM public._migrations"); got != "0" {
		t.Fatalf("prepare copied %s rows into the shadow; it must copy the schema only", got)
	}
	// Data is copied inside the window, so a row written after prepare must reach the relaid database.
	ybNodeApply(t, node, logical, `INSERT INTO _migrations (version, phase, seq, filename, checksum)
VALUES ('v0.3.0', 'expand', 2, '002_after_prepare.sql', 'checksum-after-prepare')`)
	before, err := relayout.CollectEvidence(ctx, logical)
	if err != nil {
		t.Fatalf("collect source evidence: %v", err)
	}
	ybLeased(t, relayout, "fence", relayout.Fence)
	ybRequireRuntimeConnects(t, node, logical, runtimeRole, false)
	// Building indexes leaves YugabyteDB's own superuser connections idle on the shadow; the copy must end them rather
	// than wait for them to go away.
	lingering := exec.Command("docker", "exec", container, "ysqlsh", "-X", "-h", node.Host, "-U", "yugabyte", "-d", relayout.ShadowName(), "-c", "SELECT pg_sleep(900)")
	if err = lingering.Start(); err != nil {
		t.Fatalf("open a superuser session on the shadow: %v", err)
	}
	t.Cleanup(func() { _ = lingering.Process.Kill() })
	for deadline := time.Now().Add(time.Minute); ybNodeApply(t, node, "yugabyte",
		"SELECT count(*) FROM pg_stat_activity WHERE datname = "+relayoutLiteral(relayout.ShadowName())+" AND query LIKE '%pg_sleep%'") == "0"; {
		if time.Now().After(deadline) {
			t.Fatal("superuser session on the shadow never appeared")
		}
		time.Sleep(time.Second)
	}
	// A copy interrupted after loading, before the journal records it, must empty the shadow and load again.
	ybLeased(t, relayout, "interrupted copy", func(ctx context.Context) error {
		dump, dumpErr := relayout.stageDump(ctx, "relayout", relayoutDataSections)
		if dumpErr != nil {
			return dumpErr
		}
		if advanceErr := relayout.advance(ctx, []RelayoutState{RelayoutFenced}, RelayoutDumped, map[string]string{
			"dump_path": relayoutLiteral(dump.Dir), "dump_sha256": relayoutLiteral(dump.manifestSHA256()), "dump_host": relayoutLiteral(node.Name()),
		}); advanceErr != nil {
			return advanceErr
		}
		return relayout.loadData(ctx, relayout.ShadowName(), dump, &RelayoutCopyTimings{})
	})
	ybLeased(t, relayout, "copy", relayout.Copy)
	probes := ybCapabilityProbes(logical)
	ybLeased(t, relayout, "verify", func(ctx context.Context) error {
		_, verifyErr := relayout.Verify(ctx, layout, runtimeRole, probes)
		return verifyErr
	})

	// An interruption after the source is renamed aside but before the journal records it must resume cleanly.
	if err = relayout.renameDatabase(ctx, logical, relayout.PreRelayoutName()); err != nil {
		t.Fatalf("simulate interrupted rename: %v", err)
	}
	ybLeased(t, relayout, "cutover", func(ctx context.Context) error { return relayout.Cutover(ctx, layout, runtimeRole, probes) })
	ybRequireRelaidDatabase(t, node, relayout, layout, before, runtimeRole, analyticsGrants)

	ybLeased(t, relayout, "finish", relayout.Finish)
	if got := ybDatabaseCount(t, node, relayout.PreRelayoutName()); got != "0" {
		t.Fatalf("finish left %s behind", relayout.PreRelayoutName())
	}
	if out, _ := node.Shell(ctx, "if [ -e "+shellQuote(ybRelayoutDumpDir+"/"+logical+"/relayout")+" ]; then echo present; fi"); strings.TrimSpace(out) != "" {
		t.Fatal("finish left the staged dump behind")
	}
	record, err := relayout.Record(ctx)
	if err != nil || record.State != RelayoutFinished {
		t.Fatalf("journal after finish = %+v, %v", record, err)
	}
	if err = relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestYugabyteRelayoutCutoverRunsTheWindowFromPrepare runs cutover straight from a prepared shadow, as the relayout
// command does: one invocation fences, copies, verifies, renames, and restores access.
func TestYugabyteRelayoutCutoverRunsTheWindowFromPrepare(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-direct-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	const logical, runtimeRole = "quartermaster", "quartermaster_runtime"
	layout := ybRelayoutSource(t, node, logical, logical, runtimeRole)

	ctx := context.Background()
	relayout := ybRelayoutEngine(logical, logical, runtimeRole, node)
	before, err := relayout.CollectEvidence(ctx, logical)
	if err != nil {
		t.Fatalf("collect source evidence: %v", err)
	}
	analyticsGrants := ybAnalyticsGrantCount(t, node, logical)
	if _, err = relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ybLeased(t, relayout, "prepare", func(ctx context.Context) error { return relayout.Prepare(ctx, layout) })
	ybLeased(t, relayout, "cutover from prepared", func(ctx context.Context) error {
		return relayout.Cutover(ctx, layout, runtimeRole, ybCapabilityProbes(logical))
	})
	record, err := relayout.Record(ctx)
	if err != nil || record.State != RelayoutAccessRestored || record.Receipt == nil {
		t.Fatalf("journal after direct cutover = %+v, %v; want access_restored with a receipt", record, err)
	}
	ybRequireRelaidDatabase(t, node, relayout, layout, before, runtimeRole, analyticsGrants)
	if err = relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestYugabyteRelayoutRollbackRestoresOriginalDatabase(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-rollback-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	const logical, runtimeRole = "navigator", "navigator_runtime"
	layout := ybRelayoutSource(t, node, logical, logical, runtimeRole)

	ctx := context.Background()
	relayout := ybRelayoutEngine(logical, logical, runtimeRole, node)
	before, err := relayout.CollectEvidence(ctx, logical)
	if err != nil {
		t.Fatalf("collect source evidence: %v", err)
	}
	if _, err = relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ybLeased(t, relayout, "prepare", func(ctx context.Context) error { return relayout.Prepare(ctx, layout) })
	ybLeased(t, relayout, "fence", relayout.Fence)
	ybLeased(t, relayout, "copy", relayout.Copy)
	ybLeased(t, relayout, "verify", func(ctx context.Context) error {
		_, verifyErr := relayout.Verify(ctx, layout, runtimeRole, ybCapabilityProbes(logical))
		return verifyErr
	})

	// Stop between the two renames with the shadow already holding the canonical name.
	ybLeased(t, relayout, "partial cutover", func(ctx context.Context) error {
		if renameErr := relayout.renameDatabase(ctx, logical, relayout.PreRelayoutName()); renameErr != nil {
			return renameErr
		}
		if advanceErr := relayout.advance(ctx, []RelayoutState{RelayoutVerified}, RelayoutRenamedOld, nil); advanceErr != nil {
			return advanceErr
		}
		if renameErr := relayout.renameDatabase(ctx, relayout.ShadowName(), logical); renameErr != nil {
			return renameErr
		}
		return relayout.advance(ctx, []RelayoutState{RelayoutRenamedOld}, RelayoutRenamedNew, nil)
	})
	ybLeased(t, relayout, "rollback", relayout.Rollback)

	if got := ybNodeApply(t, node, logical, "SELECT yb_is_database_colocated()"); got != "f" {
		t.Fatalf("%s after rollback is colocated=%q, want the original distributed database", logical, got)
	}
	for _, artifact := range []string{relayout.ShadowName(), relayout.PreRelayoutName()} {
		if got := ybDatabaseCount(t, node, artifact); got != "0" {
			t.Fatalf("rollback left %s behind", artifact)
		}
	}
	ybRequireRuntimeConnects(t, node, logical, runtimeRole, true)
	ybRequireDatabaseAccess(t, node, logical, runtimeRole)
	after, err := relayout.CollectEvidence(ctx, logical)
	if err != nil {
		t.Fatalf("collect rolled-back evidence: %v", err)
	}
	if differences := CompareRelayoutEvidence(before, after); len(differences) > 0 {
		t.Fatalf("rolled-back %s differs from the original:\n%s", logical, strings.Join(differences, "\n"))
	}
	record, err := relayout.Record(ctx)
	if err != nil || record.State != RelayoutRolledBack {
		t.Fatalf("journal after rollback = %+v, %v", record, err)
	}
	if err = relayout.Cutover(ctx, layout, runtimeRole, nil); err == nil {
		t.Fatal("cutover ran from a rolled-back journal")
	}
	if err = relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestYugabyteRelayoutVerifyRejectsAChangedShadow proves verify compares content and privileges rather than passing on
// any copy: a shadow row changed after prepare, and a column grant only the shadow has, both fail verification.
func TestYugabyteRelayoutVerifyRejectsAChangedShadow(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-tamper-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	const source, logical, runtimeRole = "navigator_tamper", "navigator", "navigator_runtime"
	layout := ybRelayoutSource(t, node, source, logical, runtimeRole)

	ctx := context.Background()
	relayout := ybRelayoutEngine(source, logical, runtimeRole, node)
	if _, err := relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ybLeased(t, relayout, "prepare", func(ctx context.Context) error { return relayout.Prepare(ctx, layout) })
	ybLeased(t, relayout, "fence", relayout.Fence)
	ybLeased(t, relayout, "copy", relayout.Copy)
	ybNodeApply(t, node, relayout.ShadowName(), fmt.Sprintf(`UPDATE public._migrations SET checksum = 'tampered' WHERE seq = 1;
GRANT SELECT (version) ON public._migrations TO %s;`, relayoutIdentifier(runtimeRole)))
	holdCtx, stop := relayout.Hold(ctx)
	_, err := relayout.Verify(holdCtx, layout, runtimeRole, ybCapabilityProbes(logical))
	stop()
	if err == nil {
		t.Fatal("verify accepted a shadow whose content and column privileges differ from the source")
	}
	for _, want := range []string{"table public._migrations", "attribute privilege:column:public._migrations.version:" + runtimeRole + ":SELECT is not in the source"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("verify error does not name %q:\n%v", want, err)
		}
	}
	ybLeased(t, relayout, "rollback", relayout.Rollback)
	if err = relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestYugabyteRelayoutRollbackResumesAfterAnInterruptedRollback runs the rollback steps up to the drop of the relaid
// copy and stops before access is restored, as a rollback killed there would. The rerun must find the original under
// its canonical name, leave it there, restore access, and finish.
func TestYugabyteRelayoutRollbackResumesAfterAnInterruptedRollback(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-rerollback-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	const source, logical, runtimeRole = "navigator_rerollback", "navigator", "navigator_runtime"
	layout := ybRelayoutSource(t, node, source, logical, runtimeRole)

	ctx := context.Background()
	relayout := ybRelayoutEngine(source, logical, runtimeRole, node)
	before, err := relayout.CollectEvidence(ctx, source)
	if err != nil {
		t.Fatalf("collect source evidence: %v", err)
	}
	if _, err = relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ybLeased(t, relayout, "prepare", func(ctx context.Context) error { return relayout.Prepare(ctx, layout) })
	ybLeased(t, relayout, "fence", relayout.Fence)
	ybLeased(t, relayout, "copy", relayout.Copy)
	ybLeased(t, relayout, "verify", func(ctx context.Context) error {
		_, verifyErr := relayout.Verify(ctx, layout, runtimeRole, ybCapabilityProbes(logical))
		return verifyErr
	})
	ybLeased(t, relayout, "cutover renames", func(ctx context.Context) error {
		if renameErr := relayout.renameDatabase(ctx, source, relayout.PreRelayoutName()); renameErr != nil {
			return renameErr
		}
		if advanceErr := relayout.advance(ctx, []RelayoutState{RelayoutVerified}, RelayoutRenamedOld, nil); advanceErr != nil {
			return advanceErr
		}
		if renameErr := relayout.renameDatabase(ctx, relayout.ShadowName(), source); renameErr != nil {
			return renameErr
		}
		return relayout.advance(ctx, []RelayoutState{RelayoutRenamedOld}, RelayoutRenamedNew, nil)
	})
	ybLeased(t, relayout, "interrupted rollback", func(ctx context.Context) error {
		record, recordErr := relayout.Record(ctx)
		if recordErr != nil {
			return recordErr
		}
		if returnErr := relayout.returnOriginal(ctx, record); returnErr != nil {
			return returnErr
		}
		return relayout.dropCopy(ctx, relayout.ShadowName(), relayoutCopyShadow)
	})
	ybRequireRuntimeConnects(t, node, source, runtimeRole, false)
	ybLeased(t, relayout, "rollback rerun", relayout.Rollback)

	if got := ybNodeApply(t, node, source, "SELECT yb_is_database_colocated()"); got != "f" {
		t.Fatalf("%s after the rerun is colocated=%q, want the original distributed database", source, got)
	}
	ybRequireRuntimeConnects(t, node, source, runtimeRole, true)
	after, err := relayout.CollectEvidence(ctx, source)
	if err != nil {
		t.Fatalf("collect rolled-back evidence: %v", err)
	}
	if differences := CompareRelayoutEvidence(before, after); len(differences) > 0 {
		t.Fatalf("rolled-back %s differs from the original:\n%s", source, strings.Join(differences, "\n"))
	}
	if record, recordErr := relayout.Record(ctx); recordErr != nil || record.State != RelayoutRolledBack || record.SourceOID != "" {
		t.Fatalf("journal after the rerun = %+v, %v; want rolled_back with the source identity cleared", record, recordErr)
	}
	if err = relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestYugabyteRelayoutRefusesASchemaChangedAfterPrepare changes the source schema after prepare. Cutover must refuse
// before fencing, so services never lose access, and a rollback of the prepared relayout only drops the shadow.
func TestYugabyteRelayoutRefusesASchemaChangedAfterPrepare(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-drift-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	const source, logical, runtimeRole = "navigator_drift", "navigator", "navigator_runtime"
	layout := ybRelayoutSource(t, node, source, logical, runtimeRole)

	ctx := context.Background()
	relayout := ybRelayoutEngine(source, logical, runtimeRole, node)
	if _, err := relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ybLeased(t, relayout, "prepare", func(ctx context.Context) error { return relayout.Prepare(ctx, layout) })
	ybNodeApply(t, node, source, "CREATE INDEX relayout_late_index ON public._migrations (filename)")
	holdCtx, stop := relayout.Hold(ctx)
	err := relayout.Cutover(holdCtx, layout, runtimeRole, ybCapabilityProbes(logical))
	stop()
	if err == nil || !strings.Contains(err.Error(), "changed after prepare") || !strings.Contains(err.Error(), "relayout_late_index") {
		t.Fatalf("cutover after a schema change = %v, want a refusal naming the new index", err)
	}
	ybRequireRuntimeConnects(t, node, source, runtimeRole, true)
	if record, recordErr := relayout.Record(ctx); recordErr != nil || record.State != RelayoutPrepared || record.SourceOID != "" {
		t.Fatalf("journal after the refused cutover = %+v, %v; want prepared and never fenced", record, recordErr)
	}
	ybLeased(t, relayout, "rollback", relayout.Rollback)
	if got := ybDatabaseCount(t, node, relayout.ShadowName()); got != "0" {
		t.Fatalf("rollback left %s behind", relayout.ShadowName())
	}
	ybRequireRuntimeConnects(t, node, source, runtimeRole, true)
	if err = relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestYugabyteRelayoutTakeoverEndsTheStaleOwnersRemoteWork leaves a client running on the tserver under the first
// owner's application name, as a restore does after its relayout process dies and its SSH connection drops. Once the
// first lease expires, the second owner's Acquire must end that client before it takes the lease.
func TestYugabyteRelayoutTakeoverEndsTheStaleOwnersRemoteWork(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-takeover-%d", time.Now().UnixNano()))
	host := ybSQLHost(container)
	setup := ybRelayoutNode(container, host)
	ybNodeApply(t, setup, "yugabyte", `DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'takeover_owner') THEN CREATE ROLE takeover_owner LOGIN; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'takeover_runtime') THEN CREATE ROLE takeover_runtime LOGIN; END IF;
END $$;`)
	ybNodeApply(t, setup, "yugabyte", "CREATE DATABASE takeover_probe OWNER takeover_owner")

	ctx := context.Background()
	first := ybRelayoutEngine("takeover_probe", "takeover_owner", "takeover_runtime", ybRelayoutNode(container, host))
	first.LeaseTTL = 3 * time.Second
	if _, err := first.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	worker := exec.Command("docker", "exec", "-e", "PGAPPNAME="+RelayoutApplicationName(first.LeaseOwner), container,
		"ysqlsh", "-X", "-h", host, "-U", "yugabyte", "-d", "takeover_probe", "-c", "SELECT pg_sleep(600)")
	if err := worker.Start(); err != nil {
		t.Fatalf("start the stale worker: %v", err)
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Wait() }()
	t.Cleanup(func() { _ = worker.Process.Kill() })
	for deadline := time.Now().Add(time.Minute); ybNodeApply(t, setup, "yugabyte",
		"SELECT count(*) FROM pg_stat_activity WHERE application_name = "+relayoutLiteral(RelayoutApplicationName(first.LeaseOwner))) == "0"; {
		if time.Now().After(deadline) {
			t.Fatal("the stale worker's session never appeared")
		}
		time.Sleep(time.Second)
	}

	// A script the first invocation started is between database connections, as a restore is while it checksums its
	// dump: no session exists yet, but it would connect and write once its current command finishes.
	firstNode := first.Primary.(*SSHYugabyteNode)
	scriptDone := make(chan error, 1)
	go func() {
		_, err := firstNode.Shell(context.Background(), fmt.Sprintf(`sleep 600
ysqlsh -X -h %s -U yugabyte -d takeover_probe -c 'CREATE TABLE public.stale_write (id int)'`, shellQuote(host)))
		scriptDone <- err
	}()
	registry := relayoutWorkerRoot + "/" + RelayoutApplicationName(first.LeaseOwner)
	for deadline := time.Now().Add(time.Minute); ; {
		out, _ := setup.Shell(ctx, "ls "+shellQuote(registry)+" 2>/dev/null | wc -l")
		if strings.TrimSpace(out) != "0" && strings.TrimSpace(out) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stale script never registered as a relayout worker")
		}
		time.Sleep(time.Second)
	}

	secondNode := ybRelayoutNode(container, host)
	second := ybRelayoutEngine("takeover_probe", "takeover_owner", "takeover_runtime", secondNode)
	second.LeaseOwner = "second-owner"
	secondNode.ApplicationName = RelayoutApplicationName(second.LeaseOwner)
	if _, err := second.Acquire(ctx); err == nil {
		t.Fatal("a second owner took a lease that had not expired")
	}
	select {
	case <-workerDone:
		t.Fatal("refusing a live lease ended the holder's work")
	case <-scriptDone:
		t.Fatal("refusing a live lease ended the holder's script")
	default:
	}
	time.Sleep(first.LeaseTTL + time.Second)
	record, err := second.Acquire(ctx)
	if err != nil {
		t.Fatalf("second Acquire after expiry: %v", err)
	}
	if record.LeaseOwner != second.LeaseOwner {
		t.Fatalf("lease owner after takeover = %q", record.LeaseOwner)
	}
	select {
	case <-workerDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the takeover left the stale owner's client running")
	}
	select {
	case <-scriptDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the takeover left the stale owner's script running between connections")
	}
	if got := ybNodeApply(t, setup, "takeover_probe", "SELECT to_regclass('public.stale_write') IS NULL"); got != "t" {
		t.Fatal("the stale owner's script connected and wrote after the takeover")
	}
	if err = second.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestYugabyteRelayoutWorkerAdmissionFailsClosed runs the worker wrapper on the engine host. A script that registers
// only after its owner was revoked, as an abandoned invocation paused before registering would, must not run its
// body; neither may a script whose registration cannot be written.
func TestYugabyteRelayoutWorkerAdmissionFailsClosed(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-admission-%d", time.Now().UnixNano()))
	host := ybSQLHost(container)
	ctx := context.Background()
	fencer := ybRelayoutNode(container, host)
	fencer.ApplicationName = RelayoutApplicationName("fencer")
	ran := func(marker string) bool {
		out, _ := fencer.Shell(ctx, "if [ -e "+shellQuote(marker)+" ]; then echo ran; fi")
		return strings.TrimSpace(out) == "ran"
	}

	late := ybRelayoutNode(container, host)
	late.ApplicationName = RelayoutApplicationName("late-owner")
	if out, err := fencer.Shell(ctx, relayoutStopWorkersScript(late.ApplicationName)); err != nil || !strings.Contains(out, "remaining=0") {
		t.Fatalf("revoke late-owner: %q, %v", out, err)
	}
	_, err := late.Shell(ctx, "touch /tmp/relayout-late-ran")
	if status := ybExitStatus(err); status != relayoutWorkerRevokedExit || ran("/tmp/relayout-late-ran") {
		t.Fatalf("a worker registering after revocation exited %d (%v), body ran=%v; want %d before the body", status, err, ran("/tmp/relayout-late-ran"), relayoutWorkerRevokedExit)
	}

	broken := ybRelayoutNode(container, host)
	broken.ApplicationName = RelayoutApplicationName("broken-owner")
	if _, err = fencer.Shell(ctx, "mkdir -p "+shellQuote(relayoutWorkerRoot)+" && touch "+shellQuote(relayoutWorkerRoot+"/"+broken.ApplicationName)); err != nil {
		t.Fatalf("block the registry path: %v", err)
	}
	_, err = broken.Shell(ctx, "touch /tmp/relayout-unregistered-ran")
	if status := ybExitStatus(err); status != relayoutWorkerUnregistered || ran("/tmp/relayout-unregistered-ran") {
		t.Fatalf("a worker that cannot register exited %d (%v), body ran=%v; want %d before the body", status, err, ran("/tmp/relayout-unregistered-ran"), relayoutWorkerUnregistered)
	}

	// A registry someone other than root can write could hold forged records, so neither a worker nor a fence may
	// trust it.
	exposed := ybRelayoutNode(container, host)
	exposed.ApplicationName = RelayoutApplicationName("exposed-owner")
	exposedDir := relayoutWorkerRoot + "/" + exposed.ApplicationName
	if _, err = fencer.Shell(ctx, "mkdir -p "+shellQuote(exposedDir)+" && chmod 0777 "+shellQuote(exposedDir)); err != nil {
		t.Fatalf("expose a registry directory: %v", err)
	}
	_, err = exposed.Shell(ctx, "touch /tmp/relayout-exposed-ran")
	if status := ybExitStatus(err); status != relayoutWorkerUnregistered || ran("/tmp/relayout-exposed-ran") {
		t.Fatalf("a worker with a world-writable registry exited %d (%v), body ran=%v; want %d before the body", status, err, ran("/tmp/relayout-exposed-ran"), relayoutWorkerUnregistered)
	}
	if out, stopErr := fencer.Shell(ctx, relayoutStopWorkersScript(exposed.ApplicationName)); stopErr == nil {
		t.Fatalf("a fence trusted a world-writable registry: %q", out)
	}

	admitted := ybRelayoutNode(container, host)
	admitted.ApplicationName = RelayoutApplicationName("admitted-owner")
	_, err = admitted.Shell(ctx, "touch /tmp/relayout-admitted-ran; exit 3")
	if status := ybExitStatus(err); status != 3 || !ran("/tmp/relayout-admitted-ran") {
		t.Fatalf("an admitted worker exited %d (%v), body ran=%v; want its body to run and its status 3 to pass through", status, err, ran("/tmp/relayout-admitted-ran"))
	}
	if out, _ := fencer.Shell(ctx, "ls -A "+shellQuote(relayoutWorkerRoot+"/"+admitted.ApplicationName)); strings.TrimSpace(out) != "" {
		t.Fatalf("a finished worker left its record behind: %q", out)
	}
}

// ybExitStatus returns the remote exit status carried by a runner error, 0 for no error, or -1 when it carries none.
func ybExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func TestYugabyteRelayoutLeaseExcludesSecondOwner(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-lease-%d", time.Now().UnixNano()))
	node := ybRelayoutNode(container, ybSQLHost(container))
	ybNodeApply(t, node, "yugabyte", `DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'lease_owner') THEN CREATE ROLE lease_owner LOGIN; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'lease_runtime') THEN CREATE ROLE lease_runtime LOGIN; END IF;
END $$;`)
	ybNodeApply(t, node, "yugabyte", "CREATE DATABASE lease_probe OWNER lease_owner")

	ctx := context.Background()
	first := ybRelayoutEngine("lease_probe", "lease_owner", "lease_runtime", node)
	second := ybRelayoutEngine("lease_probe", "lease_owner", "lease_runtime", node)
	second.LeaseOwner = "second-owner"
	if _, err := first.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if _, err := second.Acquire(ctx); err == nil {
		t.Fatal("a second owner acquired a held lease")
	}

	// Outlive the TTL under renewal; the lease must still be ours and usable.
	holdCtx, stop := first.Hold(ctx)
	select {
	case <-time.After(first.LeaseTTL + first.LeaseTTL/2):
	case <-holdCtx.Done():
		t.Fatalf("lease renewal failed: %v", context.Cause(holdCtx))
	}
	if err := first.requireLease(holdCtx); err != nil {
		t.Fatalf("lease after renewals: %v", err)
	}
	stop()
	if _, err := second.Acquire(ctx); err == nil {
		t.Fatal("a second owner acquired the lease while it was still valid")
	}
	if err := first.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := second.Acquire(ctx); err != nil {
		t.Fatalf("second Acquire after release: %v", err)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// TestYugabyteRelayoutPreflightRehearsesDatabase rehearses the production copy for each selected colocated database:
// a live source shaped like production is dumped, rewritten, restored into a scratch database, verified, and dropped.
func TestYugabyteRelayoutPreflightRehearsesDatabase(t *testing.T) {
	requireDocker(t)
	container := ybStart(t, fmt.Sprintf("fw-sv-yb-relayout-preflight-%d", time.Now().UnixNano()))
	services, _ := yugabyteServiceDatabases(t)
	for _, logical := range services {
		layout := ybDatabaseLayout(t, logical)
		if !layout.Colocated() {
			t.Logf("yugabyte relayout: %s is distributed; nothing to rehearse", logical)
			continue
		}
		t.Run(logical, func(t *testing.T) {
			node := ybRelayoutNode(container, ybSQLHost(container))
			source, runtimeRole := logical+"_source", logical+"_runtime"
			ybRelayoutSource(t, node, source, logical, runtimeRole)
			relayout := ybRelayoutEngine(source, logical, runtimeRole, node)
			ctx := context.Background()
			if _, err := relayout.Acquire(ctx); err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			var report *RelayoutPreflight
			ybLeased(t, relayout, "preflight", func(ctx context.Context) error {
				var preflightErr error
				report, preflightErr = relayout.Preflight(ctx, layout, runtimeRole, ybCapabilityProbes(logical))
				return preflightErr
			})
			timings := report.Timings
			t.Logf("yugabyte relayout: %s preflight tables=%d data=%dB before the window: schema dump=%s pre=%s post=%s; inside it: compare=%s data dump=%s load=%s checksum=%s checks=%s estimated window=%s",
				logical, report.Tables, report.DumpBytes, timings.SchemaDump, timings.PreData, timings.PostData, timings.Compare,
				timings.DataDump, timings.Data, report.ChecksumDuration, report.WindowChecks, report.EstimatedDowntime())
			if got := ybDatabaseCount(t, node, relayout.PreflightName()); got != "0" {
				t.Fatalf("preflight left %s behind", relayout.PreflightName())
			}
			if out, _ := node.Shell(ctx, "if [ -e "+shellQuote(ybRelayoutDumpDir+"/"+source+"/preflight")+" ]; then echo present; fi"); strings.TrimSpace(out) != "" {
				t.Fatal("preflight left its dump behind")
			}
			if record, err := relayout.Record(ctx); err != nil || record.State != RelayoutPlanned {
				t.Fatalf("preflight changed the journal: %+v, %v", record, err)
			}
			if err := relayout.Release(ctx); err != nil {
				t.Fatalf("Release: %v", err)
			}
		})
	}
}
