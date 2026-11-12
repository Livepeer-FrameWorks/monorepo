package config

// SessionRevocationStream configures the background service-only revocation feed.
type SessionRevocationStream struct {
	SignalmanGRPCAddr          string `env:"SIGNALMAN_GRPC_ADDR" default:"signalman:@port(signalman.grpc)" desc:"Signalman address for background interactive-session revocation events." introduced:"v0.3.15"`
	SignalmanGRPCTLSServerName string `env:"SIGNALMAN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Signalman revocation feed." introduced:"v0.3.15"`
}
