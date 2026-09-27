package jobs

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// Only the cell leader re-requests missing chapter sidecars; every replica
// doing it would send the same node the same regeneration each minute.
func TestChapterDTSHRetryRunsOnLeaderOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		isLeader func() bool
		want     int
	}{
		{"follower", func() bool { return false }, 0},
		{"leader", func() bool { return true }, 1},
		{"single replica", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer mockDB.Close()

			var triggered []string
			s := NewChapterReclaimSweep(ChapterReclaimSweepConfig{DB: mockDB, Logger: logging.NewLogger(), IsLeader: tc.isLeader})
			s.triggerDtshSync = func(nodeID, artifactHash, artifactType, filePath string) {
				triggered = append(triggered, nodeID+"/"+artifactHash+"/"+artifactType+"/"+filePath)
			}
			if tc.want > 0 {
				mock.ExpectQuery(`c.state = 'finalized'`).
					WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "node_id"}).AddRow("chapter-pb", "edge-1"))
			}

			s.retryFinalizedChapterDTSH(context.Background())

			if len(triggered) != tc.want {
				t.Fatalf("triggered %d sidecar requests, want %d: %v", len(triggered), tc.want, triggered)
			}
			if tc.want > 0 && triggered[0] != "edge-1/chapter-pb/vod/vod/chapter-pb.mkv" {
				t.Fatalf("unexpected sidecar request %q", triggered[0])
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
