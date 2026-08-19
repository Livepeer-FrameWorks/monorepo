//go:build schema_verify

package provisioner

import (
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// TestArtifactPlaybackIndexRepairFromPartialYugabyteAttempt proves the v0.2.96 contract migration
// reconciles the live partial-failure shape: the clips index is absent while the other three released
// functional indexes remain. The migration must restore the missing index without replacing the
// Yugabyte-compatible definitions.
func TestArtifactPlaybackIndexRepairFromPartialYugabyteAttempt(t *testing.T) {
	requireDocker(t)

	const name = "fw-pb-idx-upgrade"
	pgStart(t, name)
	defer rmContainer(t, name)
	pgCreateDB(t, name, "up")

	// The released schema: minimal tables carrying the four functional lower() unique indexes.
	pgApply(t, name, "up", `
CREATE EXTENSION IF NOT EXISTS citext;
CREATE SCHEMA commodore;
CREATE TABLE commodore.clips (playback_id CITEXT);
CREATE TABLE commodore.dvr_recordings (playback_id CITEXT);
CREATE UNIQUE INDEX idx_commodore_dvr_playback_ci ON commodore.dvr_recordings((lower(playback_id::text)));
CREATE TABLE commodore.vod_assets (playback_id CITEXT);
CREATE UNIQUE INDEX idx_commodore_vod_playback_ci ON commodore.vod_assets((lower(playback_id::text)));
CREATE TABLE commodore.dvr_chapter_playback (playback_id CITEXT);
CREATE UNIQUE INDEX idx_commodore_dvr_chapter_playback_pid_ci ON commodore.dvr_chapter_playback((lower(playback_id::text)));
`)

	mig, err := dbsql.Content.ReadFile("migrations/commodore/v0.2.96/contract/001_dvr_chapter_playback_index_realign.sql")
	if err != nil {
		t.Fatalf("read contract migration: %v", err)
	}
	pgApply(t, name, "up", string(mig))

	schema := pgIntrospect(t, name, "up")
	wantFunctional := []string{
		"idx_commodore_clips_playback_ci",
		"idx_commodore_dvr_playback_ci",
		"idx_commodore_vod_playback_ci",
		"idx_commodore_dvr_chapter_playback_pid_ci",
	}
	for _, idx := range wantFunctional {
		var def string
		for _, line := range strings.Split(schema, "\n") {
			if strings.Contains(line, "|"+idx+"|") {
				def = line
				break
			}
		}
		if def == "" {
			t.Errorf("index %s missing after upgrade", idx)
			continue
		}
		if !strings.Contains(strings.ToLower(def), "lower((playback_id)::text)") {
			t.Errorf("index %s is not functional lower(playback_id::text): %s", idx, def)
		}
	}
}
