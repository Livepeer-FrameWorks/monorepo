package grpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFetchDoesNotConfirmUsingAnOlderSharedCompile(t *testing.T) {
	s := &CommodoreServer{}
	started, release := make(chan struct{}), make(chan struct{})
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		_, _, _ = s.mediaAuthorityFetch.Do("stream", func() (any, error) {
			at := time.Now().Add(-time.Second)
			close(started)
			<-release
			return at, nil
		})
	}()
	<-started
	var compiled atomic.Int32
	result := make(chan error, 1)
	go func() {
		result <- s.compileFreshMediaAuthority(context.Background(), "stream", func() error {
			compiled.Add(1)
			return errMediaAuthorityCompileSuperseded
		})
	}()
	close(release)
	<-oldDone
	if err := <-result; !errors.Is(err, errMediaAuthorityCompileSuperseded) || compiled.Load() != 3 {
		t.Fatalf("fetch reused an earlier compile or swallowed its successor's failure: calls=%d err=%v", compiled.Load(), err)
	}
}

func TestFetchRetriesSupersededCompile(t *testing.T) {
	s := &CommodoreServer{}
	calls := 0
	err := s.compileFreshMediaAuthority(context.Background(), "stream", func() error {
		calls++
		if calls == 1 {
			return errMediaAuthorityCompileSuperseded
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("compile calls=%d error=%v", calls, err)
	}
}

func TestFetchCompileDeadlineBoundsWaitingForSharedWork(t *testing.T) {
	s := &CommodoreServer{}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_, _, _ = s.mediaAuthorityFetch.Do("stream", func() (any, error) {
			at := time.Now().Add(-time.Second)
			close(started)
			<-release
			return at, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	err := s.compileFreshMediaAuthority(ctx, "stream", func() error { calls.Add(1); return nil })
	close(release)
	<-finished
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatalf("shared compile wait ignored deadline: calls=%d error=%v", calls.Load(), err)
	}
}

// A cell reports what it decided on in batches. One bad entry must not lose the
// rest of the batch, an object's use is also its tenant's, and nobody but a
// service may say what is in use: use is what keeps an authority in cells.
func TestReportMediaAuthorityUseRecordsObjectAndTenant(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	request := &commodorepb.ReportMediaAuthorityUseRequest{ControlCellId: "cell-a", Uses: []*commodorepb.MediaAuthorityUse{
		{AuthorityKind: "media_object", AuthorityId: "live_stream:stream-1", TenantId: tenantID},
		{AuthorityKind: "media_object", AuthorityId: "live_stream:stream-2", TenantId: "not-a-uuid"},
		{AuthorityKind: "session", AuthorityId: "x", TenantId: tenantID},
		{AuthorityKind: "tenant", AuthorityId: tenantID, TenantId: tenantID},
	}}
	if _, err := s.ReportMediaAuthorityUse(context.Background(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthenticated code = %s, want PermissionDenied", status.Code(err))
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	// Each use and its renewal revival commit together. The renewal is revived
	// even when the use row did not advance (already recorded today): that repeat
	// may be the one a dormant settlement raced. A report is not a compile, so no
	// lane is excluded from the revival.
	record := func(kind, id string, advanced int64) {
		mock.ExpectExec("RecordMediaAuthorityUse").WithArgs(kind, id, tenantID).WillReturnResult(sqlmock.NewResult(0, advanced))
		mock.ExpectExec("ReviveDormantMediaAuthorityRenewal").WithArgs(kind+":"+id, "").WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectBegin()
	record("media_object", "live_stream:stream-1", 1)
	record("tenant", tenantID, 0)
	mock.ExpectCommit()
	// The tenant's own entry.
	mock.ExpectBegin()
	record("tenant", tenantID, 0)
	mock.ExpectCommit()
	if _, err := s.ReportMediaAuthorityUse(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	tooMany := &commodorepb.ReportMediaAuthorityUseRequest{ControlCellId: "cell-a", Uses: make([]*commodorepb.MediaAuthorityUse, mediaAuthorityUseBatchMax+1)}
	if _, err := s.ReportMediaAuthorityUse(ctx, tooMany); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized report code = %s, want InvalidArgument", status.Code(err))
	}
}

// A fetch compiles and signs on demand, so only a cell may ask, and only for
// something that names an authority.
func TestFetchMediaAuthorityRefusesWhatItCannotCompile(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	byPlaybackID := &commodorepb.FetchMediaAuthorityRequest{ControlCellId: "cell-a", Lookup: &commodorepb.FetchMediaAuthorityRequest_PlaybackId{PlaybackId: "playback-1"}}
	if _, err := s.FetchMediaAuthority(context.Background(), byPlaybackID); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthenticated code = %s, want PermissionDenied", status.Code(err))
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	// A compiler without a signing key cannot answer; the cell carries on with
	// what it holds.
	if _, err := s.FetchMediaAuthority(ctx, byPlaybackID); status.Code(err) != codes.Unavailable {
		t.Fatalf("unconfigured compiler code = %s, want Unavailable", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
