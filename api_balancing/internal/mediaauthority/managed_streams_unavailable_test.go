package mediaauthority

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/protobuf/proto"
)

const unavailableAfterRefetch = "Managed source authority still unavailable after a successful refetch; preserving other tenants and disabling retraction"

// A managed-stream row that stays undecodable after the control plane answered
// its refetch cannot be repaired by asking again, so it is logged at Warn with
// its authority, once per interval; until a refetch succeeds it stays at Debug.
func TestManagedStreamRowUndecodableAfterRefetchWarnsOncePerInterval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fetchErr error
		wantWarn []int
	}{
		{name: "refetch answered", wantWarn: []int{0, 1, 1, 2}},
		{name: "refetch failed", fetchErr: errors.New("commodore unreachable"), wantWarn: []int{0, 0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			mock.MatchExpectationsInOrder(false)
			now := storeFixtureNow.Add(time.Minute)
			store.now = func() time.Time { return now }
			logger := logging.NewLogger()
			logger.SetLevel(logrus.DebugLevel)
			logs := logrustest.NewLocal(logger)
			store.SetLogger(logger)
			store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
				return nil, tc.fetchErr
			})

			object := &mediapb.MediaObjectAuthority{TenantId: "tenant", InternalName: "internal",
				Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}}}
			payload, err := proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(append(payload, 0))
			until := now.Add(time.Hour)
			expectList := func() {
				mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{
					"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
					"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
				}).AddRow("stream", 2, payload, digest[:], until, until, true, false, 1))
			}
			waitForFetch := func() {
				t.Helper()
				deadline := time.Now().Add(2 * time.Second)
				for {
					store.fetcher.mu.Lock()
					active := store.fetcher.asyncActive
					store.fetcher.mu.Unlock()
					if active == 0 {
						return
					}
					if time.Now().After(deadline) {
						t.Fatal("refetch did not finish")
					}
					time.Sleep(time.Millisecond)
				}
			}
			warnings := func() int {
				count := 0
				for _, entry := range logs.AllEntries() {
					if entry.Level == logrus.WarnLevel && entry.Message == unavailableAfterRefetch {
						if entry.Data["authority_id"] != "stream" {
							t.Fatalf("warning names %v, want the authority", entry.Data)
						}
						count++
					}
				}
				return count
			}
			read := func(step int) {
				t.Helper()
				set, readErr := store.ManagedStreams(context.Background(), "cell-a")
				if readErr != nil || set.Complete || len(set.Rows) != 0 {
					t.Fatalf("read %d: undecodable row must leave the set incomplete without rows: %+v, %v", step, set, readErr)
				}
				waitForFetch()
				if got := warnings(); got != tc.wantWarn[step] {
					t.Fatalf("read %d: %d warnings, want %d", step, got, tc.wantWarn[step])
				}
			}

			// The first read dispatches the refetch.
			expectList()
			mock.ExpectQuery("BeginMediaAuthorityConfirmation").WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(now))
			read(0)
			// After it, the still-undecodable row warns once.
			expectList()
			read(1)
			expectList()
			read(2)
			// Past the interval, and past the refetch cooldown, it warns again.
			now = now.Add(unavailableRowWarnInterval + time.Second)
			expectList()
			mock.ExpectQuery("BeginMediaAuthorityConfirmation").WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(now))
			read(3)
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
