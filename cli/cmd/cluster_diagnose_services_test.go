package cmd

import (
	"bytes"
	"context"
	"errors"
	"frameworks/cli/pkg/inventory"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	controlpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"strings"
	"testing"
	"testing/synctest"
)

type fakeBillingDiagnosis struct {
	tenant, jwt string
	err         error
}

func (f *fakeBillingDiagnosis) DiagnoseBilling(ctx context.Context, tenant string) (*purserpb.DiagnoseBillingResponse, error) {
	f.tenant, f.jwt = tenant, ctxkeys.GetJWTToken(ctx)
	return &purserpb.DiagnoseBillingResponse{TenantId: tenant, Status: "active", Intents: []*purserpb.BillingDiagnosticIntent{{Id: "intent", SubmissionState: "unknown", AmountCents: 1250, Currency: "EUR"}}, IntentCount: 1}, f.err
}

type fakeWebhookDiagnosis struct {
	tenant string
	err    error
}

func (f *fakeWebhookDiagnosis) DiagnoseWebhooks(_ context.Context, tenant string) (*bosunpb.DiagnoseWebhooksResponse, error) {
	f.tenant = tenant
	return &bosunpb.DiagnoseWebhooksResponse{TenantId: tenant, Endpoints: []*bosunpb.WebhookEndpointDiagnosis{{Endpoint: &bosunpb.WebhookEndpoint{Id: "endpoint", Status: bosunpb.WebhookEndpointStatus_WEBHOOK_ENDPOINT_STATUS_DISABLED, DisabledReason: bosunpb.WebhookEndpointDisabledReason_WEBHOOK_ENDPOINT_DISABLED_REASON_FAILING}}}}, f.err
}
func TestServiceDiagnosticReads(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		var out bytes.Buffer
		billing := &fakeBillingDiagnosis{}
		if err := runBillingDiagnose(t.Context(), &out, billing, "operator-jwt", "tenant", jsonOutput); err != nil {
			t.Fatal(err)
		}
		if billing.tenant != "tenant" || billing.jwt != "operator-jwt" || !strings.Contains(out.String(), "unknown") {
			t.Fatalf("billing target/output: %s %s %s", billing.tenant, billing.jwt, out.String())
		}
		if !jsonOutput && !strings.Contains(out.String(), "amount_cents=1250 currency=EUR") {
			t.Fatalf("payment amount has ambiguous units: %s", out.String())
		}
		out.Reset()
		billing.err = errors.New("unavailable")
		if err := runBillingDiagnose(t.Context(), &out, billing, "operator-jwt", "tenant", jsonOutput); err == nil || out.Len() != 0 {
			t.Fatalf("billing error produced success output: %v %s", err, out.String())
		}
		webhooks := &fakeWebhookDiagnosis{}
		if err := runWebhooksDiagnose(t.Context(), &out, webhooks, "operator-jwt", "tenant", jsonOutput); err != nil {
			t.Fatal(err)
		}
		if webhooks.tenant != "tenant" || !strings.Contains(out.String(), "FAILING") {
			t.Fatal(out.String())
		}
		out.Reset()
		webhooks.err = errors.New("unavailable")
		if err := runWebhooksDiagnose(t.Context(), &out, webhooks, "operator-jwt", "tenant", jsonOutput); err == nil || out.Len() != 0 {
			t.Fatalf("webhook error produced success output: %v %s", err, out.String())
		}
	}
}

type fakeStreamDiagnosis struct {
	manifest *inventory.Manifest
	entries  []string
	badEU    bool
	usOnline bool
	waitEU   bool
}

func (f *fakeStreamDiagnosis) ResolveStream(context.Context, string) (*commodorepb.DiagnoseStreamResponse, error) {
	return &commodorepb.DiagnoseStreamResponse{TenantId: "tenant", StreamId: "stream", InternalName: "live+one", ActiveClusterId: "media-eu", Authorities: []*commodorepb.StreamAuthorityDiagnosis{{CellId: "cell-eu"}, {CellId: "cell-us"}}}, nil
}
func (f *fakeStreamDiagnosis) Manifest(context.Context) (*inventory.Manifest, error) {
	return f.manifest, nil
}
func (f *fakeStreamDiagnosis) DiagnoseStream(ctx context.Context, entry string, req *foghornpb.DiagnoseStreamRequest) (*foghornpb.DiagnoseStreamResponse, error) {
	f.entries = append(f.entries, entry)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entry == "foghorn-eu" && f.waitEU {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if req.GetTenantId() != "tenant" || req.GetStreamId() != "stream" {
		return nil, errors.New("incorrect tenant or stream")
	}
	if entry == "foghorn-us" && !f.usOnline {
		return nil, errors.New("cell offline")
	}
	node := "edge-eu"
	if entry == "foghorn-us" {
		node = "edge-us"
	}
	if entry == "foghorn-eu" && f.badEU {
		node = string([]byte{0xff})
	}
	return &foghornpb.DiagnoseStreamResponse{TenantId: req.GetTenantId(), Authorities: []*foghornpb.StreamHeldAuthorityDiagnosis{{Kind: "tenant", DecisionReason: "payment_required"}}, Sessions: []*foghornpb.StreamIngestDiagnosis{{NodeId: node}}}, nil
}
func TestStreamDiagnosisMapsCellsAndRetainsPartialResults(t *testing.T) {
	manifest := perCellFoghornManifest()
	manifest.Clusters = map[string]inventory.ClusterConfig{"media-eu": {Cell: "cell-eu"}, "media-us": {Cell: "cell-us"}}
	for _, jsonOutput := range []bool{false, true} {
		src := &fakeStreamDiagnosis{manifest: manifest}
		var out bytes.Buffer
		err := runStreamDiagnose(t.Context(), &out, src, "operator-jwt", "playback", "", jsonOutput)
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("partial read error: %v", err)
		}
		if strings.Join(src.entries, ",") != "foghorn-eu,foghorn-us" {
			t.Fatalf("queried %v, want each cell exactly once", src.entries)
		}
		if !strings.Contains(out.String(), "edge-eu") || !strings.Contains(out.String(), "cell offline") || !strings.Contains(out.String(), "payment_required") {
			t.Fatal(out.String())
		}
	}
}

