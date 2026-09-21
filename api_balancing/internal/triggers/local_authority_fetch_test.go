package triggers

import (
	"context"
	"database/sql"
	"regexp"
	"sync"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

const localAuthorityObjectQuery = "SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,"

func TestRuntimePlaybackUsesLocalInternalNameWithoutCoreFetch(t *testing.T) {
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	asked := recordFetches(p)
	expectLocalObject(mock, objectBytes, time.Now().Add(time.Hour), true)
	expectLocalTenant(mock, tenantBytes, time.Now().Add(time.Hour), true)
	content, found, err := p.ResolveLocalContent(context.Background(), "live+stream-internal")
	if err != nil || !found || content == nil || content.InternalName != "stream-internal" {
		t.Fatalf("runtime source not resolved locally: %+v, %v, %v", content, found, err)
	}
	if got := asked(); len(got) != 0 {
		t.Fatalf("runtime source was incorrectly fetched as a playback ID: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// recordFetches installs a fetcher that answers "nothing for this cell" and
// records what it was asked for.
func recordFetches(p *Processor) func() []localauthority.AuthorityLookup {
	var mu sync.Mutex
	var asked []localauthority.AuthorityLookup
	p.mediaAuthorityStore.SetAuthorityFetcher(func(_ context.Context, lookup localauthority.AuthorityLookup) ([][]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, lookup)
		return nil, nil
	})
	return func() []localauthority.AuthorityLookup {
		mu.Lock()
		defer mu.Unlock()
		return append([]localauthority.AuthorityLookup(nil), asked...)
	}
}

// A cell holds the authorities in use. One it holds past its validity is asked
// for before it is refused, and when asking brings nothing the refusal is
// exactly what it was: an expired authority is never decided on.
func TestHardExpiredPlaybackAsksForTheAuthorityBeforeRefusing(t *testing.T) {
	p, mock, closeDB, _, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	asked := recordFetches(p)
	expectLocalObject(mock, objectBytes, time.Now().Add(-time.Minute), true)
	mock.ExpectQuery("BeginMediaAuthorityConfirmation").WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(time.Now()))

	_, found, err := p.resolveReadyLocalPlayback(context.Background(), "playbackkey", true)
	if !found || !IsLocalAuthorityExpired(err) {
		t.Fatalf("found=%v err=%v, want the expired refusal to stand when the fetch brought nothing", found, err)
	}
	if got := asked(); len(got) != 1 || got[0] != (localauthority.AuthorityLookup{PlaybackID: "playbackkey"}) {
		t.Fatalf("asked for %+v, want the playback id once", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A tombstone or a denial is an answer. Asking the control plane around it would
// turn a refusal into a second opinion.
func TestDeniedPlaybackIsNeverFetchedAround(t *testing.T) {
	p, mock, closeDB, _, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	asked := recordFetches(p)
	object := &mediaauthoritypb.MediaObjectAuthority{}
	if err := proto.Unmarshal(objectBytes, object); err != nil {
		t.Fatal(err)
	}
	object.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	tombstone, err := proto.MarshalOptions{Deterministic: true}.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	// Past its validity as well, which every tombstone eventually is: it is never
	// renewed, and it stays a refusal, not something to ask about again.
	for _, ready := range []bool{true, false} {
		expectLocalObject(mock, tombstone, time.Now().Add(-time.Hour), ready)
		_, found, err := p.resolveReadyLocalPlayback(context.Background(), "playbackkey", true)
		if !found || !IsLocalAuthorityDenied(err) {
			t.Fatalf("ready=%v found=%v err=%v, want denied", ready, found, err)
		}
	}
	if got := asked(); len(got) != 0 {
		t.Fatalf("a tombstoned object was asked for: %+v", got)
	}
}

// An object this cell holds nothing for may simply not have been used for a
// while. It is asked for by the name the request used, and when nothing comes
// back the read is still "absent", which is what sends the caller to connected
// validation.
func TestAbsentPlaybackIsAskedForByTheNameTheRequestUsed(t *testing.T) {
	p, mock, closeDB, _, _ := localAuthorityFixture(t)
	defer closeDB()
	asked := recordFetches(p)
	mock.ExpectQuery(regexp.QuoteMeta(localAuthorityObjectQuery)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("BeginMediaAuthorityConfirmation").WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(time.Now()))

	_, found, err := p.resolveReadyLocalPlayback(context.Background(), "stream-internal", false)
	if found || err != nil {
		t.Fatalf("found=%v err=%v, want absent", found, err)
	}
	if got := asked(); len(got) != 1 || got[0] != (localauthority.AuthorityLookup{InternalName: "stream-internal"}) {
		t.Fatalf("asked for %+v, want the internal name once", got)
	}
}

// A push on a key whose authority is past its validity asks for that stream's
// authority, and is refused when that brings nothing. It never falls back to the
// expired authority, with or without a control plane.
func TestPushRewriteHardExpiredAsksThenRefuses(t *testing.T) {
	p, mock, closeDB, _, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	asked := recordFetches(p)
	authorityID := sharedauthority.LiveStreamAuthorityID("30000000-0000-0000-0000-000000000001")
	mock.ExpectQuery(regexp.QuoteMeta(localAuthorityObjectQuery)).
		WithArgs(sharedauthority.PublishingCredentialDigest("sk_local")).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_ingest_ready", "withheld_by_tenant_revival", "tenant_authority_version"}).
			AddRow(objectBytes, localPayloadDigest(objectBytes), time.Now().Add(-2*time.Minute), time.Now().Add(-time.Minute), authorityID, int64(4), true, false, int64(8)))
	mock.ExpectQuery("BeginMediaAuthorityConfirmation").WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(time.Now()))

	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-1", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
		PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "expired", TriggerUnixMillis: 1, StreamName: "sk_local"},
	}}
	streamName, blocking, err := p.handlePushRewrite(trigger)
	if err == nil || !blocking || streamName != "" {
		t.Fatalf("hard-expired PUSH_REWRITE = stream=%q blocking=%v err=%v", streamName, blocking, err)
	}
	// The credential never leaves the cell: the stream is asked for by its
	// authority id.
	if got := asked(); len(got) != 1 || got[0] != (localauthority.AuthorityLookup{AuthorityID: authorityID}) {
		t.Fatalf("asked for %+v, want the stream's authority id once", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
