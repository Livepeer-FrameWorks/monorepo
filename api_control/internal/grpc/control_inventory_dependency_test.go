package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTenantControlInventoryDoesNotRequireAdmissionClients(t *testing.T) {
	addr := startTenantControlTestServer(t, &testTenantControlServer{invalidateResp: &foghorncontrolpb.InvalidateTenantCacheResponse{EntriesInvalidated: 5}})
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	server.routeCache = map[string]*clusterRoute{}
	server.foghornDiscovery.Store("media-us-1", &foghornDiscoveryEntry{at: time.Now(), addrs: []string{addr}})
	server.controlRecipientLookup = func(_ context.Context, request *quartermasterpb.GetTenantControlRecipientsRequest) (*quartermasterpb.GetTenantControlRecipientsResponse, error) {
		if request.GetTenantId() != "tenant-1" {
			t.Error("inventory lost tenant binding")
		}
		return &quartermasterpb.GetTenantControlRecipientsResponse{Complete: true, Recipients: []*quartermasterpb.TenantControlRecipient{{CellId: "media-us-1", SourceClusterIds: []string{"private-one", "private-two"}}}}, nil
	}
	response, err := server.InvalidateTenantCache(serviceCtx(), &foghorncontrolpb.InvalidateTenantCacheRequest{TenantId: "tenant-1"})
	if err != nil || response.GetEntriesInvalidated() != 5 {
		t.Fatalf("background control still depends on admission clients: response=%v err=%v", response, err)
	}
}

func TestTenantControlBoundedFanoutReservesTimeForLaterHealthyCell(t *testing.T) {
	hanging := startTenantControlTestServer(t, &testTenantControlServer{terminateWait: true})
	healthy := startTenantControlTestServer(t, &testTenantControlServer{terminateResp: &foghorncontrolpb.TerminateTenantStreamsResponse{StreamsTerminated: 2}})
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	var targets []clusterFanoutTarget
	for i := range 8 {
		targets = append(targets, clusterFanoutTarget{clusterID: fmt.Sprintf("hanging-%d", i), addr: hanging})
	}
	targets = append(targets, clusterFanoutTarget{clusterID: "later-healthy", addr: healthy})
	ctx, cancel := context.WithTimeout(serviceCtx(), time.Second)
	defer cancel()
	results := dispatchTenantControl(ctx, server, targets, func(ctx context.Context, client *foghornclient.GRPCClient) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
		response, _, err := client.TerminateTenantStreams(ctx, "tenant-1", "test")
		return response, err
	})
	last := results[len(results)-1]
	if last.err != nil || last.response.GetStreamsTerminated() != 2 {
		t.Fatalf("early failures starved a later healthy cell: response=%v err=%v", last.response, last.err)
	}
	for _, result := range results[:8] {
		if status.Code(result.err) != codes.DeadlineExceeded && !errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("hanging delivery unexpectedly succeeded: %v", result.err)
		}
	}
}

func TestTenantControlInventoryCanonicalizesRemovedPrivateHistory(t *testing.T) {
	server := newTenantFanoutTestServer(t, &clusterRoute{})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server.db = db
	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant-1", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("removed-private").AddRow("historical-central"))
	server.controlRecipientLookup = func(_ context.Context, req *quartermasterpb.GetTenantControlRecipientsRequest) (*quartermasterpb.GetTenantControlRecipientsResponse, error) {
		response := &quartermasterpb.GetTenantControlRecipientsResponse{Complete: true}
		for _, id := range req.GetHistoricalClusterIds() {
			if id == "removed-private" {
				response.Recipients = append(response.Recipients, &quartermasterpb.TenantControlRecipient{CellId: "media-us", SourceClusterIds: []string{id}})
			}
		}
		return response, nil
	}
	targets, err := server.tenantControlFanoutTargets(t.Context(), "tenant-1")
	if err != nil || len(targets) != 1 || targets[0].clusterID != "media-us" {
		t.Fatalf("removed private history did not use owner canonical inventory: targets=%v err=%v", targets, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