type fakeNodeInventory struct{}

func (fakeNodeInventory) GetNode(context.Context, string) (*quartermasterpb.NodeResponse, error) {
	return &quartermasterpb.NodeResponse{Node: &quartermasterpb.InfrastructureNode{Id: "node-uuid", NodeId: "edge-one", ClusterId: "media-eu", EnrollmentOrigin: "runtime_enrolled"}}, nil
}
func (fakeNodeInventory) ListNodeFingerprints(context.Context, string, bool, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodeFingerprintsResponse, error) {
	return &quartermasterpb.ListNodeFingerprintsResponse{Fingerprints: []*quartermasterpb.NodeFingerprintBinding{{FingerprintId: "binding", NodeId: "edge-one", HasIdentityKey: true, FingerprintMachineSha256: "private-fingerprint"}}}, nil
}
func TestNodeDiagnosisReportsConfigWithoutRawFingerprint(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		var out bytes.Buffer
		health := func(_ context.Context, entry, node string) (*controlpb.GetNodeHealthResponse, error) {
			if entry != "foghorn-eu" || node != "edge-one" {
				return nil, errors.New("wrong cell/node")
			}
			return &controlpb.GetNodeHealthResponse{NodeId: node, ProvisionedConfigCliVersion: "v0.3.14", Capabilities: []string{"ingest"}}, nil
		}
		err := runNodeDiagnose(t.Context(), &out, fakeNodeInventory{}, health, perCellFoghornManifest(), "operator-jwt", "edge-one", "", jsonOutput)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "v0.3.14") || !strings.Contains(out.String(), "binding") || strings.Contains(out.String(), "private-fingerprint") {
			t.Fatal(out.String())
		}
	}
}

func TestStreamDiagnosisEncodingErrorKeepsOtherCellEvidence(t *testing.T) {
	manifest := perCellFoghornManifest()
	manifest.Clusters = map[string]inventory.ClusterConfig{"media-eu": {Cell: "cell-eu"}, "media-us": {Cell: "cell-us"}}
	src := &fakeStreamDiagnosis{manifest: manifest, badEU: true, usOnline: true}
	var out bytes.Buffer
	err := runStreamDiagnose(t.Context(), &out, src, "operator-jwt", "playback", "", false)
	if err == nil || !strings.Contains(out.String(), "edge-us") {
		t.Fatalf("lost healthy-cell evidence on an encoding error: %v\n%s", err, out.String())
	}
}

func TestStreamDiagnosisCellTimeoutKeepsHealthyEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		jsonOutput bool
	}{{"text", false}, {"json", true}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				manifest := perCellFoghornManifest()
				manifest.Clusters = map[string]inventory.ClusterConfig{"media-eu": {Cell: "cell-eu"}, "media-us": {Cell: "cell-us"}}
				src := &fakeStreamDiagnosis{manifest: manifest, waitEU: true, usOnline: true}
				var out bytes.Buffer
				err := runStreamDiagnose(t.Context(), &out, src, "operator-jwt", "playback", "", tc.jsonOutput)
				if err == nil || !strings.Contains(err.Error(), "incomplete") {
					t.Fatalf("expected partial diagnosis error, got %v", err)
				}
				if strings.Join(src.entries, ",") != "foghorn-eu,foghorn-us" {
					t.Fatalf("queried %v, want each cell exactly once", src.entries)
				}
				if !strings.Contains(out.String(), "context deadline exceeded") || !strings.Contains(out.String(), "edge-us") {
					t.Fatalf("lost healthy-cell evidence after first cell timed out: %s", out.String())
				}
			})
		})
	}
}
