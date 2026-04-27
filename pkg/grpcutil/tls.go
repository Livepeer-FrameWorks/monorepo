package grpcutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"frameworks/pkg/config"
	"frameworks/pkg/logging"

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
	CACertFile    string
	CACertPEM     string
	ServerName    string
	AllowInsecure bool
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

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server tls key pair: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}, nil
}

func buildClientTLSConfig(cfg ClientTLSConfig) (*tls.Config, bool, error) {
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
