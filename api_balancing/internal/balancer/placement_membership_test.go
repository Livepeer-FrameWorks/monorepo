package balancer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type memoryMembershipStore struct {
	mu     sync.Mutex
	rows   map[string]StoredPlacementMembership
	writes int
	fail   error
}

func (store *memoryMembershipStore) LoadPlacementMembership(_ context.Context, cellID string) (*StoredPlacementMembership, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	row, ok := store.rows[cellID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (store *memoryMembershipStore) StorePlacementMembership(_ context.Context, cellID string, membership StoredPlacementMembership) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fail != nil {
		return store.fail
	}
	if store.rows == nil {
		store.rows = map[string]StoredPlacementMembership{}
	}
	store.rows[cellID] = membership
	store.writes++
	return nil
}

type membershipStream struct {
	grpc.ClientStream
	ctx      context.Context
	messages chan *quartermasterpb.PlacementMembership
	end      error
}

func (stream *membershipStream) Recv() (*quartermasterpb.PlacementMembership, error) {
	select {
	case membership, ok := <-stream.messages:
		if !ok {
			return nil, stream.end
		}
		return membership, nil
	case <-stream.ctx.Done():
		return nil, stream.ctx.Err()
	}
}

type membershipSource struct {
	mu       sync.Mutex
	get      func() (*quartermasterpb.PlacementMembership, error)
	messages chan *quartermasterpb.PlacementMembership
	watchErr error
	gets     int
}

func (source *membershipSource) GetPlacementMembership(context.Context, string) (*quartermasterpb.PlacementMembership, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.gets++
	return source.get()
}

func (source *membershipSource) WatchPlacementMembership(ctx context.Context, _ string) (quartermasterpb.ClusterService_WatchPlacementMembershipClient, error) {
	// A stream that ends at once delivers the configured status on its first Recv,
	// as a gRPC server stream does.
	messages := source.messages
	if source.watchErr != nil {
		messages = closedMembershipChannel()
	}
	return &membershipStream{ctx: ctx, messages: messages, end: source.watchErr}, nil
}

func closedMembershipChannel() chan *quartermasterpb.PlacementMembership {
	ch := make(chan *quartermasterpb.PlacementMembership)
	close(ch)
	return ch
}

func testMembership(revision string, observed time.Time, admitted bool) *quartermasterpb.PlacementMembership {
	return &quartermasterpb.PlacementMembership{
		ControlCellId: "cell", Revision: revision, Complete: true, ObservedAt: timestamppb.New(observed),
		Clusters: []*quartermasterpb.PlacementMembershipCluster{{ClusterId: "a", IsActive: true}, {ClusterId: "b", IsActive: true}, {ClusterId: "retired"}},
		Nodes: []*quartermasterpb.MediaPlacementInventoryNode{
			{ClusterId: "a", NodeId: "a-1", AdmissionEnabled: true},
			{ClusterId: "a", NodeId: "a-2", AdmissionEnabled: admitted},
			{ClusterId: "b", NodeId: "b-1", AdmissionEnabled: true},
			{ClusterId: "retired", NodeId: "r-1", AdmissionEnabled: true},
		},
	}
}

func TestMembership_RestartWithQMDownLoadsDurable(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	clock := func() time.Time { return now }
	store := &memoryMembershipStore{}
	before := NewPlacementMembership(PlacementMembershipConfig{CellID: "cell", Store: store, Now: clock})
	if err := before.Apply(context.Background(), testMembership("rev-1", now.Add(-time.Second), true)); err != nil {
		t.Fatal(err)
	}

	now = now.Add(10 * time.Minute)
	down := &membershipSource{
		get: func() (*quartermasterpb.PlacementMembership, error) {
			return nil, status.Error(codes.Unavailable, "quartermaster down")
		},
		watchErr: status.Error(codes.Unavailable, "quartermaster down"),
	}
	restarted := NewPlacementMembership(PlacementMembershipConfig{CellID: "cell", Store: store, Now: clock, Source: func() PlacementMembershipSource { return down }})
	if _, err := restarted.Inventory("tenant", []string{"a"}, now); !errors.Is(err, ErrPlacementMembershipUnavailable) {
		t.Fatalf("membership answered before it was loaded: %v", err)
	}
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted.Reconcile(context.Background())
	inventory, err := restarted.Inventory("tenant", []string{"a", "b"}, now)
	if err != nil || !inventory.GetComplete() || len(inventory.GetNodes()) != 3 || !inventory.GetObservedAt().AsTime().Equal(now) || inventory.GetTenantId() != "tenant" || inventory.GetControlCellId() != "cell" {
		t.Fatalf("restart with Quartermaster down lost membership: %v, %v", inventory, err)
	}
	if age := restarted.AgeSeconds(); age != 600 {
		t.Fatalf("restored membership age = %v, want the stored confirmation's 600s", age)
	}
	if down.gets != 1 {
		t.Fatalf("reconcile reads = %d", down.gets)
	}
}

func TestMembership_InventoryRefusesClustersOutsideSnapshot(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	membership := NewPlacementMembership(PlacementMembershipConfig{CellID: "cell", Now: func() time.Time { return now }})
	if err := membership.Apply(context.Background(), testMembership("rev-1", now, false)); err != nil {
		t.Fatal(err)
	}
	for _, clusters := range [][]string{{"a", "moved"}, {"retired"}} {
		if inventory, err := membership.Inventory("tenant", clusters, now); inventory != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("clusters %v outside the snapshot = %v, %v", clusters, inventory, err)
		}
	}
	inventory, err := membership.Inventory("tenant", []string{"a"}, now)
	if err != nil || len(inventory.GetNodes()) != 2 || inventory.GetNodes()[1].GetAdmissionEnabled() {
		t.Fatalf("drained node lost its admission state: %v, %v", inventory, err)
	}
	inventory.Nodes[0].AdmissionEnabled = false
	if again, _ := membership.Inventory("tenant", []string{"a"}, now); !again.GetNodes()[0].GetAdmissionEnabled() {
		t.Fatal("a caller's inventory aliased the snapshot")
	}
}

