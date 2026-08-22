//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/lib/pq"
)

func TestBootstrapAccountsRepository_RealPG(t *testing.T) {
	db := startCommodoreBootstrapRealPG(t)
	ctx := context.Background()
	tenantID := "10000000-0000-4000-8000-000000000001"
	account := Account{
		Kind: AccountSystemOperator, Tenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Users: []AccountUser{{
			Email: "ops@example.test", Role: "owner", FirstName: "Ops", LastName: "Person",
			Password: "original-password", PlatformOperator: true,
		}},
	}

	result, warnings, err := ReconcileAccounts(ctx, db, []Account{account}, staticResolver(tenantID), false)
	if err != nil || len(warnings) != 0 || len(result.Created) != 1 {
		t.Fatalf("create account = %#v, warnings = %#v, err = %v", result, warnings, err)
	}
	var userID, passwordHash string
	var permissions []string
	if err := db.QueryRowContext(ctx, `
		SELECT id::text, password_hash, permissions
		FROM commodore.users
		WHERE tenant_id = $1::uuid AND email = 'ops@example.test'
	`, tenantID).Scan(&userID, &passwordHash, pq.Array(&permissions)); err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword("original-password", passwordHash) {
		t.Fatal("created password hash does not verify")
	}
	if fmt.Sprint(permissions) != "[read write admin]" {
		t.Fatalf("permissions = %#v", permissions)
	}

	replay := account
	replay.Users = append([]AccountUser(nil), account.Users...)
	replay.Users[0].Email = "OPS@EXAMPLE.TEST"
	result, warnings, err = ReconcileAccounts(ctx, db, []Account{replay}, staticResolver(tenantID), false)
	if err != nil || len(warnings) != 0 || len(result.Noop) != 1 {
		t.Fatalf("case-insensitive replay = %#v, warnings = %#v, err = %v", result, warnings, err)
	}

	replay.Users[0].FirstName = "Operator"
	replay.Users[0].Password = "must-not-replace"
	result, _, err = ReconcileAccounts(ctx, db, []Account{replay}, staticResolver(tenantID), false)
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("profile update = %#v, err = %v", result, err)
	}
	var updatedHash, firstName string
	if err := db.QueryRowContext(ctx, `SELECT password_hash, first_name FROM commodore.users WHERE id = $1::uuid`, userID).Scan(&updatedHash, &firstName); err != nil {
		t.Fatal(err)
	}
	if updatedHash != passwordHash || firstName != "Operator" {
		t.Fatalf("profile update changed credentials or missed profile: hash_changed=%t first_name=%q", updatedHash != passwordHash, firstName)
	}

	replay.Users[0].ResetCredentials = true
	result, warnings, err = ReconcileAccounts(ctx, db, []Account{replay}, staticResolver(tenantID), false)
	if err != nil || len(warnings) != 1 || len(result.Noop) != 1 {
		t.Fatalf("guarded credential reset = %#v, warnings = %#v, err = %v", result, warnings, err)
	}
	result, warnings, err = ReconcileAccounts(ctx, db, []Account{replay}, staticResolver(tenantID), true)
	if err != nil || len(warnings) != 0 || len(result.Updated) != 1 {
		t.Fatalf("authorized credential reset = %#v, warnings = %#v, err = %v", result, warnings, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT password_hash FROM commodore.users WHERE id = $1::uuid`, userID).Scan(&updatedHash); err != nil {
		t.Fatal(err)
	}
	if updatedHash == passwordHash || !auth.CheckPassword("must-not-replace", updatedHash) {
		t.Fatal("authorized credential reset did not replace the password hash")
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.users WHERE tenant_id = $1::uuid`, tenantID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("bootstrap replay created %d users, want 1", count)
	}
}

