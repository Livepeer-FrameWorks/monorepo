package logic

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

const (
	internalRootRole         = "root_cert_only"
	internalIntermediateRole = "intermediate"
	internalLeafValidity     = 72 * time.Hour
	internalLeafRenewWindow  = 36 * time.Hour
	internalRootValidity     = 10 * 365 * 24 * time.Hour
	internalIntermediateTTL  = 2 * 365 * 24 * time.Hour
)

type internalCAStore interface {
	GetInternalCA(ctx context.Context, role string) (*store.InternalCA, error)
	SaveInternalCA(ctx context.Context, ca *store.InternalCA) error
	GetInternalCertificate(ctx context.Context, nodeID, serviceType string) (*store.InternalCertificate, error)
	SaveInternalCertificate(ctx context.Context, cert *store.InternalCertificate) error
}

type internalCAQuartermaster interface {
	ValidateBootstrapTokenEx(ctx context.Context, req *pb.ValidateBootstrapTokenRequest) (*pb.ValidateBootstrapTokenResponse, error)
	ListServiceInstances(ctx context.Context, clusterID, serviceID, nodeID string, pagination *pb.CursorPaginationRequest) (*pb.ListServiceInstancesResponse, error)
}

type InternalCAManager struct {
	store      internalCAStore
	qm         internalCAQuartermaster
	logger     logging.Logger
	rootDomain string
}

func NewInternalCAManager(store internalCAStore, qm internalCAQuartermaster, logger logging.Logger, rootDomain string) *InternalCAManager {
	return &InternalCAManager{
		store:      store,
		qm:         qm,
		logger:     logger,
		rootDomain: strings.TrimSpace(rootDomain),
	}
}

