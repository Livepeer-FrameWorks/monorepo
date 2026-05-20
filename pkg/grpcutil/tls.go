package grpcutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type ServerTLSConfig struct {
	CertFile      string
	KeyFile       string
	AllowInsecure bool
}

type ClientTLSConfig struct {
	CACertFile        string
	CACertPEM         string
	ServerName        string
	DefaultServerName string
	AllowInsecure     bool
}

func ServerTLS(cfg ServerTLSConfig, logger logging.Logger) (grpc.ServerOption, error) {
	tlsConfig, err := buildServerTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	if tlsConfig == nil {
		logInsecureAllowed(logger, "gRPC server")
		return nil, nil
	}
	return ServerTLSFromConfig(tlsConfig, logger), nil
}

func ServerTLSFromConfig(tlsConfig *tls.Config, logger logging.Logger) grpc.ServerOption {
	if tlsConfig == nil {
		logInsecureAllowed(logger, "gRPC server")
		return nil
	}
	return grpc.Creds(credentials.NewTLS(tlsConfig))
}

func ClientTLS(cfg ClientTLSConfig, logger logging.Logger) (grpc.DialOption, error) {
	creds, err := ClientTransportCredentials(cfg, logger)
	if err != nil {
		return nil, err
	}
	return grpc.WithTransportCredentials(creds), nil
}

func ClientTransportCredentials(cfg ClientTLSConfig, logger logging.Logger) (credentials.TransportCredentials, error) {
	tlsConfig, insecureAllowed, err := buildClientTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	if insecureAllowed {
		logInsecureAllowed(logger, "gRPC client")
		return insecure.NewCredentials(), nil
	}
	return credentials.NewTLS(tlsConfig), nil
}

func buildServerTLSConfig(cfg ServerTLSConfig) (*tls.Config, error) {
	if cfg.AllowInsecure && config.IsProduction() {
		return nil, fmt.Errorf("server TLS cannot use AllowInsecure in production")
	}

	hasCert := cfg.CertFile != ""
	hasKey := cfg.KeyFile != ""
	if hasCert != hasKey {
		return nil, fmt.Errorf("both CertFile and KeyFile are required together")
	}
	if !hasCert {
		if cfg.AllowInsecure {
			return nil, nil
		}
		return nil, fmt.Errorf("server TLS requires CertFile/KeyFile or AllowInsecure=true")
	}

	reloader := &serverCertificateReloader{
		certFile: cfg.CertFile,
		keyFile:  cfg.KeyFile,
	}
	if _, err := reloader.current(); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: reloader.getCertificate,
	}, nil
}

type serverCertificateReloader struct {
	certFile string
	keyFile  string

	mu       sync.RWMutex
	cert     *tls.Certificate
	certStat fileStamp
	keyStat  fileStamp
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

func (r *serverCertificateReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.current()
}

func (r *serverCertificateReloader) current() (*tls.Certificate, error) {
	certStat, keyStat, err := statServerTLSFiles(r.certFile, r.keyFile)
	if err != nil {
		return nil, err
	}

	r.mu.RLock()
	if r.cert != nil && r.certStat == certStat && r.keyStat == keyStat {
		cert := r.cert
		r.mu.RUnlock()
		return cert, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	certStat, keyStat, err = statServerTLSFiles(r.certFile, r.keyFile)
	if err != nil {
		return nil, err
	}
	if r.cert != nil && r.certStat == certStat && r.keyStat == keyStat {
		return r.cert, nil
	}

	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, fmt.Errorf("load server tls key pair: %w", err)
	}
	r.cert = &cert
	r.certStat = certStat
	r.keyStat = keyStat
	return r.cert, nil
}

func statServerTLSFiles(certFile, keyFile string) (fileStamp, fileStamp, error) {
	certInfo, err := os.Stat(certFile)
	if err != nil {
		return fileStamp{}, fileStamp{}, fmt.Errorf("stat server TLS cert %q: %w", certFile, err)
	}
	keyInfo, err := os.Stat(keyFile)
	if err != nil {
		return fileStamp{}, fileStamp{}, fmt.Errorf("stat server TLS key %q: %w", keyFile, err)
	}
	return fileStamp{modTime: certInfo.ModTime(), size: certInfo.Size()},
		fileStamp{modTime: keyInfo.ModTime(), size: keyInfo.Size()},
		nil
}

func buildClientTLSConfig(cfg ClientTLSConfig) (*tls.Config, bool, error) {
	cfg.CACertFile = strings.TrimSpace(cfg.CACertFile)
	cfg.CACertPEM = strings.TrimSpace(cfg.CACertPEM)
	cfg.ServerName = strings.TrimSpace(cfg.ServerName)
	cfg.DefaultServerName = strings.TrimSpace(cfg.DefaultServerName)
	if cfg.AllowInsecure && config.IsProduction() {
		return nil, false, fmt.Errorf("client TLS cannot use AllowInsecure in production")
	}

	if cfg.AllowInsecure && cfg.CACertFile == "" && cfg.CACertPEM == "" && cfg.ServerName == "" {
		return nil, true, nil
	}

	var (
		rootCAs *x509.CertPool
		err     error
	)
	if cfg.CACertFile != "" || cfg.CACertPEM != "" {
		rootCAs = x509.NewCertPool()
	}
	if cfg.CACertFile != "" {
		pem, readErr := os.ReadFile(cfg.CACertFile)
		if readErr != nil {
			return nil, false, fmt.Errorf("read CA cert %q: %w", cfg.CACertFile, readErr)
		}
		if !rootCAs.AppendCertsFromPEM(pem) {
			return nil, false, fmt.Errorf("append CA cert %q: invalid PEM", cfg.CACertFile)
		}
	}
	if cfg.CACertPEM != "" {
		if !rootCAs.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, false, fmt.Errorf("append inline CA cert: invalid PEM")
		}
	}
	if rootCAs != nil && cfg.ServerName == "" {
		cfg.ServerName = cfg.DefaultServerName
	}
	if rootCAs != nil && cfg.ServerName == "" {
		return nil, false, fmt.Errorf("client TLS with custom CA requires ServerName or DefaultServerName")
	}
	if rootCAs == nil {
		rootCAs, err = x509.SystemCertPool()
		if err != nil {
			return nil, false, fmt.Errorf("load system cert pool: %w", err)
		}
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
		ServerName: cfg.ServerName,
	}, false, nil
}

func logInsecureAllowed(logger logging.Logger, component string) {
	if logger == nil {
		return
	}
	logger.WithField("component", component).Warn("TLS disabled via AllowInsecure")
}