func TestBootstrapPullStreamsRepository_RealPG(t *testing.T) {
	db := startCommodoreBootstrapRealPG(t)
	ctx := context.Background()
	tenantID := "10000000-0000-4000-8000-000000000001"
	ownerID := "20000000-0000-4000-8000-000000000002"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.users
			(id, tenant_id, email, role, permissions, platform_operator, is_active, verified)
		VALUES ($1::uuid, $2::uuid, 'owner@example.test', 'owner', ARRAY['read','write','admin'], false, true, true)
	`, ownerID, tenantID); err != nil {
		t.Fatal(err)
	}
	stream := PullStream{
		PlaybackID:  "contract-pull",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Contract pull", Description: "initial", SourceURI: "rtsp://example.test/live", Enabled: true,
		AllowedClusterIDs: []string{"edge-b", "edge-a", "edge-a"},
	}
	clusters := stubClusterResolver{caps: []pullsource.ClusterCapability{{ID: "edge-a"}, {ID: "edge-b"}}}
	result, err := ReconcilePullStreams(ctx, db, []PullStream{stream}, staticResolver(tenantID), clusters, fakeCipher{})
	if err != nil || len(result.Created) != 1 {
		t.Fatalf("create pull stream = %#v, err = %v", result, err)
	}
	var streamID, ingestMode, encryptedURI string
	var enabled bool
	var allowed []string
	if err := db.QueryRowContext(ctx, `
		SELECT s.id::text, s.ingest_mode, p.source_uri_enc, p.enabled, p.allowed_cluster_ids
		FROM commodore.streams s
		JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
		WHERE s.tenant_id = $1::uuid AND s.playback_id = $2::citext
	`, tenantID, stream.PlaybackID).Scan(&streamID, &ingestMode, &encryptedURI, &enabled, pq.Array(&allowed)); err != nil {
		t.Fatal(err)
	}
	if ingestMode != "pull" || encryptedURI != "enc:"+stream.SourceURI || !enabled || fmt.Sprint(allowed) != "[edge-a edge-b]" {
		t.Fatalf("stored pull stream = mode %q uri %q enabled %t allowed %#v", ingestMode, encryptedURI, enabled, allowed)
	}

	result, err = ReconcilePullStreams(ctx, db, []PullStream{stream}, staticResolver(tenantID), clusters, fakeCipher{})
	if err != nil || len(result.Noop) != 1 {
		t.Fatalf("replay pull stream = %#v, err = %v", result, err)
	}
	stream.Title = "Updated pull"
	stream.SourceURI = "rtsp://example.test/updated"
	stream.Enabled = false
	stream.AllowedClusterIDs = []string{"edge-b"}
	result, err = ReconcilePullStreams(ctx, db, []PullStream{stream}, staticResolver(tenantID), clusters, fakeCipher{})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("update pull stream = %#v, err = %v", result, err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT s.title, p.source_uri_enc, p.enabled, p.allowed_cluster_ids
		FROM commodore.streams s
		JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
		WHERE s.id = $1::uuid
	`, streamID).Scan(&stream.Title, &encryptedURI, &enabled, pq.Array(&allowed)); err != nil {
		t.Fatal(err)
	}
	if stream.Title != "Updated pull" || encryptedURI != "enc:rtsp://example.test/updated" || enabled || fmt.Sprint(allowed) != "[edge-b]" {
		t.Fatalf("updated pull stream = title %q uri %q enabled %t allowed %#v", stream.Title, encryptedURI, enabled, allowed)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM commodore.stream_pull_sources WHERE stream_id = $1::uuid`, streamID); err != nil {
		t.Fatal(err)
	}
	result, err = ReconcilePullStreams(ctx, db, []PullStream{stream}, staticResolver(tenantID), clusters, fakeCipher{})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("repair missing pull source = %#v, err = %v", result, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.stream_pull_sources WHERE stream_id = $1::uuid`, streamID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("repaired pull source count = %d, want 1", count)
	}
}

