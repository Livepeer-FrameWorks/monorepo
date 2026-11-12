package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func aliasTestBundle(t *testing.T, tenant, sub, version string) *dnspb.GetTLSBundleResponse {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{sub + ".cdn.example.test", "*." + sub + ".cdn.example.test"}
	now := time.Now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, DNSNames: names, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return &dnspb.GetTLSBundleResponse{Found: true, BundleId: "tenant:" + tenant, Domains: names, CertPem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), KeyPem: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private})), Version: version, ExpiresAt: template.NotAfter.Unix()}
}

type repeatingAliasRegistry struct {
	instances   []*quartermasterpb.ServiceInstance
	assignments map[string][]string
}

func (r *repeatingAliasRegistry) ListServiceInstances(context.Context, string, string, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
	return &quartermasterpb.ListServiceInstancesResponse{Instances: r.instances}, nil
}
func (r *repeatingAliasRegistry) ListServiceClusterAssignments(_ context.Context, id, _ string) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error) {
	return &quartermasterpb.ListServiceClusterAssignmentsResponse{ClusterIds: r.assignments[id]}, nil
}
func stableAliasRegistry() *repeatingAliasRegistry {
	return &repeatingAliasRegistry{instances: []*quartermasterpb.ServiceInstance{{ServiceId: "foghorn", InstanceId: "fh-1", ClusterId: "media-test", Status: "running"}}}
}
