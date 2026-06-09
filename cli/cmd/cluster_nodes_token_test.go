package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeReleaseTargetClient struct {
	resp *quartermasterpb.ClusterReleaseTargetResponse
	err  error
}

func (f *fakeReleaseTargetClient) GetClusterReleaseTarget(_ context.Context, _ *quartermasterpb.GetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error) {
	return f.resp, f.err
}

func TestRunResolveInstallVersion(t *testing.T) {
	ctxCfg := fwcfg.Context{}

	// Pinned target_version wins.
	v, err := runResolveInstallVersion(context.Background(), &fakeReleaseTargetClient{
		resp: &quartermasterpb.ClusterReleaseTargetResponse{Target: &quartermasterpb.ClusterReleaseTarget{TargetVersion: "v1.2.3", Channel: "stable"}},
	}, ctxCfg, "c-1", "fallback")
	if err != nil || v != "v1.2.3" {
		t.Errorf("target_version: got %q err %v, want v1.2.3", v, err)
	}

	// No version → channel.
	v, err = runResolveInstallVersion(context.Background(), &fakeReleaseTargetClient{
		resp: &quartermasterpb.ClusterReleaseTargetResponse{Target: &quartermasterpb.ClusterReleaseTarget{Channel: "stable"}},
	}, ctxCfg, "c-1", "fallback")
	if err != nil || v != "stable" {
		t.Errorf("channel: got %q err %v, want stable", v, err)
	}

	// Neither version nor channel → fallback.
	v, err = runResolveInstallVersion(context.Background(), &fakeReleaseTargetClient{
		resp: &quartermasterpb.ClusterReleaseTargetResponse{Target: &quartermasterpb.ClusterReleaseTarget{}},
	}, ctxCfg, "c-1", "fallback")
	if err != nil || v != "fallback" {
		t.Errorf("empty target: got %q err %v, want fallback", v, err)
	}

	// Nil target → fallback.
	v, err = runResolveInstallVersion(context.Background(), &fakeReleaseTargetClient{
		resp: &quartermasterpb.ClusterReleaseTargetResponse{},
	}, ctxCfg, "c-1", "fallback")
	if err != nil || v != "fallback" {
		t.Errorf("nil target: got %q err %v, want fallback", v, err)
	}

	// NotFound is not an error — it falls back.
	v, err = runResolveInstallVersion(context.Background(), &fakeReleaseTargetClient{
		err: status.Error(codes.NotFound, "no target"),
	}, ctxCfg, "c-1", "fallback")
	if err != nil || v != "fallback" {
		t.Errorf("NotFound: got %q err %v, want fallback", v, err)
	}

	// Any other error surfaces.
	if _, err := runResolveInstallVersion(context.Background(), &fakeReleaseTargetClient{
		err: status.Error(codes.Internal, "boom"),
	}, ctxCfg, "c-1", "fallback"); err == nil || !strings.Contains(err.Error(), "load cluster release target") {
		t.Errorf("internal error should surface, got %v", err)
	}
}

type fakeEnrollmentTokenClient struct {
	req  *quartermasterpb.CreateEnrollmentTokenRequest
	resp *quartermasterpb.CreateBootstrapTokenResponse
	err  error
}

func (f *fakeEnrollmentTokenClient) CreateEnrollmentToken(_ context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func okEnrollmentResp() *quartermasterpb.CreateBootstrapTokenResponse {
	return &quartermasterpb.CreateBootstrapTokenResponse{Token: &quartermasterpb.BootstrapToken{Token: "tok-xyz"}}
}

func TestRunCreateClusterEnrollmentToken_TokenNameAndTTL(t *testing.T) {
	// No node name → generic token name; user persona → no tenant binding.
	fake := &fakeEnrollmentTokenClient{resp: okEnrollmentResp()}
	tok, err := runCreateClusterEnrollmentToken(context.Background(), fake, fwcfg.Context{Persona: fwcfg.PersonaUser}, "c-1", "", "")
	if err != nil || tok != "tok-xyz" {
		t.Fatalf("got %q err %v", tok, err)
	}
	if fake.req.GetName() != "cluster nodes add" {
		t.Errorf("token name = %q, want generic", fake.req.GetName())
	}
	if fake.req.TenantId != nil {
		t.Errorf("user persona should not bind tenant, got %v", fake.req.TenantId)
	}
	if fake.req.Ttl != nil {
		t.Errorf("no TTL → nil, got %v", fake.req.Ttl)
	}

	// Node name → suffixed token name; TTL forwarded.
	fake2 := &fakeEnrollmentTokenClient{resp: okEnrollmentResp()}
	if _, err := runCreateClusterEnrollmentToken(context.Background(), fake2, fwcfg.Context{Persona: fwcfg.PersonaUser}, "c-1", "edge-eu", "24h"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake2.req.GetName() != "cluster nodes add: edge-eu" {
		t.Errorf("token name = %q, want suffixed", fake2.req.GetName())
	}
	if fake2.req.GetTtl() != "24h" {
		t.Errorf("ttl = %q, want 24h", fake2.req.GetTtl())
	}
}

func TestRunCreateClusterEnrollmentToken_PlatformBindsTenant(t *testing.T) {
	fake := &fakeEnrollmentTokenClient{resp: okEnrollmentResp()}
	ctxCfg := fwcfg.Context{Persona: fwcfg.PersonaPlatform, SystemTenantID: "tenant-1"}
	if _, err := runCreateClusterEnrollmentToken(context.Background(), fake, ctxCfg, "c-1", "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.req.GetTenantId() != "tenant-1" {
		t.Errorf("platform persona must bind system tenant, got %q", fake.req.GetTenantId())
	}
}

func TestRunCreateClusterEnrollmentToken_PlatformMissingTenant(t *testing.T) {
	fake := &fakeEnrollmentTokenClient{resp: okEnrollmentResp()}
	ctxCfg := fwcfg.Context{Persona: fwcfg.PersonaPlatform, Name: "plat", SystemTenantID: ""}
	if _, err := runCreateClusterEnrollmentToken(context.Background(), fake, ctxCfg, "c-1", "", ""); err == nil {
		t.Fatal("platform context without system_tenant_id must error before minting")
	}
	if fake.req != nil {
		t.Error("must not call CreateEnrollmentToken when tenant binding is impossible")
	}
}

func TestRunCreateClusterEnrollmentToken_EmptyTokenAndError(t *testing.T) {
	// Empty token in response is an error.
	empty := &fakeEnrollmentTokenClient{resp: &quartermasterpb.CreateBootstrapTokenResponse{Token: &quartermasterpb.BootstrapToken{Token: ""}}}
	if _, err := runCreateClusterEnrollmentToken(context.Background(), empty, fwcfg.Context{}, "c-1", "", ""); err == nil {
		t.Fatal("empty token should error")
	}

	rpcErr := &fakeEnrollmentTokenClient{err: errors.New("rpc down")}
	if _, err := runCreateClusterEnrollmentToken(context.Background(), rpcErr, fwcfg.Context{}, "c-1", "", ""); err == nil {
		t.Fatal("expected RPC error to propagate")
	}
}
