package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"time"

	"frameworks/api_dns/internal/store"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
)

type hostedBundleReader interface {
	GetTLSBundleCertificate(context.Context, string) (string, error)
}

var errHostedCertificateRevision = errors.New("hosted playback serves a different valid certificate revision")

func hostedCertificateRejected(err error) bool {
	var verification *tls.CertificateVerificationError
	var header tls.RecordHeaderError
	return errors.As(err, &verification) || errors.As(err, &header)
}

// hostedCertificateChecker reads the public leaf once per publication pass.
// New DNS members must serve that revision. Existing members may serve a prior
// still-trusted certificate for the same alias while Privateer rotates hosts.
func (w *AliasApplyStateWorker) hostedCertificateChecker(ctx context.Context, alias store.TenantAlias) (func(context.Context, store.TenantAlias, ServiceAddress) error, error) {
	reader, ok := w.store.(hostedBundleReader)
	if !ok {
		return nil, errors.New("tenant certificate reader unavailable")
	}
	certificate, err := reader.GetTLSBundleCertificate(ctx, "tenant:"+alias.TenantID)
	if err != nil {
		return nil, fmt.Errorf("read current tenant certificate: %w", err)
	}
	block, _ := pem.Decode([]byte(certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("current tenant certificate invalid")
	}
	label, ok := pkgdns.PublicSubdomain("foghorn")
	if !ok || label == "" {
		return nil, errors.New("hosted playback service label unavailable")
	}
	name := label + "." + alias.Subdomain + "." + w.tenantZoneLabel + "." + w.rootDomain
	return func(ctx context.Context, _ store.TenantAlias, address ServiceAddress) error {
		return verifyHostedCertificate(ctx, net.JoinHostPort(address.IP, "443"), name, block.Bytes, nil)
	}, nil
}

func verifyHostedCertificate(ctx context.Context, address, name string, expected []byte, roots *x509.CertPool) error {
	// This is an asynchronous DNS eligibility probe, never an admission call.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	dialer := &tls.Dialer{Config: &tls.Config{ServerName: name, RootCAs: roots, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("verify hosted playback TLS: %w", err)
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return errors.New("hosted playback did not negotiate TLS")
	}
	peers := tlsConn.ConnectionState().PeerCertificates
	if len(peers) == 0 {
		return errors.New("hosted playback served no certificate")
	}
	if !bytes.Equal(peers[0].Raw, expected) {
		return errHostedCertificateRevision
	}
	return nil
}