func TestMembership_StoresOnlyChangedRevisions(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	store := &memoryMembershipStore{}
	membership := NewPlacementMembership(PlacementMembershipConfig{CellID: "cell", Store: store, Now: func() time.Time { return now }})
	apply := func(revision string, observed time.Time) error {
		return membership.Apply(context.Background(), testMembership(revision, observed, revision == "rev-1"))
	}
	if err := apply("rev-1", now); err != nil {
		t.Fatal(err)
	}
	if err := apply("rev-1", now.Add(time.Minute)); err != nil || store.writes != 1 {
		t.Fatalf("an unchanged revision was rewritten: writes=%d %v", store.writes, err)
	}
	store.fail = errors.New("database unavailable")
	if err := apply("rev-2", now.Add(2*time.Minute)); err == nil {
		t.Fatal("a failed write was not reported")
	}
	if inventory, _ := membership.Inventory("tenant", []string{"a"}, now); inventory.GetNodes()[1].GetAdmissionEnabled() {
		t.Fatal("a failed write withheld the new membership from placement")
	}
	store.fail = nil
	if err := apply("rev-2", now.Add(3*time.Minute)); err != nil || store.writes != 2 || store.rows["cell"].Revision != "rev-2" {
		t.Fatalf("a failed write was not retried on the next confirmation: writes=%d %v", store.writes, err)
	}
	// A read taken before the held revision cannot replace it.
	if err := apply("rev-1", now.Add(time.Minute)); err != nil || store.writes != 2 {
		t.Fatalf("an older read was applied: writes=%d %v", store.writes, err)
	}
	if inventory, _ := membership.Inventory("tenant", []string{"a"}, now); inventory.GetNodes()[1].GetAdmissionEnabled() {
		t.Fatal("an older read replaced the held membership")
	}
}

func TestMembership_RejectsForeignOrInconsistentMembership(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	membership := NewPlacementMembership(PlacementMembershipConfig{CellID: "cell"})
	foreign := testMembership("rev", now, true)
	foreign.ControlCellId = "other"
	incomplete := testMembership("rev", now, true)
	incomplete.Complete = false
	orphan := testMembership("rev", now, true)
	orphan.Nodes = append(orphan.Nodes, &quartermasterpb.MediaPlacementInventoryNode{ClusterId: "unlisted", NodeId: "x"})
	duplicate := testMembership("rev", now, true)
	duplicate.Nodes = append(duplicate.Nodes, &quartermasterpb.MediaPlacementInventoryNode{ClusterId: "b", NodeId: "a-1"})
	for name, value := range map[string]*quartermasterpb.PlacementMembership{"foreign": foreign, "incomplete": incomplete, "orphan": orphan, "duplicate": duplicate, "nil": nil} {
		if err := membership.Apply(context.Background(), value); err == nil {
			t.Fatalf("%s membership was applied", name)
		}
	}
	if _, err := membership.Inventory("tenant", []string{"a"}, now); !errors.Is(err, ErrPlacementMembershipUnavailable) {
		t.Fatalf("rejected membership answered: %v", err)
	}
}

func TestMembership_LegacyQuartermasterFallsBackPerRequest(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	current := testMembership("rev-1", now, true)
	var getErr error
	source := &membershipSource{get: func() (*quartermasterpb.PlacementMembership, error) { return current, getErr }}
	membership := NewPlacementMembership(PlacementMembershipConfig{CellID: "cell", Now: func() time.Time { return now }, Source: func() PlacementMembershipSource { return source }})
	membership.Reconcile(context.Background())
	if _, err := membership.Inventory("tenant", []string{"a"}, now); err != nil {
		t.Fatal(err)
	}
	getErr = status.Error(codes.Unimplemented, "unknown method")
	membership.Reconcile(context.Background())
	if _, err := membership.Inventory("tenant", []string{"a"}, now); !errors.Is(err, ErrPlacementMembershipUnavailable) {
		t.Fatalf("a Quartermaster without membership left the snapshot in use: %v", err)
	}
	if age := membership.AgeSeconds(); age != 0 {
		t.Fatalf("legacy age = %v", age)
	}
	getErr = nil
	membership.Reconcile(context.Background())
	if _, err := membership.Inventory("tenant", []string{"a"}, now); err != nil {
		t.Fatalf("an upgraded Quartermaster did not restore the snapshot: %v", err)
	}
}

func TestMembership_WatchDeliversChanges(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	source := &membershipSource{
		get:      func() (*quartermasterpb.PlacementMembership, error) { return testMembership("rev-1", now, true), nil },
		messages: make(chan *quartermasterpb.PlacementMembership, 1),
	}
	membership := NewPlacementMembership(PlacementMembershipConfig{CellID: "cell", Now: func() time.Time { return now }, Source: func() PlacementMembershipSource { return source }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go membership.Run(ctx)
	source.messages <- testMembership("rev-2", now.Add(time.Second), false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		inventory, err := membership.Inventory("tenant", []string{"a"}, now)
		if err == nil && !inventory.GetNodes()[1].GetAdmissionEnabled() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("watched drain never reached placement: %v, %v", inventory, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
