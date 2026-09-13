package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A DTSC connection at the origin is admitted as a prepared source only for the
// pull this origin accepted against the current generation on the owning node;
// a different node, a superseded generation or a stale record is a viewer.
func TestAcceptedOutboundPullBindsNodeGenerationAndWindow(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "source-pull-test-secret")
	const stream = "60546679b497415db2338cd5cae54992"
	r := newPopulatedRegistry(t)
	projectSourceForTest(t, r, stream, "edge-1", 100, "trigger-1", "gen-1", 1)
	pull := OutboundPull{TenantID: "tenant-1", DestClusterID: "demo-selfhosted", DestNodeID: "edge-b", SourceNodeID: "edge-1",
		SourceGeneration: "gen-1", SourceRevision: 1, DTSCURL: "dtsc://edge-1:4200/live+" + stream}
	pull, err := r.RecordOutboundPull(context.Background(), stream, pull)
	if err != nil {
		t.Fatalf("record outbound pull: %v", err)
	}
	sourceURL, err := SourcePullURL(pull.DTSCURL, stream, pull)
	if err != nil {
		t.Fatal(err)
	}
	credential := SourcePullCredential(sourceURL)
	// The record is stamped when accepted; the connection arrives afterwards.
	now := time.Now().Add(time.Millisecond)

	if got, ok := r.AcceptedOutboundPull(context.Background(), stream, "edge-1", credential, now); !ok || got.DestNodeID != "edge-b" {
		t.Fatalf("accepted pull for the owning node = %+v ok=%v", got, ok)
	}
	if _, ok := r.AcceptedOutboundPull(context.Background(), "live+"+stream, "edge-1", credential, now); !ok {
		t.Fatal("runtime-prefixed name must resolve the same accepted pull")
	}
	if _, ok := r.AcceptedOutboundPull(context.Background(), stream, "edge-2", credential, now); ok {
		t.Fatal("a node that does not own the source cannot admit the pull")
	}
	if _, ok := r.AcceptedOutboundPull(context.Background(), stream, "edge-1", credential, now.Add(acceptedPullAdmissionWindow+time.Second)); ok {
		t.Fatal("a pull past its renewal window must not admit a connection")
	}
	for _, invalid := range []string{"", "not-a-source-credential", credential + "x"} {
		if _, ok := r.AcceptedOutboundPull(context.Background(), stream, "edge-1", invalid, now); ok {
			t.Fatal("an unrelated DTSC connection inherited another destination's acceptance")
		}
	}
	other := pull
	other.DestNodeID = "another-destination"
	otherURL, err := SourcePullURL(other.DTSCURL, stream, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.AcceptedOutboundPull(context.Background(), stream, "edge-1", SourcePullCredential(otherURL), now); ok {
		t.Fatal("credential for another destination admitted against this attempt")
	}

	// Publisher replacement supersedes the generation the pull was accepted for.
	projectSourceForTest(t, r, stream, "edge-1", 101, "trigger-2", "gen-2", 2)
	if _, ok := r.AcceptedOutboundPull(context.Background(), stream, "edge-1", credential, now); ok {
		t.Fatal("a pull accepted for a superseded generation must not admit a connection")
	}
}

func TestAcceptedOutboundDVRPullRequiresCurrentRecordingOwner(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "source-pull-test-secret")
	localDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	db = localDB
	t.Cleanup(func() {
		db = previousDB
		if expectationErr := mock.ExpectationsWereMet(); expectationErr != nil {
			t.Error(expectationErr)
		}
		localDB.Close()
	})
	const runtime = "dvr+recording"
	r := newPopulatedRegistry(t)
	pull, err := r.RecordOutboundPull(t.Context(), runtime, OutboundPull{
		TenantID: "tenant-1", SourceNodeID: "edge-1", DestNodeID: "edge-b", DestClusterID: "remote",
		DTSCURL: "dtsc://edge-1:4200/" + runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceURL, err := SourcePullURL(pull.DTSCURL, runtime, pull)
	if err != nil {
		t.Fatal(err)
	}
	credential := SourcePullCredential(sourceURL)
	now := time.Now()
	for _, tc := range []struct {
		name, stream, node, credential string
		when                           time.Time
	}{
		{"bare artifact", "recording", "edge-1", credential, now},
		{"live alias", "live+recording", "edge-1", credential, now},
		{"wrong node", runtime, "edge-2", credential, now},
		{"missing credential", runtime, "edge-1", "", now},
		{"tampered credential", runtime, "edge-1", credential + "x", now},
		{"expired", runtime, "edge-1", credential, now.Add(acceptedPullAdmissionWindow + time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := r.AcceptedOutboundPull(t.Context(), tc.stream, tc.node, tc.credential, tc.when); ok {
				t.Fatal("invalid DVR source connection accepted")
			}
		})
	}
	for _, tc := range []struct {
		name      string
		recording bool
		failure   error
	}{
		{"recording owner", true, nil},
		{"stopped or moved recording", false, nil},
		{"database unavailable", false, errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := mock.ExpectQuery(`SELECT EXISTS`).WithArgs("tenant-1", "recording", "edge-1")
			if tc.failure != nil {
				query.WillReturnError(tc.failure)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.recording))
			}
			_, ok := r.AcceptedOutboundPull(t.Context(), runtime, "edge-1", credential, now)
			if ok != tc.recording {
				t.Fatalf("accepted=%v want=%v", ok, tc.recording)
			}
		})
	}
}