func TestBootstrapMistNativeRepository_RealPG(t *testing.T) {
	db := startCommodoreBootstrapRealPG(t)
	ctx := context.Background()
	tenantID := "10000000-0000-4000-8000-000000000001"
	ownerID := "20000000-0000-4000-8000-000000000002"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.users
			(id, tenant_id, email, role, permissions, platform_operator, is_active, verified)
		VALUES ($1::uuid, $2::uuid, 'owner@example.test', 'owner', ARRAY['read','write','admin'], false, true, true)
	`, ownerID, tenantID); err != nil {
		t.Fatal(err)
	}
	primary := MistNativeStream{
		PlaybackID: "mist-contract", OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title: "Contract loop", Description: "initial", Source: "ts-exec:cat /dev/null", SourceKind: "exec",
		AlwaysOn: true, IsRecordingEnabled: false, Monitoring: "inherit", PlacementCount: 0,
		AllowedClusterIDs: []string{"edge-a"},
		ProcessPolicy: []any{map[string]any{
			"process": "Thumbs", "track_select": "video=lowres", "x-LSP-name": "Thumbnail Sprites",
		}},
	}
	stale := primary
	stale.PlaybackID = "mist-stale"
	stale.Title = "Stale loop"
	stale.ProcessPolicy = nil

	result, err := ReconcileMistNativeStreams(ctx, db, []MistNativeStream{primary, stale}, staticResolver(tenantID))
	if err != nil || len(result.Created) != 2 {
		t.Fatalf("create mist streams = %#v, err = %v", result, err)
	}
	var streamID, sourceSpec, sourceKind, localAssets, processPolicy string
	var placement int
	var allowed []string
	if err := db.QueryRowContext(ctx, `
		SELECT s.id::text, mn.source_spec, mn.source_kind, mn.placement_count,
		       mn.allowed_cluster_ids, mn.local_asset_paths::text, spc.processes_live::text
		FROM commodore.streams s
		JOIN commodore.stream_mist_sources mn ON mn.stream_id = s.id
		JOIN commodore.stream_processing_config spc ON spc.stream_id = s.id
		WHERE s.tenant_id = $1::uuid AND s.playback_id = $2::citext
	`, tenantID, primary.PlaybackID).Scan(
		&streamID, &sourceSpec, &sourceKind, &placement, pq.Array(&allowed), &localAssets, &processPolicy,
	); err != nil {
		t.Fatal(err)
	}
	if sourceSpec != primary.Source || sourceKind != "exec" || placement != 1 || fmt.Sprint(allowed) != "[edge-a]" || localAssets != "[]" || processPolicy == "" {
		t.Fatalf("stored mist source = source %q kind %q placement %d allowed %#v assets %q policy %q", sourceSpec, sourceKind, placement, allowed, localAssets, processPolicy)
	}

	result, err = ReconcileMistNativeStreams(ctx, db, []MistNativeStream{primary}, staticResolver(tenantID))
	if err != nil || len(result.Noop) != 1 || len(result.Deleted) != 1 || result.Deleted[0] != stale.PlaybackID {
		t.Fatalf("replay and prune mist streams = %#v, err = %v", result, err)
	}
	primary.Title = "Updated loop"
	primary.Source = "ts-exec:printf updated"
	primary.AlwaysOn = false
	primary.IsRecordingEnabled = true
	primary.Monitoring = "on"
	primary.PlacementCount = 2
	primary.ProcessPolicy = nil
	result, err = ReconcileMistNativeStreams(ctx, db, []MistNativeStream{primary}, staticResolver(tenantID))
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("update mist stream = %#v, err = %v", result, err)
	}
	var title string
	var alwaysOn, recording bool
	var monitoring sql.NullBool
	if err := db.QueryRowContext(ctx, `
		SELECT s.title, s.always_on, s.is_recording_enabled, s.monitoring_enabled,
		       mn.source_spec, mn.placement_count
		FROM commodore.streams s
		JOIN commodore.stream_mist_sources mn ON mn.stream_id = s.id
		WHERE s.id = $1::uuid
	`, streamID).Scan(&title, &alwaysOn, &recording, &monitoring, &sourceSpec, &placement); err != nil {
		t.Fatal(err)
	}
	if title != primary.Title || alwaysOn || !recording || !monitoring.Valid || !monitoring.Bool || sourceSpec != primary.Source || placement != 2 {
		t.Fatalf("updated mist stream = title %q always_on %t recording %t monitoring %#v source %q placement %d", title, alwaysOn, recording, monitoring, sourceSpec, placement)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.stream_processing_config WHERE stream_id = $1::uuid`, streamID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cleared process override count = %d, want 0", count)
	}

	result, err = PruneAllMistNativeStreams(ctx, db, staticResolver(tenantID), []string{"frameworks"})
	if err != nil || len(result.Deleted) != 1 || result.Deleted[0] != primary.PlaybackID {
		t.Fatalf("prune final mist stream = %#v, err = %v", result, err)
	}
}

func startCommodoreBootstrapRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-commodore-bootstrap-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
