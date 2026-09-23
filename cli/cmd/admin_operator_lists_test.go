package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type fakeAllTenantTokensClient struct {
	pages     []*commodorepb.AdminListAPITokensResponse
	err       error
	calls     []*commonpb.CursorPaginationRequest
	tenantIDs []string
	filters   []bool
	jwts      []string
}

func (f *fakeAllTenantTokensClient) AdminListAPITokens(ctx context.Context, tenantID string, unsupported bool, p *commonpb.CursorPaginationRequest) (*commodorepb.AdminListAPITokensResponse, error) {
	f.calls = append(f.calls, p)
	f.tenantIDs = append(f.tenantIDs, tenantID)
	f.filters = append(f.filters, unsupported)
	f.jwts = append(f.jwts, ctxkeys.GetJWTToken(ctx))
	if f.err != nil {
		return nil, f.err
	}
	return f.pages[len(f.calls)-1], nil
}

func TestRunTokensListAllTenantsWalksPagesWithOperatorJWT(t *testing.T) {
	fake := &fakeAllTenantTokensClient{pages: []*commodorepb.AdminListAPITokensResponse{
		{
			Tokens:     []*commodorepb.AdminAPITokenInfo{{Id: "t-1", TenantId: "tenant-a", TokenName: "ci", Permissions: []string{"read", "streams:read"}, UnsupportedPermissions: []string{"read"}, Status: "active"}},
			Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true, EndCursor: strPtr("c1")},
		},
		{
			Tokens:     []*commodorepb.AdminAPITokenInfo{{Id: "t-2", TenantId: "tenant-b", TokenName: "legacy", Permissions: []string{"write"}, UnsupportedPermissions: []string{"write"}, Status: "inactive"}},
			Pagination: &commonpb.CursorPaginationResponse{},
		},
	}}
	var buf bytes.Buffer
	filter := adminAllTenantTokensFilter{TenantID: "tenant-a", UnsupportedScopesOnly: true}
	if err := runTokensListAllTenants(context.Background(), &buf, fake, "operator-jwt", filter, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 || fake.calls[0].GetAfter() != "" || fake.calls[1].GetAfter() != "c1" || fake.calls[0].GetFirst() != operatorListPageSize {
		t.Fatalf("page requests = %+v", fake.calls)
	}
	for i := range fake.calls {
		if fake.jwts[i] != "operator-jwt" || fake.tenantIDs[i] != "tenant-a" || !fake.filters[i] {
			t.Fatalf("call %d: jwt=%q tenant=%q unsupported=%t", i, fake.jwts[i], fake.tenantIDs[i], fake.filters[i])
		}
	}
	out := buf.String()
	for _, want := range []string{"unsupported scopes (2)", "ci (t-1) tenant=tenant-a", "unsupported=read", "legacy (t-2) tenant=tenant-b", "unsupported=write"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunTokensListAllTenantsJSONAndErrors(t *testing.T) {
	fake := &fakeAllTenantTokensClient{pages: []*commodorepb.AdminListAPITokensResponse{{Tokens: []*commodorepb.AdminAPITokenInfo{{Id: "t-1"}}}}}
	var buf bytes.Buffer
	if err := runTokensListAllTenants(context.Background(), &buf, fake, "jwt", adminAllTenantTokensFilter{}, true); err != nil {
		t.Fatal(err)
	}
	var decoded commodorepb.AdminListAPITokensResponse
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil || len(decoded.Tokens) != 1 {
		t.Fatalf("json output %q: %v", buf.String(), err)
	}

	noSession := &fakeAllTenantTokensClient{}
	if err := runTokensListAllTenants(context.Background(), &bytes.Buffer{}, noSession, "", adminAllTenantTokensFilter{}, false); err == nil || len(noSession.calls) != 0 {
		t.Fatalf("missing session: err=%v calls=%d; want refusal before any RPC", err, len(noSession.calls))
	}
	denied := &fakeAllTenantTokensClient{err: errors.New("permission denied")}
	if err := runTokensListAllTenants(context.Background(), &bytes.Buffer{}, denied, "jwt", adminAllTenantTokensFilter{}, false); err == nil {
		t.Fatal("RPC error swallowed")
	}
}

func TestRunTokensListAllTenantsRejectsRepeatedCursor(t *testing.T) {
	page := &commodorepb.AdminListAPITokensResponse{Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true, EndCursor: strPtr("same")}}
	fake := &fakeAllTenantTokensClient{pages: []*commodorepb.AdminListAPITokensResponse{page, page, page}}
	if err := runTokensListAllTenants(context.Background(), &bytes.Buffer{}, fake, "jwt", adminAllTenantTokensFilter{}, false); err == nil {
		t.Fatal("a non-advancing cursor must stop the walk")
	}
}

func TestAdminTokensListFiltersRequireAllTenants(t *testing.T) {
	cmd := newAdminTokensListCmd()
	cmd.SetArgs([]string{"--unsupported-scopes"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--all-tenants") {
		t.Fatalf("err = %v, want --all-tenants requirement", err)
	}
}

type fakeNodeFingerprintsClient struct {
	pages      []*quartermasterpb.ListNodeFingerprintsResponse
	listCalls  []*commonpb.CursorPaginationRequest
	listArgs   []string
	dupArgs    []bool
	unbindArgs [][3]string
	unbindJWT  string
	unbindErr  error
}

func (f *fakeNodeFingerprintsClient) ListNodeFingerprints(_ context.Context, clusterID string, duplicatesOnly bool, p *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodeFingerprintsResponse, error) {
	f.listCalls = append(f.listCalls, p)
	f.listArgs = append(f.listArgs, clusterID)
	f.dupArgs = append(f.dupArgs, duplicatesOnly)
	return f.pages[len(f.listCalls)-1], nil
}

func (f *fakeNodeFingerprintsClient) UnbindNodeFingerprint(ctx context.Context, nodeID, fingerprintID, reason string) (*quartermasterpb.UnbindNodeFingerprintResponse, error) {
	f.unbindArgs = append(f.unbindArgs, [3]string{nodeID, fingerprintID, reason})
	f.unbindJWT = ctxkeys.GetJWTToken(ctx)
	if f.unbindErr != nil {
		return nil, f.unbindErr
	}
	return &quartermasterpb.UnbindNodeFingerprintResponse{NodeId: nodeID, FingerprintId: fingerprintID}, nil
}

func TestRunNodeFingerprintsListDuplicates(t *testing.T) {
	machine := strings.Repeat("ab", 32)
	fake := &fakeNodeFingerprintsClient{pages: []*quartermasterpb.ListNodeFingerprintsResponse{
		{
			Fingerprints: []*quartermasterpb.NodeFingerprintBinding{{FingerprintId: "fp-1", NodeId: "edge-old", TenantId: "tenant-a", FingerprintMachineSha256: machine, MachineDuplicateCount: 2}},
			Pagination:   &commonpb.CursorPaginationResponse{HasNextPage: true, EndCursor: strPtr("c1")},
		},
		{
			Fingerprints: []*quartermasterpb.NodeFingerprintBinding{{FingerprintId: "fp-2", NodeId: "edge-new", ClusterId: "media-eu", FingerprintMachineSha256: machine, MachineDuplicateCount: 2, HasIdentityKey: true}},
		},
	}}
	var buf bytes.Buffer
	if err := runNodeFingerprintsList(context.Background(), &buf, fake, "operator-jwt", "media-eu", true, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.listCalls) != 2 || fake.listCalls[1].GetAfter() != "c1" || fake.listArgs[0] != "media-eu" || !fake.dupArgs[0] {
		t.Fatalf("list calls = %+v args=%v dup=%v", fake.listCalls, fake.listArgs, fake.dupArgs)
	}
	out := buf.String()
	for _, want := range []string{
		"Duplicate node fingerprint bindings (2)",
		"node=edge-old fingerprint=fp-1 cluster=(node row missing)",
		"machine=" + machine[:12] + "(x2)",
		"node=edge-new fingerprint=fp-2 cluster=media-eu",
		"identity_key=yes",
		"fingerprints unbind",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if err := runNodeFingerprintsList(context.Background(), &bytes.Buffer{}, &fakeNodeFingerprintsClient{}, "", "", false, false); err == nil {
		t.Fatal("list without an operator session must fail before the RPC")
	}
}

func TestRunNodeFingerprintUnbind(t *testing.T) {
	const fp = "0b6f3f7e-0000-4000-8000-000000000001"
	fake := &fakeNodeFingerprintsClient{}
	var confirmed [2]string
	var buf bytes.Buffer
	err := runNodeFingerprintUnbind(context.Background(), &buf, fake, "operator-jwt", "edge-old", fp, "  replaced host  ", func(nodeID, fingerprintID string) bool {
		confirmed = [2]string{nodeID, fingerprintID}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed != [2]string{"edge-old", fp} {
		t.Fatalf("confirm saw %v", confirmed)
	}
	if len(fake.unbindArgs) != 1 || fake.unbindArgs[0] != [3]string{"edge-old", fp, "replaced host"} || fake.unbindJWT != "operator-jwt" {
		t.Fatalf("unbind args=%v jwt=%q", fake.unbindArgs, fake.unbindJWT)
	}
	if !strings.Contains(buf.String(), "Unbound fingerprint "+fp+" from node edge-old") {
		t.Fatalf("output: %s", buf.String())
	}
}

func TestRunNodeFingerprintUnbindRefusals(t *testing.T) {
	const fp = "0b6f3f7e-0000-4000-8000-000000000001"
	always := func(string, string) bool { return true }
	for _, tc := range []struct {
		name, jwt, node, fp, reason string
	}{
		{name: "missing reason", jwt: "jwt", node: "edge-1", fp: fp, reason: "   "},
		{name: "fingerprint not a UUID", jwt: "jwt", node: "edge-1", fp: "not-a-uuid", reason: "stale"},
		{name: "missing node", jwt: "jwt", node: " ", fp: fp, reason: "stale"},
		{name: "no operator session", jwt: "", node: "edge-1", fp: fp, reason: "stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeNodeFingerprintsClient{}
			if err := runNodeFingerprintUnbind(context.Background(), &bytes.Buffer{}, fake, tc.jwt, tc.node, tc.fp, tc.reason, always); err == nil {
				t.Fatal("expected refusal")
			}
			if len(fake.unbindArgs) != 0 {
				t.Fatal("refused unbind still called Quartermaster")
			}
		})
	}

	fake := &fakeNodeFingerprintsClient{}
	var buf bytes.Buffer
	if err := runNodeFingerprintUnbind(context.Background(), &buf, fake, "jwt", "edge-1", fp, "stale", func(string, string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if len(fake.unbindArgs) != 0 || !strings.Contains(buf.String(), "Cancelled") {
		t.Fatalf("declined confirmation still unbound: %v", fake.unbindArgs)
	}

	failing := &fakeNodeFingerprintsClient{unbindErr: errors.New("not found")}
	if err := runNodeFingerprintUnbind(context.Background(), &bytes.Buffer{}, failing, "jwt", "edge-1", fp, "stale", always); err == nil {
		t.Fatal("RPC error swallowed")
	}
}

func TestAdminNodesRegistersFingerprintCommands(t *testing.T) {
	cmd, _, err := newAdminNodesCmd().Find([]string{"fingerprints", "unbind"})
	if err != nil || cmd.Name() != "unbind" || cmd.Flags().Lookup("reason") == nil || cmd.Flags().Lookup("yes") == nil {
		t.Fatalf("unbind command: %v %v", cmd, err)
	}
	list, _, err := newAdminNodesCmd().Find([]string{"fingerprints", "list"})
	if err != nil || list.Flags().Lookup("duplicates") == nil || list.Flags().Lookup("cluster-id") == nil {
		t.Fatalf("list command: %v %v", list, err)
	}
}