func (m *InternalCAManager) EnsureCA(ctx context.Context) error {
	_, rootErr := m.store.GetInternalCA(ctx, internalRootRole)
	_, intErr := m.store.GetInternalCA(ctx, internalIntermediateRole)
	if rootErr == nil && intErr == nil {
		return nil
	}
	if rootErr != nil && rootErr != store.ErrNotFound {
		return fmt.Errorf("load root ca: %w", rootErr)
	}
	if intErr != nil && intErr != store.ErrNotFound {
		return fmt.Errorf("load intermediate ca: %w", intErr)
	}

	rootCertFile := strings.TrimSpace(os.Getenv("NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE"))
	intermediateCertFile := strings.TrimSpace(os.Getenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE"))
	intermediateKeyFile := strings.TrimSpace(os.Getenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE"))
	rootCertB64 := strings.TrimSpace(os.Getenv("NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64"))
	intermediateCertB64 := strings.TrimSpace(os.Getenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64"))
	intermediateKeyB64 := strings.TrimSpace(os.Getenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64"))

	switch {
	case rootCertB64 != "" || intermediateCertB64 != "" || intermediateKeyB64 != "":
		if rootCertB64 == "" || intermediateCertB64 == "" || intermediateKeyB64 == "" {
			return fmt.Errorf("internal ca import requires root cert, intermediate cert, and intermediate key base64 env vars")
		}
		if err := m.importCAFromEnv(ctx, rootCertB64, intermediateCertB64, intermediateKeyB64); err != nil {
			return err
		}
		return nil
	case rootCertFile != "" || intermediateCertFile != "" || intermediateKeyFile != "":
		if rootCertFile == "" || intermediateCertFile == "" || intermediateKeyFile == "" {
			return fmt.Errorf("internal ca import requires root cert, intermediate cert, and intermediate key files")
		}
		if err := m.importCAFromFiles(ctx, rootCertFile, intermediateCertFile, intermediateKeyFile); err != nil {
			return err
		}
		return nil
	default:
		if config.IsProduction() {
			return fmt.Errorf("managed internal CA material is required in production")
		}
		if m.logger != nil {
			m.logger.Warn("Generating Navigator internal CA in-process; set NAVIGATOR_INTERNAL_CA_* file or *_PEM_B64 env vars to import managed CA material instead.")
		}
		return m.generateCA(ctx)
	}
}

func (m *InternalCAManager) GetCABundle(ctx context.Context) (string, error) {
	rootCA, err := m.store.GetInternalCA(ctx, internalRootRole)
	if err != nil {
		return "", fmt.Errorf("load root ca: %w", err)
	}
	intCA, err := m.store.GetInternalCA(ctx, internalIntermediateRole)
	if err != nil {
		return "", fmt.Errorf("load intermediate ca: %w", err)
	}
	return strings.TrimSpace(rootCA.CertPEM) + "\n" + strings.TrimSpace(intCA.CertPEM) + "\n", nil
}

func (m *InternalCAManager) IssueInternalCert(ctx context.Context, nodeID, serviceType, issueToken string) (*store.InternalCertificate, error) {
	nodeID = strings.TrimSpace(nodeID)
	serviceType = strings.TrimSpace(serviceType)
	issueToken = strings.TrimSpace(issueToken)
	if nodeID == "" || serviceType == "" || issueToken == "" {
		return nil, fmt.Errorf("node_id, service_type, and issue_token are required")
	}

	clusterID, err := m.authorizeIssue(ctx, nodeID, serviceType, issueToken)
	if err != nil {
		return nil, err
	}

	if existing, err := m.store.GetInternalCertificate(ctx, nodeID, serviceType); err == nil {
		if existing.ClusterID == clusterID && time.Until(existing.ExpiresAt) > internalLeafRenewWindow {
			return existing, nil
		}
	} else if err != store.ErrNotFound {
		return nil, fmt.Errorf("load internal certificate: %w", err)
	}

	certPEM, keyPEM, notAfter, err := m.signInternalLeaf(ctx, serviceType, clusterID)
	if err != nil {
		return nil, err
	}

	cert := &store.InternalCertificate{
		NodeID:      nodeID,
		ClusterID:   clusterID,
		ServiceType: serviceType,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		ExpiresAt:   notAfter,
	}
	if err := m.store.SaveInternalCertificate(ctx, cert); err != nil {
		return nil, fmt.Errorf("save internal certificate: %w", err)
	}
	return cert, nil
}

func (m *InternalCAManager) EnsureLocalServerCertificate(ctx context.Context, serviceType, certPath, keyPath string) error {
	serviceType = strings.TrimSpace(serviceType)
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	if serviceType == "" || certPath == "" || keyPath == "" {
		return nil
	}
	if existingCert, existingKey := fileExistsAndNonEmpty(certPath), fileExistsAndNonEmpty(keyPath); existingCert && existingKey {
		return nil
	}

	certPEM, keyPEM, _, err := m.signInternalLeaf(ctx, serviceType, "", "")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, []byte(certPEM), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		return err
	}
	return nil
}

func (m *InternalCAManager) importCAFromFiles(ctx context.Context, rootCertFile, intermediateCertFile, intermediateKeyFile string) error {
	rootCertPEM, err := os.ReadFile(filepath.Clean(rootCertFile))
	if err != nil {
		return fmt.Errorf("read root ca cert: %w", err)
	}
	intermediateCertPEM, err := os.ReadFile(filepath.Clean(intermediateCertFile))
	if err != nil {
		return fmt.Errorf("read intermediate ca cert: %w", err)
	}
	intermediateKeyPEM, err := os.ReadFile(filepath.Clean(intermediateKeyFile))
	if err != nil {
		return fmt.Errorf("read intermediate ca key: %w", err)
	}

	return m.importCA(ctx, string(rootCertPEM), string(intermediateCertPEM), string(intermediateKeyPEM))
}

func (m *InternalCAManager) importCAFromEnv(ctx context.Context, rootCertB64, intermediateCertB64, intermediateKeyB64 string) error {
	rootCertPEM, err := decodeInternalCAMaterial(rootCertB64, "root ca cert")
	if err != nil {
		return err
	}
	intermediateCertPEM, err := decodeInternalCAMaterial(intermediateCertB64, "intermediate ca cert")
	if err != nil {
		return err
	}
	intermediateKeyPEM, err := decodeInternalCAMaterial(intermediateKeyB64, "intermediate ca key")
	if err != nil {
		return err
	}

	return m.importCA(ctx, rootCertPEM, intermediateCertPEM, intermediateKeyPEM)
}

func (m *InternalCAManager) importCA(ctx context.Context, rootCertPEM, intermediateCertPEM, intermediateKeyPEM string) error {
	rootCert, err := parseCertificatePEM(rootCertPEM)
	if err != nil {
		return fmt.Errorf("parse root ca cert: %w", err)
	}
	intermediateCert, err := parseCertificatePEM(intermediateCertPEM)
	if err != nil {
		return fmt.Errorf("parse intermediate ca cert: %w", err)
	}
	intermediateKey, err := parseECPrivateKeyPEM(intermediateKeyPEM)
	if err != nil {
		return fmt.Errorf("parse intermediate ca key: %w", err)
	}
	if err := validateImportedCAChain(rootCert, intermediateCert, intermediateKey); err != nil {
		return err
	}

	if err := m.store.SaveInternalCA(ctx, &store.InternalCA{
		Role:      internalRootRole,
		CertPEM:   rootCertPEM,
		ExpiresAt: rootCert.NotAfter,
	}); err != nil {
		return fmt.Errorf("save root ca: %w", err)
	}
	if err := m.store.SaveInternalCA(ctx, &store.InternalCA{
		Role:      internalIntermediateRole,
		CertPEM:   intermediateCertPEM,
		KeyPEM:    intermediateKeyPEM,
		ExpiresAt: intermediateCert.NotAfter,
	}); err != nil {
		return fmt.Errorf("save intermediate ca: %w", err)
	}
	return nil
}

func decodeInternalCAMaterial(value, label string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("decode %s base64 env: %w", label, err)
	}
	return string(decoded), nil
}

func validateImportedCAChain(rootCert, intermediateCert *x509.Certificate, intermediateKey *ecdsa.PrivateKey) error {
	now := time.Now()
	if !rootCert.IsCA {
		return fmt.Errorf("root ca cert must be a CA certificate")
	}
	if !intermediateCert.IsCA {
		return fmt.Errorf("intermediate ca cert must be a CA certificate")
	}
	if now.Before(rootCert.NotBefore) || now.After(rootCert.NotAfter) {
		return fmt.Errorf("root ca cert is not currently valid")
	}
	if now.Before(intermediateCert.NotBefore) || now.After(intermediateCert.NotAfter) {
		return fmt.Errorf("intermediate ca cert is not currently valid")
	}
	if !publicKeysEqual(intermediateCert.PublicKey, &intermediateKey.PublicKey) {
		return fmt.Errorf("intermediate ca key does not match intermediate certificate")
	}
	if err := intermediateCert.CheckSignatureFrom(rootCert); err != nil {
		return fmt.Errorf("intermediate ca cert is not signed by root ca cert: %w", err)
	}
	return nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	aBytes, aErr := x509.MarshalPKIXPublicKey(a)
	bBytes, bErr := x509.MarshalPKIXPublicKey(b)
	return aErr == nil && bErr == nil && string(aBytes) == string(bBytes)
}

func (m *InternalCAManager) generateCA(ctx context.Context) error {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate root key: %w", err)
	}
	rootSerial, err := randomSerialNumber()
	if err != nil {
		return fmt.Errorf("generate root serial: %w", err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber: rootSerial,
		Subject: pkix.Name{
			CommonName:   "FrameWorks Internal Root CA",
			Organization: []string{"FrameWorks Internal"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(internalRootValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		return fmt.Errorf("sign root certificate: %w", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return fmt.Errorf("parse generated root certificate: %w", err)
	}

	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate intermediate key: %w", err)
	}
	intermediateSerial, err := randomSerialNumber()
	if err != nil {
		return fmt.Errorf("generate intermediate serial: %w", err)
	}
	intermediateTemplate := &x509.Certificate{
		SerialNumber: intermediateSerial,
		Subject: pkix.Name{
			CommonName:   "FrameWorks Internal Intermediate CA",
			Organization: []string{"FrameWorks Internal"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(internalIntermediateTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
	}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, rootCert, &intermediateKey.PublicKey, rootKey)
	if err != nil {
		return fmt.Errorf("sign intermediate certificate: %w", err)
	}
	intermediateCert, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		return fmt.Errorf("parse generated intermediate certificate: %w", err)
	}

	if err := m.store.SaveInternalCA(ctx, &store.InternalCA{
		Role:      internalRootRole,
		CertPEM:   encodeCertificatePEM(rootDER),
		ExpiresAt: rootCert.NotAfter,
	}); err != nil {
		return fmt.Errorf("save root ca: %w", err)
	}
	if err := m.store.SaveInternalCA(ctx, &store.InternalCA{
		Role:      internalIntermediateRole,
		CertPEM:   encodeCertificatePEM(intermediateDER),
		KeyPEM:    encodeECPrivateKeyPEM(intermediateKey),
		ExpiresAt: intermediateCert.NotAfter,
	}); err != nil {
		return fmt.Errorf("save intermediate ca: %w", err)
	}
	return nil
}

func (m *InternalCAManager) authorizeIssue(ctx context.Context, nodeID, serviceType, issueToken string) (string, error) {
	if m.qm == nil {
		return "", fmt.Errorf("quartermaster client is required for internal certificate authorization")
	}

	resp, err := m.qm.ValidateBootstrapTokenEx(ctx, &pb.ValidateBootstrapTokenRequest{Token: issueToken})
	if err != nil {
		return "", fmt.Errorf("validate issue token: %w", err)
	}
	if !resp.GetValid() {
		return "", fmt.Errorf("issue token rejected: %s", resp.GetReason())
	}
	if kind := resp.GetKind(); kind != "infrastructure_node" && kind != "edge_node" {
		return "", fmt.Errorf("issue token kind %q is not allowed for internal certificate issuance", kind)
	}

	metadata := map[string]interface{}{}
	if resp.GetMetadata() != nil {
		metadata = resp.GetMetadata().AsMap()
	}
	if purpose, _ := metadata["purpose"].(string); purpose != "cert_sync" {
		return "", fmt.Errorf("issue token purpose %q is not allowed", purpose)
	}
	if tokenNodeID, _ := metadata["node_id"].(string); tokenNodeID == "" || tokenNodeID != nodeID {
		return "", fmt.Errorf("issue token is not valid for node %q", nodeID)
	}

	clusterID := strings.TrimSpace(resp.GetClusterId())
	if clusterID == "" {
		return "", fmt.Errorf("issue token is missing cluster binding")
	}
	if err := m.ensureServiceAllowedOnNode(ctx, clusterID, nodeID, serviceType); err != nil {
		return "", err
	}
	return clusterID, nil
}

func (m *InternalCAManager) ensureServiceAllowedOnNode(ctx context.Context, clusterID, nodeID, serviceType string) error {
	pagination := &pb.CursorPaginationRequest{First: 200}
	for {
		resp, err := m.qm.ListServiceInstances(ctx, clusterID, "", nodeID, pagination)
		if err != nil {
			return fmt.Errorf("list node service instances: %w", err)
		}
		for _, instance := range resp.GetInstances() {
			if instance.GetServiceId() != serviceType {
				continue
			}
			if strings.EqualFold(instance.GetStatus(), "stopped") {
				continue
			}
			return nil
		}

		p := resp.GetPagination()
		if p == nil || !p.GetHasNextPage() || p.GetEndCursor() == "" {
			break
		}
		endCursor := p.GetEndCursor()
		pagination = &pb.CursorPaginationRequest{
			First: 200,
			After: &endCursor,
		}
	}
	return fmt.Errorf("service %q is not registered on node %q", serviceType, nodeID)
}

func (m *InternalCAManager) signInternalLeaf(ctx context.Context, serviceType, clusterID string, extraSANs ...string) (string, string, time.Time, error) {
	intermediate, err := m.store.GetInternalCA(ctx, internalIntermediateRole)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("load intermediate ca: %w", err)
	}

	parentCert, err := parseCertificatePEM(intermediate.CertPEM)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("parse intermediate certificate: %w", err)
	}
	parentKey, err := parseECPrivateKeyPEM(intermediate.KeyPEM)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("parse intermediate private key: %w", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate internal certificate key: %w", err)
	}
	serial, err := randomSerialNumber()
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("generate internal certificate serial: %w", err)
	}

	notBefore := time.Now().Add(-5 * time.Minute)
	notAfter := time.Now().Add(internalLeafValidity)
	dnsNames := append(internalCertSANs(serviceType, clusterID, m.rootDomain), extraSANs...)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   serviceType,
			Organization: []string{"FrameWorks Internal"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		DNSNames:              normalizeDomains(dnsNames),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, parentCert, &leafKey.PublicKey, parentKey)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("sign internal certificate: %w", err)
	}
	return encodeCertificatePEM(derBytes), encodeECPrivateKeyPEM(leafKey), notAfter, nil
}

func fileExistsAndNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func internalCertSANs(serviceType, clusterID, rootDomain string) []string {
	sans := []string{
		serviceType,
		serviceType + ".internal",
		"localhost",
	}
	if clusterID != "" && rootDomain != "" {
		sans = append(sans, fmt.Sprintf("%s.%s.%s", serviceType, clusterID, rootDomain))
	}
	return normalizeDomains(sans)
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func parseCertificatePEM(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("missing pem block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseECPrivateKeyPEM(keyPEM string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, fmt.Errorf("missing pem block")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err == nil {
		return key, nil
	}
	pkcs8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecKey, ok := pkcs8.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not ECDSA")
	}
	return ecKey, nil
}

func encodeCertificatePEM(derBytes []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
}

func encodeECPrivateKeyPEM(key *ecdsa.PrivateKey) string {
	derBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: derBytes}))
}
