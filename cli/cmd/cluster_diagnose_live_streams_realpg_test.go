//go:build schema_verify

package cmd

import (
	"database/sql"
	"fmt"
	"os/exec"
	"slices"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

// TestMediaAuthorityLiveStreamsSectionListsOnlyLiveStreamsRealPG runs the
// media-authority "live streams" section against the current Commodore
// baseline. Only a stream whose ingest claim is inside its lease, or one with
// an unfinished refresh, is listed; idle, lapsed and deleted streams are not.
func TestMediaAuthorityLiveStreamsSectionListsOnlyLiveStreamsRealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-cli-live-streams-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := dockerpg.WaitReady(admin, name); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE commodore`); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/commodore?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	const tenant = "00000000-0000-0000-0000-0000000000aa"
	streams := map[string]string{
		"live":    "00000000-0000-0000-0000-000000000001",
		"idle":    "00000000-0000-0000-0000-000000000002",
		"lapsed":  "00000000-0000-0000-0000-000000000003",
		"deleted": "00000000-0000-0000-0000-000000000004",
		"stuck":   "00000000-0000-0000-0000-000000000005",
	}
	for label, id := range streams {
		if _, err := db.Exec(`INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
			VALUES ($1, $2, $2, $3::text, $3::text, $3::text, $3::text)`, id, tenant, "s-"+label); err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE commodore.streams SET active_ingest_cluster_id = 'media-eu-1', active_ingest_cluster_updated_at = NOW() WHERE id = $1`, streams["live"])
	mustExec(`UPDATE commodore.streams SET active_ingest_cluster_id = 'media-eu-1', active_ingest_cluster_updated_at = NOW() - INTERVAL '10 minutes' WHERE id = $1`, streams["lapsed"])
	mustExec(`UPDATE commodore.streams SET deleted_at = NOW() WHERE id = $1`, streams["deleted"])
	mustExec(`INSERT INTO commodore.media_authority_refresh_inbox (source_service, source_event_id, tenant_id, reason, last_error)
		VALUES ('commodore', 'reconcile:1', $1, $2, 'no ingest owner')`, tenant, "media_object:live_stream:"+streams["stuck"]+":ingest")

	rows, err := db.Query(commodoreLiveStreamsDiagnosticSQL)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprint(values[0])
		if b, ok := values[0].([]byte); ok {
			id = string(b)
		}
		for label, sid := range streams {
			if sid == id {
				listed = append(listed, label)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, []string{"live", "stuck"}) {
		t.Fatalf("live streams section listed %v, want [live stuck]", listed)
	}
}
