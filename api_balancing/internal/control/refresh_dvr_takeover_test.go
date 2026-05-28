package control

import (
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// TestRefreshActiveDVRSourceOnTakeover_GuardClauses_NoDB covers the
// bootstrap-race case where AdmitAndReserve's goroutine fires the
// refresh before SetDatabase has installed the connection. Must
// return cleanly without panicking on the nil db.
func TestRefreshActiveDVRSourceOnTakeover_GuardClauses_NoDB(t *testing.T) {
	prev := db
	db = nil
	t.Cleanup(func() { db = prev })

	// Must not panic.
	RefreshActiveDVRSourceOnTakeover("any-internal", "node-1", logging.NewLogger())
}

// TestRefreshActiveDVRSourceOnTakeover_GuardClauses_EmptyArgs covers the
// argument guard — empty strings are unaddressable and must not even
// attempt a db lookup.
func TestRefreshActiveDVRSourceOnTakeover_GuardClauses_EmptyArgs(t *testing.T) {
	RefreshActiveDVRSourceOnTakeover("", "node-1", logging.NewLogger())
	RefreshActiveDVRSourceOnTakeover("internal", "", logging.NewLogger())
	RefreshActiveDVRSourceOnTakeover("", "", logging.NewLogger())
}

// TestRefreshActiveDVRSourceOnTakeover_DispatchHappyPath drives the full
// function end-to-end: artifact lookup returns a dvr_hash, storage-node
// resolution returns the recording node, StreamStateManager already
// shows the new owner with a DTSC output, registry resolves the stream
// as push-ingest. The dispatcher is captured to assert dvr_hash +
// source_runtime_name ("live+<x>") + a non-empty source_base_url were
// passed correctly.
func TestRefreshActiveDVRSourceOnTakeover_DispatchHappyPath(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	prevDB := db
	db = mockDB
	t.Cleanup(func() {
		db = prevDB
		mockDB.Close()
	})

	const (
		internalName = "stream-takeover-1"
		newOwner     = "edge-node-B"
		storageNode  = "storage-node-Z"
		dvrHash      = "dvr-abc123"
	)

	// 1) artifact lookup returns the active DVR hash for this stream.
	mock.ExpectQuery(`SELECT artifact_hash\s+FROM foghorn.artifacts`).
		WithArgs(internalName).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow(dvrHash))

	// 2) artifact_nodes lookup returns exactly one non-orphaned row.
	mock.ExpectQuery(`SELECT node_id, COALESCE\(is_orphaned, false\)\s+FROM foghorn.artifact_nodes`).
		WithArgs(dvrHash).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "is_orphaned"}).AddRow(storageNode, false))

	// State manager already shows new owner with a DTSC output template.
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo(newOwner, "https://edge-b.example", true, nil, nil, "",
		`{"DTSC":"dtsc://HOST/$"}`,
		map[string]any{"DTSC": "dtsc://HOST/$"})
	_ = sm.UpdateStreamFromBuffer("live+"+internalName, internalName, newOwner, "tenant", "FULL", "")

	// Registry resolves the source as push-ingest so RuntimeNameFor
	// produces "live+<internal>".
	prevRegistry := StreamRegistryInstance
	StreamRegistryInstance = NewStreamRegistry(nil, "cluster-A", time.Minute)
	StreamRegistryInstance.UpsertLocalSource(StreamEntry{
		InternalName: internalName,
		IngestMode:   IngestPush,
	})
	t.Cleanup(func() { StreamRegistryInstance = prevRegistry })

	// Capture the dispatch instead of going out over the real conn.
	var captured *pb.DVRUpdateSourceRequest
	var capturedNode string
	var mu sync.Mutex
	prevDispatch := sendDVRUpdateSourceFn
	sendDVRUpdateSourceFn = func(nodeID string, req *pb.DVRUpdateSourceRequest) error {
		mu.Lock()
		defer mu.Unlock()
		capturedNode = nodeID
		captured = req
		return nil
	}
	t.Cleanup(func() { sendDVRUpdateSourceFn = prevDispatch })

	RefreshActiveDVRSourceOnTakeover(internalName, newOwner, logging.NewLogger())

	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatal("dispatcher not called; refresh path didn't reach dispatch")
	}
	if capturedNode != storageNode {
		t.Errorf("dispatched to %q, want %q (the resolved storage node)", capturedNode, storageNode)
	}
	if captured.GetDvrHash() != dvrHash {
		t.Errorf("DvrHash = %q, want %q", captured.GetDvrHash(), dvrHash)
	}
	if got, want := captured.GetSourceRuntimeName(), "live+"+internalName; got != want {
		t.Errorf("SourceRuntimeName = %q, want %q (IngestPush → live+ prefix)", got, want)
	}
	if captured.GetSourceBaseUrl() == "" {
		t.Error("SourceBaseUrl is empty; BuildDTSCURI failed to resolve the new owner's DTSC URL")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
