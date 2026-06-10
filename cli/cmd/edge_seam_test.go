package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeEdgePreRegClient is a hand-written stand-in for the Foghorn
// PreRegisterEdge surface. It records the request and returns canned output.
type fakeEdgePreRegClient struct {
	gotReq *foghornpb.PreRegisterEdgeRequest
	resp   *foghornpb.PreRegisterEdgeResponse
	err    error
}

func (f *fakeEdgePreRegClient) PreRegisterEdge(_ context.Context, req *foghornpb.PreRegisterEdgeRequest) (*foghornpb.PreRegisterEdgeResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

func TestRunEdgePreRegister(t *testing.T) {
	t.Run("happy forwards request and returns response", func(t *testing.T) {
		f := &fakeEdgePreRegClient{resp: &foghornpb.PreRegisterEdgeResponse{
			NodeId:     "edge-1",
			EdgeDomain: "edge-1.example.com",
		}}
		req := &foghornpb.PreRegisterEdgeRequest{
			EnrollmentToken: "tok",
			ExternalIp:      "203.0.113.4",
			PreferredNodeId: "edge-1",
		}
		got, err := runEdgePreRegister(context.Background(), f, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.GetNodeId() != "edge-1" || got.GetEdgeDomain() != "edge-1.example.com" {
			t.Fatalf("unexpected response: %+v", got)
		}
		if f.gotReq == nil || f.gotReq.GetEnrollmentToken() != "tok" || f.gotReq.GetExternalIp() != "203.0.113.4" {
			t.Fatalf("request not forwarded verbatim: %+v", f.gotReq)
		}
	})

	t.Run("rpc error is propagated", func(t *testing.T) {
		f := &fakeEdgePreRegClient{err: status.Error(codes.PermissionDenied, "bad token")}
		_, err := runEdgePreRegister(context.Background(), f, &foghornpb.PreRegisterEdgeRequest{})
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied, got %v", err)
		}
	})
}

// fakeEdgeRegisterQM is a hand-written stand-in for the Quartermaster edge
// registration surface. It records each request and returns canned responses
// and errors per RPC, so runEdgeRegisterNode is exercised without a live
// control plane.
type fakeEdgeRegisterQM struct {
	healthErr error

	gotCreate  *quartermasterpb.CreateNodeRequest
	createResp *quartermasterpb.NodeResponse
	createErr  error

	gotToken  *quartermasterpb.CreateEnrollmentTokenRequest
	tokenResp *quartermasterpb.CreateBootstrapTokenResponse
	tokenErr  error
}

func (f *fakeEdgeRegisterQM) CheckHealth(_ context.Context) error { return f.healthErr }

func (f *fakeEdgeRegisterQM) CreateNode(_ context.Context, req *quartermasterpb.CreateNodeRequest) (*quartermasterpb.NodeResponse, error) {
	f.gotCreate = req
	return f.createResp, f.createErr
}

func (f *fakeEdgeRegisterQM) CreateEnrollmentToken(_ context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	f.gotToken = req
	return f.tokenResp, f.tokenErr
}

func okEdgeRegisterQM() *fakeEdgeRegisterQM {
	return &fakeEdgeRegisterQM{
		createResp: &quartermasterpb.NodeResponse{
			Node: &quartermasterpb.InfrastructureNode{
				NodeId:        "edge-us-east-1",
				OwnerTenantId: strptr("tenant-7"),
			},
		},
		tokenResp: &quartermasterpb.CreateBootstrapTokenResponse{
			Token: &quartermasterpb.BootstrapToken{Token: "enroll-xyz"},
		},
	}
}

func TestRunEdgeRegisterNode(t *testing.T) {
	t.Run("happy returns token, forwards request, renders", func(t *testing.T) {
		f := okEdgeRegisterQM()
		var buf bytes.Buffer
		token, err := runEdgeRegisterNode(context.Background(), &buf, f, "qm:9000", "edge-us-east-1", "cluster-a", "203.0.113.4", "us-east-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token != "enroll-xyz" {
			t.Fatalf("expected token enroll-xyz, got %q", token)
		}
		if f.gotCreate == nil {
			t.Fatal("CreateNode not called")
		}
		if f.gotCreate.GetClusterId() != "cluster-a" || f.gotCreate.GetNodeName() != "edge-us-east-1" {
			t.Fatalf("CreateNode request not forwarded: %+v", f.gotCreate)
		}
		if f.gotCreate.GetExternalIp() != "203.0.113.4" || f.gotCreate.GetRegion() != "us-east-1" {
			t.Fatalf("optional fields not set: %+v", f.gotCreate)
		}
		if f.gotToken == nil || f.gotToken.GetClusterId() != "cluster-a" {
			t.Fatalf("enrollment token request not forwarded: %+v", f.gotToken)
		}
		out := buf.String()
		if !strings.Contains(out, "edge-us-east-1") {
			t.Fatalf("output missing node id render: %q", out)
		}
	})

	t.Run("optional fields omitted when empty", func(t *testing.T) {
		f := okEdgeRegisterQM()
		var buf bytes.Buffer
		if _, err := runEdgeRegisterNode(context.Background(), &buf, f, "qm:9000", "edge-1", "cluster-a", "", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.gotCreate.ExternalIp != nil {
			t.Fatalf("expected nil ExternalIp, got %v", f.gotCreate.ExternalIp)
		}
		if f.gotCreate.Region != nil {
			t.Fatalf("expected nil Region, got %v", f.gotCreate.Region)
		}
	})

	t.Run("health failure short-circuits", func(t *testing.T) {
		f := okEdgeRegisterQM()
		f.healthErr = status.Error(codes.Unavailable, "down")
		var buf bytes.Buffer
		_, err := runEdgeRegisterNode(context.Background(), &buf, f, "qm:9000", "edge-1", "cluster-a", "", "")
		if err == nil {
			t.Fatal("expected health error")
		}
		if !strings.Contains(err.Error(), "health check failed") {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.gotCreate != nil {
			t.Fatal("CreateNode should not be called after health failure")
		}
	})

	t.Run("CreateNode error is wrapped", func(t *testing.T) {
		f := okEdgeRegisterQM()
		f.createResp = nil
		f.createErr = errors.New("rpc boom")
		var buf bytes.Buffer
		_, err := runEdgeRegisterNode(context.Background(), &buf, f, "qm:9000", "edge-1", "cluster-a", "", "")
		if err == nil || !strings.Contains(err.Error(), "failed to create node") {
			t.Fatalf("expected create-node error, got %v", err)
		}
	})

	t.Run("missing owner tenant id rejected", func(t *testing.T) {
		f := okEdgeRegisterQM()
		f.createResp = &quartermasterpb.NodeResponse{
			Node: &quartermasterpb.InfrastructureNode{NodeId: "edge-1"},
		}
		var buf bytes.Buffer
		_, err := runEdgeRegisterNode(context.Background(), &buf, f, "qm:9000", "edge-1", "cluster-a", "", "")
		if err == nil || !strings.Contains(err.Error(), "owner_tenant_id") {
			t.Fatalf("expected owner_tenant_id error, got %v", err)
		}
		if f.gotToken != nil {
			t.Fatal("CreateEnrollmentToken should not be called without tenant id")
		}
	})

	t.Run("CreateEnrollmentToken error is wrapped", func(t *testing.T) {
		f := okEdgeRegisterQM()
		f.tokenResp = nil
		f.tokenErr = errors.New("token boom")
		var buf bytes.Buffer
		_, err := runEdgeRegisterNode(context.Background(), &buf, f, "qm:9000", "edge-1", "cluster-a", "", "")
		if err == nil || !strings.Contains(err.Error(), "create enrollment token") {
			t.Fatalf("expected token error, got %v", err)
		}
	})

	t.Run("empty token rejected", func(t *testing.T) {
		f := okEdgeRegisterQM()
		f.tokenResp = &quartermasterpb.CreateBootstrapTokenResponse{
			Token: &quartermasterpb.BootstrapToken{Token: ""},
		}
		var buf bytes.Buffer
		_, err := runEdgeRegisterNode(context.Background(), &buf, f, "qm:9000", "edge-1", "cluster-a", "", "")
		if err == nil || !strings.Contains(err.Error(), "empty enrollment token") {
			t.Fatalf("expected empty-token error, got %v", err)
		}
	})
}

// fakeEdgeNavCert is a hand-written stand-in for the Navigator
// IssueCertificate surface.
type fakeEdgeNavCert struct {
	gotReq *dnspb.IssueCertificateRequest
	resp   *dnspb.IssueCertificateResponse
	err    error
}

func (f *fakeEdgeNavCert) IssueCertificate(_ context.Context, req *dnspb.IssueCertificateRequest) (*dnspb.IssueCertificateResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

func TestRunEdgeFetchCert(t *testing.T) {
	req := func() *dnspb.IssueCertificateRequest {
		return &dnspb.IssueCertificateRequest{Domain: "edge-1.example.com", Email: "ops@example.com"}
	}

	t.Run("happy returns cert and key, forwards request", func(t *testing.T) {
		f := &fakeEdgeNavCert{resp: &dnspb.IssueCertificateResponse{
			Success: true,
			CertPem: "CERT",
			KeyPem:  "KEY",
		}}
		var buf bytes.Buffer
		cert, key, err := runEdgeFetchCert(context.Background(), &buf, f, req())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cert != "CERT" || key != "KEY" {
			t.Fatalf("unexpected cert/key: %q %q", cert, key)
		}
		if f.gotReq == nil || f.gotReq.GetDomain() != "edge-1.example.com" || f.gotReq.GetEmail() != "ops@example.com" {
			t.Fatalf("request not forwarded: %+v", f.gotReq)
		}
		if !strings.Contains(buf.String(), "edge-1.example.com") {
			t.Fatalf("output missing domain: %q", buf.String())
		}
	})

	t.Run("rpc error is wrapped", func(t *testing.T) {
		f := &fakeEdgeNavCert{err: status.Error(codes.DeadlineExceeded, "slow")}
		var buf bytes.Buffer
		_, _, err := runEdgeFetchCert(context.Background(), &buf, f, req())
		if err == nil || !strings.Contains(err.Error(), "certificate issuance failed") {
			t.Fatalf("expected wrapped rpc error, got %v", err)
		}
	})

	t.Run("unsuccessful envelope uses error field", func(t *testing.T) {
		f := &fakeEdgeNavCert{resp: &dnspb.IssueCertificateResponse{
			Success: false,
			Error:   "dns-01 timeout",
		}}
		var buf bytes.Buffer
		_, _, err := runEdgeFetchCert(context.Background(), &buf, f, req())
		if err == nil || !strings.Contains(err.Error(), "dns-01 timeout") {
			t.Fatalf("expected envelope error, got %v", err)
		}
	})

	t.Run("unsuccessful envelope falls back to message", func(t *testing.T) {
		f := &fakeEdgeNavCert{resp: &dnspb.IssueCertificateResponse{
			Success: false,
			Message: "pending validation",
		}}
		var buf bytes.Buffer
		_, _, err := runEdgeFetchCert(context.Background(), &buf, f, req())
		if err == nil || !strings.Contains(err.Error(), "pending validation") {
			t.Fatalf("expected message fallback, got %v", err)
		}
	})
}
