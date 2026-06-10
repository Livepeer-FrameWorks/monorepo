package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"

	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// periA wires a resolver to a FakePeriscope only. Every other client stays nil
// and panics if a resolver unexpectedly reaches for it, which proves the
// Periscope-only path was the one exercised. (Distinct helper name from periR in
// analytics_reads_resolver_test.go to avoid a package-level redeclaration.)
func periA(p *clientstest.FakePeriscope) *Resolver {
	return &Resolver{Clients: clientstest.Clients(clientstest.WithPeriscope(p)), Logger: clientstest.DiscardLogger()}
}

// --- DoGetAPIUsageConnection ---

func TestDoGetAPIUsageConnection_ForwardsFiltersAndMapsSummaries(t *testing.T) {
	var gotAuth, gotOpType, gotOpName *string
	var gotSummaryOnly bool
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetAPIUsageFn: func(_ context.Context, _ string, authType, opType, opName *string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetAPIUsageResponse, error) {
			gotAuth, gotOpType, gotOpName, gotTR, gotSummaryOnly = authType, opType, opName, tr, summaryOnly
			return &periscopepb.GetAPIUsageResponse{
				Records:            []*periscopepb.APIUsageRecord{{AuthType: "jwt", OperationType: "query", OperationName: "GetStreams", RequestCount: 9}},
				Summaries:          []*periscopepb.APIUsageSummary{{}},
				OperationSummaries: []*periscopepb.APIUsageOperationSummary{{}, {}},
			}, nil
		},
	}
	auth, opType, opName := "jwt", "query", "GetStreams"
	tr := testRange()
	got, err := periA(p).DoGetAPIUsageConnection(clientstest.AuthedCtx("t1"), &auth, &opType, &opName, tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// summaryOnly is hard-wired false: this surface returns detailed records AND summaries.
	if gotSummaryOnly {
		t.Fatal("summaryOnly must be false for the detail+summary surface")
	}
	if gotAuth == nil || *gotAuth != "jwt" || gotOpType == nil || *gotOpType != "query" || gotOpName == nil || *gotOpName != "GetStreams" {
		t.Fatalf("filters not forwarded: %v %v %v", gotAuth, gotOpType, gotOpName)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.RequestCount != 9 {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
	if len(got.Summaries) != 1 || len(got.OperationSummaries) != 2 {
		t.Fatalf("summaries not copied: %d/%d", len(got.Summaries), len(got.OperationSummaries))
	}
}

func TestDoGetAPIUsageConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetAPIUsageConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetArtifactEventsConnection (via loadClipEvents → GetClipEvents) ---

func TestDoGetArtifactEventsConnection_NormalizesStreamAndForwardsFilters(t *testing.T) {
	var gotName, gotStage, gotContentType *string
	p := &clientstest.FakePeriscope{
		GetClipEventsFn: func(_ context.Context, _ string, streamID, stage, contentType *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetClipEventsResponse, error) {
			gotName, gotStage, gotContentType = streamID, stage, contentType
			return &periscopepb.GetClipEventsResponse{
				Events: []*periscopepb.ClipEvent{{RequestId: "req-1", StreamId: "raw-ae"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-ae")
	stage, contentType := "completed", "clip"
	got, err := periA(p).DoGetArtifactEventsConnection(clientstest.AuthedCtx("t1"), &relayID, &stage, &contentType, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Relay stream ID is decoded to raw before becoming the name filter.
	if gotName == nil || *gotName != "raw-ae" {
		t.Fatalf("stream not normalized into name filter: %v", gotName)
	}
	if gotStage == nil || *gotStage != "completed" || gotContentType == nil || *gotContentType != "clip" {
		t.Fatalf("filters not forwarded: %v %v", gotStage, gotContentType)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.RequestId != "req-1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetArtifactEventsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetArtifactEventsConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetArtifactState ---

func TestDoGetArtifactState_ForwardsRequestIDAndReturnsArtifact(t *testing.T) {
	var gotTenant, gotReqID string
	p := &clientstest.FakePeriscope{
		GetArtifactStateFn: func(_ context.Context, tenantID, requestID string) (*periscopepb.GetArtifactStateResponse, error) {
			gotTenant, gotReqID = tenantID, requestID
			return &periscopepb.GetArtifactStateResponse{
				Artifact: &periscopepb.ArtifactState{RequestId: requestID, Stage: "completed"},
			}, nil
		},
	}
	got, err := periA(p).DoGetArtifactState(clientstest.AuthedCtx("t1"), "req-7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "t1" || gotReqID != "req-7" {
		t.Fatalf("args not forwarded: tenant=%q req=%q", gotTenant, gotReqID)
	}
	if got == nil || got.RequestId != "req-7" || got.Stage != "completed" {
		t.Fatalf("artifact not unwrapped: %+v", got)
	}
}

func TestDoGetArtifactState_BackendError(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetArtifactStateFn: func(context.Context, string, string) (*periscopepb.GetArtifactStateResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := periA(p).DoGetArtifactState(clientstest.AuthedCtx("t1"), "req-1"); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestDoGetArtifactState_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetArtifactState(authedNoTenantCtx(), "req-1"); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetArtifactStatesConnection ---

func TestDoGetArtifactStatesConnection_NormalizesStreamAndForwardsFilters(t *testing.T) {
	var gotStream, gotContentType, gotStage *string
	p := &clientstest.FakePeriscope{
		GetArtifactStatesFn: func(_ context.Context, _ string, streamID, contentType, stage *string, _ *periscope.CursorPaginationOpts) (*periscopepb.GetArtifactStatesResponse, error) {
			gotStream, gotContentType, gotStage = streamID, contentType, stage
			return &periscopepb.GetArtifactStatesResponse{
				Artifacts: []*periscopepb.ArtifactState{{RequestId: "as-1", StreamId: "raw-as"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-as")
	contentType, stage := "dvr", "processing"
	got, err := periA(p).DoGetArtifactStatesConnection(clientstest.AuthedCtx("t1"), &relayID, &contentType, &stage, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-as" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotContentType == nil || *gotContentType != "dvr" || gotStage == nil || *gotStage != "processing" {
		t.Fatalf("filters not forwarded: %v %v", gotContentType, gotStage)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.RequestId != "as-1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetArtifactStatesConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetArtifactStatesConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetBufferEventsConnection ---

func TestDoGetBufferEventsConnection_NormalizesStreamAndConvertsRange(t *testing.T) {
	var gotStream string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetBufferEventsFn: func(_ context.Context, _ string, streamID string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetBufferEventsResponse, error) {
			gotStream, gotTR = streamID, tr
			return &periscopepb.GetBufferEventsResponse{
				Events: []*periscopepb.BufferEvent{{EventId: "be-1", StreamId: "raw-be"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-be")
	tr := testRange()
	got, err := periA(p).DoGetBufferEventsConnection(clientstest.AuthedCtx("t1"), relayID, tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream != "raw-be" {
		t.Fatalf("stream not normalized: %q", gotStream)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) || !gotTR.EndTime.Equal(tr.End) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.EventId != "be-1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetBufferEventsConnection_EmptyStreamRejected(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetBufferEventsConnection(clientstest.AuthedCtx("t1"), "", nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected streamId required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite empty stream: Calls=%d", p.Calls)
	}
}

func TestDoGetBufferEventsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetBufferEventsConnection(authedNoTenantCtx(), "s1", nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetClientMetrics5mConnection ---

func TestDoGetClientMetrics5mConnection_NormalizesStreamAndForwardsNode(t *testing.T) {
	var gotStream, gotNode *string
	p := &clientstest.FakePeriscope{
		GetClientMetrics5mFn: func(_ context.Context, _ string, streamID, nodeID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetClientMetrics5MResponse, error) {
			gotStream, gotNode = streamID, nodeID
			return &periscopepb.GetClientMetrics5MResponse{
				Records: []*periscopepb.ClientMetrics5M{{Id: "cm-1", StreamId: "raw-cm"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-cm")
	node := "node-3"
	got, err := periA(p).DoGetClientMetrics5mConnection(clientstest.AuthedCtx("t1"), &relayID, &node, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-cm" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotNode == nil || *gotNode != "node-3" {
		t.Fatalf("nodeID not forwarded: %v", gotNode)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "cm-1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetClientMetrics5mConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetClientMetrics5mConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetClientQoeSummary ---

func TestDoGetClientQoeSummary_MapsSummary(t *testing.T) {
	loss := 0.25
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetClientQoeSummaryFn: func(_ context.Context, _ string, _ *string, tr *periscope.TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error) {
			gotTR = tr
			return &periscopepb.GetClientQoeSummaryResponse{
				Summary: &periscopepb.ClientQoeSummary{
					AvgPacketLossRate:   &loss,
					AvgBandwidthIn:      100,
					AvgBandwidthOut:     200,
					AvgConnectionTime:   12,
					TotalActiveSessions: 7,
				},
			}, nil
		},
	}
	tr := testRange()
	got, err := periA(p).DoGetClientQoeSummary(clientstest.AuthedCtx("t1"), nil, tr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if got.AvgPacketLossRate == nil || *got.AvgPacketLossRate != 0.25 {
		t.Fatalf("loss not mapped: %v", got.AvgPacketLossRate)
	}
	if got.AvgBandwidthIn != 100 || got.AvgBandwidthOut != 200 || got.AvgConnectionTime != 12 || got.TotalActiveSessions != 7 {
		t.Fatalf("summary not mapped: %+v", got)
	}
}

func TestDoGetClientQoeSummary_NilSummaryReturnsZeroValue(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetClientQoeSummaryFn: func(context.Context, string, *string, *periscope.TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error) {
			return &periscopepb.GetClientQoeSummaryResponse{}, nil
		},
	}
	got, err := periA(p).DoGetClientQoeSummary(clientstest.AuthedCtx("t1"), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Absent summary must yield a zero model, never a nil-deref panic.
	if got == nil || got.TotalActiveSessions != 0 || got.AvgBandwidthIn != 0 {
		t.Fatalf("expected zero summary, got %+v", got)
	}
}

func TestDoGetClientQoeSummary_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetClientQoeSummary(authedNoTenantCtx(), nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetClusterTrafficMatrix (empty pairs path avoids the Quartermaster geo enrichment) ---

func TestDoGetClusterTrafficMatrix_ReturnsPairsWithoutGeoEnrichment(t *testing.T) {
	var gotTenant string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetClusterTrafficMatrixFn: func(_ context.Context, tenantID string, tr *periscope.TimeRangeOpts) (*periscopepb.GetClusterTrafficMatrixResponse, error) {
			gotTenant, gotTR = tenantID, tr
			// A single pair with no remote cluster: enrichTrafficMatrixGeo still
			// queries Quartermaster per cluster, so keep the geo lookup out of
			// this Periscope-only test by returning an empty matrix.
			return &periscopepb.GetClusterTrafficMatrixResponse{}, nil
		},
	}
	tr := testRange()
	got, err := periA(p).DoGetClusterTrafficMatrix(clientstest.AuthedCtx("t1"), tr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "t1" {
		t.Fatalf("tenant = %q", gotTenant)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty pairs, got %+v", got)
	}
}

func TestDoGetClusterTrafficMatrix_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetClusterTrafficMatrix(authedNoTenantCtx(), nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetFederationEventsConnection ---

func TestDoGetFederationEventsConnection_DefaultLimitAndForwardsType(t *testing.T) {
	var gotLimit int32
	var gotType *string
	p := &clientstest.FakePeriscope{
		GetFederationEventsFn: func(_ context.Context, _ string, _ *periscope.TimeRangeOpts, eventType *string, limit int32) (*periscopepb.GetFederationEventsResponse, error) {
			gotLimit, gotType = limit, eventType
			return &periscopepb.GetFederationEventsResponse{
				Events:     []*periscopepb.FederationEvent{{EventType: "STREAM_ADVERTISED"}},
				TotalCount: 1,
			}, nil
		},
	}
	evtType := "STREAM_ADVERTISED"
	got, err := periA(p).DoGetFederationEventsConnection(clientstest.AuthedCtx("t1"), testRange(), nil, &evtType, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unset first defaults the limit to 100.
	if gotLimit != 100 {
		t.Fatalf("default limit not 100: %d", gotLimit)
	}
	if gotType == nil || *gotType != "STREAM_ADVERTISED" {
		t.Fatalf("eventType not forwarded: %v", gotType)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.EventType != "STREAM_ADVERTISED" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
	if got.TotalCount != 1 {
		t.Fatalf("total count not mapped: %d", got.TotalCount)
	}
}

func TestDoGetFederationEventsConnection_ExplicitLimit(t *testing.T) {
	var gotLimit int32
	p := &clientstest.FakePeriscope{
		GetFederationEventsFn: func(_ context.Context, _ string, _ *periscope.TimeRangeOpts, _ *string, limit int32) (*periscopepb.GetFederationEventsResponse, error) {
			gotLimit = limit
			return &periscopepb.GetFederationEventsResponse{}, nil
		},
	}
	n := 5
	if _, err := periA(p).DoGetFederationEventsConnection(clientstest.AuthedCtx("t1"), nil, &n, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotLimit != 5 {
		t.Fatalf("explicit limit not forwarded: %d", gotLimit)
	}
}

func TestDoGetFederationEventsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetFederationEventsConnection(authedNoTenantCtx(), nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetFederationSummary ---

func TestDoGetFederationSummary_ReturnsSummary(t *testing.T) {
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetFederationSummaryFn: func(_ context.Context, _ string, tr *periscope.TimeRangeOpts) (*periscopepb.GetFederationSummaryResponse, error) {
			gotTR = tr
			return &periscopepb.GetFederationSummaryResponse{
				Summary: &periscopepb.FederationSummary{TotalEvents: 42, OverallFailureRate: 0.1},
			}, nil
		},
	}
	tr := testRange()
	got, err := periA(p).DoGetFederationSummary(clientstest.AuthedCtx("t1"), tr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if got == nil || got.TotalEvents != 42 || got.OverallFailureRate != 0.1 {
		t.Fatalf("summary not unwrapped: %+v", got)
	}
}

func TestDoGetFederationSummary_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetFederationSummary(authedNoTenantCtx(), nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetPlayerBootSummary ---

func TestDoGetPlayerBootSummary_NormalizesStreamAndForwardsArtifact(t *testing.T) {
	var gotStream, gotArtifact *string
	p := &clientstest.FakePeriscope{
		GetPlayerBootSummaryFn: func(_ context.Context, _ string, streamID, artifactHash *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetPlayerBootSummaryResponse, error) {
			gotStream, gotArtifact = streamID, artifactHash
			return &periscopepb.GetPlayerBootSummaryResponse{
				Summary: &periscopepb.PlayerBootSummary{BootCount: 11, P50TtfMs: 250},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-pb")
	artifact := "vodhash-1"
	got, err := periA(p).DoGetPlayerBootSummary(clientstest.AuthedCtx("t1"), &relayID, &artifact, testRange(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-pb" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotArtifact == nil || *gotArtifact != "vodhash-1" {
		t.Fatalf("artifactHash not forwarded: %v", gotArtifact)
	}
	if got == nil || got.BootCount != 11 || got.P50TtfMs != 250 {
		t.Fatalf("summary not unwrapped: %+v", got)
	}
}

func TestDoGetPlayerBootSummary_NilSummaryReturnsZeroValue(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetPlayerBootSummaryFn: func(context.Context, string, *string, *string, *periscope.TimeRangeOpts) (*periscopepb.GetPlayerBootSummaryResponse, error) {
			return &periscopepb.GetPlayerBootSummaryResponse{}, nil
		},
	}
	got, err := periA(p).DoGetPlayerBootSummary(clientstest.AuthedCtx("t1"), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.BootCount != 0 {
		t.Fatalf("expected zero summary, got %+v", got)
	}
}

func TestDoGetPlayerBootSummary_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetPlayerBootSummary(authedNoTenantCtx(), nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetPlayerBootTimeSeries ---

func TestDoGetPlayerBootTimeSeries_DefaultIntervalAndReturnsBuckets(t *testing.T) {
	var gotInterval string
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetPlayerBootTimeSeriesFn: func(_ context.Context, _ string, streamID, _ *string, _ *periscope.TimeRangeOpts, interval string) (*periscopepb.GetPlayerBootTimeSeriesResponse, error) {
			gotInterval, gotStream = interval, streamID
			return &periscopepb.GetPlayerBootTimeSeriesResponse{
				Buckets: []*periscopepb.PlayerBootTimeSeriesBucket{{}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-pbt")
	got, err := periA(p).DoGetPlayerBootTimeSeries(clientstest.AuthedCtx("t1"), &relayID, nil, testRange(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// resolveInterval defaults the bucket interval to 1h when unset.
	if gotInterval != "1h" {
		t.Fatalf("default interval not 1h: %q", gotInterval)
	}
	if gotStream == nil || *gotStream != "raw-pbt" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if len(got) != 1 {
		t.Fatalf("buckets not returned: %+v", got)
	}
}

func TestDoGetPlayerBootTimeSeries_ExplicitInterval(t *testing.T) {
	var gotInterval string
	p := &clientstest.FakePeriscope{
		GetPlayerBootTimeSeriesFn: func(_ context.Context, _ string, _, _ *string, _ *periscope.TimeRangeOpts, interval string) (*periscopepb.GetPlayerBootTimeSeriesResponse, error) {
			gotInterval = interval
			return &periscopepb.GetPlayerBootTimeSeriesResponse{}, nil
		},
	}
	iv := "5m"
	if _, err := periA(p).DoGetPlayerBootTimeSeries(clientstest.AuthedCtx("t1"), nil, nil, nil, &iv, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInterval != "5m" {
		t.Fatalf("explicit interval not forwarded: %q", gotInterval)
	}
}

func TestDoGetPlayerBootTimeSeries_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetPlayerBootTimeSeries(authedNoTenantCtx(), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetProcessingUsageConnection ---

func TestDoGetProcessingUsageConnection_NormalizesStreamAndCopiesSummaries(t *testing.T) {
	var gotStream, gotProcType *string
	var gotSummaryOnly bool
	p := &clientstest.FakePeriscope{
		GetProcessingUsageFn: func(_ context.Context, _ string, streamID, processType *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetProcessingUsageResponse, error) {
			gotStream, gotProcType, gotSummaryOnly = streamID, processType, summaryOnly
			return &periscopepb.GetProcessingUsageResponse{
				Records:   []*periscopepb.ProcessingUsageRecord{{Id: "pu-1", StreamId: "raw-pu"}},
				Summaries: []*periscopepb.ProcessingUsageSummary{{}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-pu")
	procType := "Livepeer"
	got, err := periA(p).DoGetProcessingUsageConnection(clientstest.AuthedCtx("t1"), &relayID, &procType, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// summaryOnly is hard-wired false: detail records + summaries together.
	if gotSummaryOnly {
		t.Fatal("summaryOnly must be false")
	}
	if gotStream == nil || *gotStream != "raw-pu" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotProcType == nil || *gotProcType != "Livepeer" {
		t.Fatalf("processType not forwarded: %v", gotProcType)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "pu-1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
	if len(got.Summaries) != 1 {
		t.Fatalf("summaries not copied: %d", len(got.Summaries))
	}
}

func TestDoGetProcessingUsageConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetProcessingUsageConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetQualityTierDailyConnection ---

func TestDoGetQualityTierDailyConnection_NormalizesStreamAndBuildsEdges(t *testing.T) {
	var gotStream *string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetQualityTierDailyFn: func(_ context.Context, _ string, streamID *string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetQualityTierDailyResponse, error) {
			gotStream, gotTR = streamID, tr
			return &periscopepb.GetQualityTierDailyResponse{
				Records: []*periscopepb.QualityTierDaily{{Id: "qt-1", StreamId: "raw-qt", PrimaryTier: "hd"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-qt")
	tr := testRange()
	got, err := periA(p).DoGetQualityTierDailyConnection(clientstest.AuthedCtx("t1"), &relayID, tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-qt" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "qt-1" || got.Edges[0].Node.PrimaryTier != "hd" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetQualityTierDailyConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetQualityTierDailyConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetRoutingEfficiency ---

func TestDoGetRoutingEfficiency_MapsSummaryAndCountries(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetRoutingEfficiencyFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetRoutingEfficiencyResponse, error) {
			gotStream = streamID
			return &periscopepb.GetRoutingEfficiencyResponse{
				Summary: &periscopepb.RoutingEfficiencySummary{
					TotalDecisions: 100,
					SuccessCount:   90,
					SuccessRate:    0.9,
					AvgLatencyMs:   12.5,
					TopCountries: []*periscopepb.RoutingCountryStat{
						{CountryCode: "US", RequestCount: 50},
					},
				},
			}, nil
		},
	}
	stream := "s-re"
	got, err := periA(p).DoGetRoutingEfficiency(clientstest.AuthedCtx("t1"), &stream, testRange(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "s-re" {
		t.Fatalf("stream not forwarded: %v", gotStream)
	}
	if got.TotalDecisions != 100 || got.SuccessCount != 90 || got.SuccessRate != 0.9 || got.AvgLatencyMs != 12.5 {
		t.Fatalf("summary not mapped: %+v", got)
	}
	if len(got.TopCountries) != 1 || got.TopCountries[0].CountryCode != "US" || got.TopCountries[0].RequestCount != 50 {
		t.Fatalf("countries not mapped: %+v", got.TopCountries)
	}
}

func TestDoGetRoutingEfficiency_NilSummaryReturnsZeroValue(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetRoutingEfficiencyFn: func(context.Context, string, *string, *periscope.TimeRangeOpts) (*periscopepb.GetRoutingEfficiencyResponse, error) {
			return &periscopepb.GetRoutingEfficiencyResponse{}, nil
		},
	}
	got, err := periA(p).DoGetRoutingEfficiency(clientstest.AuthedCtx("t1"), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.TotalDecisions != 0 || len(got.TopCountries) != 0 {
		t.Fatalf("expected zero efficiency, got %+v", got)
	}
}

func TestDoGetRoutingEfficiency_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetRoutingEfficiency(authedNoTenantCtx(), nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetSessionQoeSummary ---

func TestDoGetSessionQoeSummary_NormalizesStreamAndReturnsSummary(t *testing.T) {
	var gotStream, gotArtifact *string
	p := &clientstest.FakePeriscope{
		GetSessionQoeSummaryFn: func(_ context.Context, _ string, streamID, artifactHash *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetSessionQoeSummaryResponse, error) {
			gotStream, gotArtifact = streamID, artifactHash
			return &periscopepb.GetSessionQoeSummaryResponse{
				Summary: &periscopepb.SessionQoeSummary{SessionCount: 8, PlayedHours: 3.5},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-sq")
	artifact := "vh-1"
	got, err := periA(p).DoGetSessionQoeSummary(clientstest.AuthedCtx("t1"), &relayID, &artifact, testRange(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-sq" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotArtifact == nil || *gotArtifact != "vh-1" {
		t.Fatalf("artifactHash not forwarded: %v", gotArtifact)
	}
	if got == nil || got.SessionCount != 8 || got.PlayedHours != 3.5 {
		t.Fatalf("summary not unwrapped: %+v", got)
	}
}

func TestDoGetSessionQoeSummary_NilSummaryReturnsZeroValue(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetSessionQoeSummaryFn: func(context.Context, string, *string, *string, *periscope.TimeRangeOpts) (*periscopepb.GetSessionQoeSummaryResponse, error) {
			return &periscopepb.GetSessionQoeSummaryResponse{}, nil
		},
	}
	got, err := periA(p).DoGetSessionQoeSummary(clientstest.AuthedCtx("t1"), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.SessionCount != 0 {
		t.Fatalf("expected zero summary, got %+v", got)
	}
}

func TestDoGetSessionQoeSummary_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetSessionQoeSummary(authedNoTenantCtx(), nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetSessionQoeTimeSeries ---

func TestDoGetSessionQoeTimeSeries_DefaultIntervalAndReturnsBuckets(t *testing.T) {
	var gotInterval string
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetSessionQoeTimeSeriesFn: func(_ context.Context, _ string, streamID, _ *string, _ *periscope.TimeRangeOpts, interval string) (*periscopepb.GetSessionQoeTimeSeriesResponse, error) {
			gotInterval, gotStream = interval, streamID
			return &periscopepb.GetSessionQoeTimeSeriesResponse{
				Buckets: []*periscopepb.SessionQoeTimeSeriesBucket{{}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-sqt")
	got, err := periA(p).DoGetSessionQoeTimeSeries(clientstest.AuthedCtx("t1"), &relayID, nil, testRange(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInterval != "1h" {
		t.Fatalf("default interval not 1h: %q", gotInterval)
	}
	if gotStream == nil || *gotStream != "raw-sqt" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if len(got) != 1 {
		t.Fatalf("buckets not returned: %+v", got)
	}
}

func TestDoGetSessionQoeTimeSeries_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetSessionQoeTimeSeries(authedNoTenantCtx(), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- Access-scope-gated resolvers: tenant guard only ---
//
// DoGetClusterBootOps, DoGetClusterQoeOps, DoGetNodeMetricsAggregated and
// DoGetNodePerformance5mConnection all gate access through Quartermaster
// (requireClusterOperatorTenant / requireOwnedNode), which is nil here and would
// panic on the happy path. Their tenant guard runs before any client call, so
// only that branch is asserted — Periscope is never reached.

func TestDoGetClusterBootOps_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetClusterBootOps(authedNoTenantCtx(), nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

func TestDoGetClusterQoeOps_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetClusterQoeOps(authedNoTenantCtx(), nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

func TestDoGetNodeMetricsAggregated_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetNodeMetricsAggregated(authedNoTenantCtx(), nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

func TestDoGetNodePerformance5mConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periA(p).DoGetNodePerformance5mConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}
