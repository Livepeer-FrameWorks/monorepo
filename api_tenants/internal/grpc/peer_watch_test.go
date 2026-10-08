package grpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
)

// recordingPeerStream captures what a subscriber would receive.
type recordingPeerStream struct {
	grpc.ServerStream
	ctx  context.Context
	mu   sync.Mutex
	sent []*quartermasterpb.ListPeersResponse
}

func (s *recordingPeerStream) Send(resp *quartermasterpb.ListPeersResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, resp)
	return nil
}

func (s *recordingPeerStream) Context() context.Context { return s.ctx }

func (s *recordingPeerStream) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func TestPeerWatchHubCollapsesBurstsAndBoundsSubscribers(t *testing.T) {
	hub := newPeerWatchHub()
	id, wake, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	// Several changes before the subscriber reads collapse into one re-read:
	// the subscriber re-derives its own peer set, so extra wakes buy nothing.
	hub.wake()
	hub.wake()
	hub.wake()
	if len(wake) != 1 {
		t.Fatalf("wake queue held %d entries, want a single pending re-read", len(wake))
	}
	<-wake
	select {
	case <-wake:
		t.Fatal("a collapsed burst delivered more than one wake")
	default:
	}

	hub.unsubscribe(id)
	if hub.watcherCount() != 0 {
		t.Fatalf("watcher count = %d after unsubscribe", hub.watcherCount())
	}
	// An unsubscribed waiter must never be woken again.
	hub.wake()
	select {
	case <-wake:
		t.Fatal("an unsubscribed waiter was woken")
	default:
	}
}

func TestPeerWatchHubRefusesUnboundedSubscribers(t *testing.T) {
	hub := newPeerWatchHub()
	for range maxPeerWatchers {
		if _, _, err := hub.subscribe(); err != nil {
			t.Fatalf("subscribe within capacity: %v", err)
		}
	}
	if _, _, err := hub.subscribe(); err == nil {
		t.Fatal("hub accepted a subscriber beyond its bound")
	}
}

// The watcher reads one value-level digest per interval and hands it through
// unchanged; which rewrites move it is proven against a real engine by
// TestUnchangedHealthReportsLeaveRowsAndPeerCensusAlone.
func TestPeerCensusFingerprintIsTheDigest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &QuartermasterServer{db: db, peerWatch: newPeerWatchHub()}
	mock.ExpectQuery("md5").WillReturnRows(sqlmock.NewRows([]string{"fingerprint"}).AddRow("digest-a"))
	got, err := server.peerCensusFingerprint(context.Background())
	if err != nil || got != "digest-a" {
		t.Fatalf("fingerprint = %q, %v; want digest-a", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWatchPeersSendsCurrentSetThenOnlyChanges(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := newPeerWatchHub()
	server := &QuartermasterServer{db: db, peerWatch: hub}

	peerRows := func(addr string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"cluster_id", "shared_tenant_ids", "cluster_name", "cluster_type", "foghorn_addr", "foghorn_addrs", "control_cell_id"}).
			AddRow("eu", []byte("{tenant}"), "EU", "central", addr, []byte("{"+addr+"}"), "eu-cell")
	}
	// Subscribe, an unchanged re-read, then a changed one.
	mock.ExpectQuery("peer_clusters").WillReturnRows(peerRows("eu-foghorn:18019"))
	mock.ExpectQuery("peer_clusters").WillReturnRows(peerRows("eu-foghorn:18019"))
	mock.ExpectQuery("peer_clusters").WillReturnRows(peerRows("eu-foghorn-2:18019"))

	ctx, cancel := context.WithCancel(serviceCallerContext())
	stream := &recordingPeerStream{ctx: ctx}
	done := make(chan error, 1)
	go func() { done <- server.WatchPeers(&quartermasterpb.ListPeersRequest{ClusterId: "us"}, stream) }()

	waitFor := func(want int, what string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for stream.count() < want {
			if time.Now().After(deadline) {
				t.Fatalf("%s: sent %d messages, want %d", what, stream.count(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(1, "subscribing did not deliver the current peer set")

	// An unchanged census must not produce traffic.
	hub.wake()
	time.Sleep(200 * time.Millisecond)
	if stream.count() != 1 {
		t.Fatalf("an unchanged peer set was resent: %d messages", stream.count())
	}

	hub.wake()
	waitFor(2, "a changed peer set was not delivered")
	if addr := stream.sent[1].GetPeers()[0].GetFoghornAddr(); addr != "eu-foghorn-2:18019" {
		t.Fatalf("second message carried %q", addr)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the subscriber did not end the stream")
	}
	if hub.watcherCount() != 0 {
		t.Fatal("ending a stream left its waiter registered")
	}
}

// serviceCallerContext presents the control-plane identity the interceptor
// installs, which is what a media cell's subscription arrives with.
func serviceCallerContext() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}
