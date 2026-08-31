//go:build schema_verify

package provisioner

import (
	"strings"
	"testing"
)

// TestArtifactPlaybackIndexUpgradeFromReleasedLower proves the released-lower playback indexes are
// repaired by the v0.2.96 contract migration without replacing their Yugabyte-compatible shape.
func TestArtifactPlaybackIndexUpgradeFromReleasedLower(t *testing.T) {
	requireDocker(t)
	// Keep this historical contract tied to the release that shipped the
	// repair. Shipped migrations are intentionally squashed out of the current
	// embedded tree; the immutable tag is their source of truth.
	const repairTag = "v0.2.96"
	fromTag := schemaVerifyFromTag(t)
	if compareSemver(fromTag, repairTag) < 0 {
		t.Fatalf("schema verification tag %s predates playback-index repair %s", fromTag, repairTag)
	}

	const name = "fw-pb-idx-upgrade"
	pgStart(t, name)
	defer rmContainer(t, name)
	pgCreateDB(t, name, "up")

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

	migration := repositoryFileAtTag(t, repairTag, "pkg/database/sql/migrations/commodore/v0.2.96/contract/001_dvr_chapter_playback_index_realign.sql")
	pgApply(t, name, "up", migration)

	schema := pgIntrospect(t, name, "up")
	wantFunctional := []string{
		"idx_commodore_clips_playback_ci",
		"idx_commodore_dvr_playback_ci",
		"idx_commodore_vod_playback_ci",
		"idx_commodore_dvr_chapter_playback_pid_ci",
	}
	for _, indexName := range wantFunctional {
		var definition string
		for _, line := range strings.Split(schema, "\n") {
			if strings.Contains(line, "|"+indexName+"|") {
				definition = line
				break
			}
		}
		if definition == "" {
			t.Errorf("index %s missing after upgrade", indexName)
			continue
		}
		if !strings.Contains(strings.ToLower(definition), "lower((playback_id)::text)") {
			t.Errorf("index %s is not functional lower(playback_id::text): %s", indexName, definition)
		}
	}
}
