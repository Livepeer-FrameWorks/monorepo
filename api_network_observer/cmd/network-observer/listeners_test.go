package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// With the internal TLS listener carrying the API, the plain listener answers
// only the probes, so no bearer secret is accepted over plaintext.
func TestPlainListenerServesOnlyProbesBesideInternalTLS(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	plain := probesOnly(api)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/health", http.StatusTeapot},
		{http.MethodGet, "/ready", http.StatusTeapot},
		{http.MethodGet, "/v1/inventory", http.StatusNotFound},
		{http.MethodGet, "/v1/work", http.StatusNotFound},
		{http.MethodPost, "/v1/observations", http.StatusNotFound},
		{http.MethodGet, "/v1/candidates", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		plain.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.want {
			t.Errorf("%s %s on the plain listener = %d, want %d", tc.method, tc.path, response.Code, tc.want)
		}
	}
}

type pagedNodes struct {
	pages [][]*quartermasterpb.InfrastructureNode
	calls []string
}

func (p *pagedNodes) ListNodes(_ context.Context, _, _, _ string, page *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
	after := page.GetAfter()
	p.calls = append(p.calls, after)
	index := 0
	if after != "" {
		index = int(after[0] - '0')
	}
	resp := &quartermasterpb.ListNodesResponse{Nodes: p.pages[index], Pagination: &commonpb.CursorPaginationResponse{}}
	if index+1 < len(p.pages) {
		resp.Pagination.HasNextPage = true
		next := string(rune('0' + index + 1))
		resp.Pagination.EndCursor = &next
	}
	return resp, nil
}

// Every node page is read, so a gateway on a later page still gets a
// position, and a node without coordinates is reported as unknown.
func TestListNodeLocationsFollowsPagination(t *testing.T) {
	lat, lon := 52.0, 4.0
	lister := &pagedNodes{pages: [][]*quartermasterpb.InfrastructureNode{
		{{NodeId: "core-1", ClusterId: "core"}},
		{{NodeId: "regional-eu-1", ClusterId: "regional-eu", Latitude: &lat, Longitude: &lon}},
	}}
	got, err := listNodeLocations(context.Background(), lister)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Known || !got[1].Known || got[1].NodeID != "regional-eu-1" || got[1].Latitude != 52 {
		t.Fatalf("node locations = %+v", got)
	}
	if !slices.Equal(lister.calls, []string{"", "1"}) {
		t.Fatalf("pages requested after %q, want the first page then cursor 1", lister.calls)
	}
}
