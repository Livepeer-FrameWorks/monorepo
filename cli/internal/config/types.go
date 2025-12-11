package config

type Endpoints struct {
	GatewayURL            string `yaml:"gateway_url"`
	QuartermasterURL      string `yaml:"quartermaster_url"`       // deprecated: use QuartermasterGRPCAddr
	QuartermasterGRPCAddr string `yaml:"quartermaster_grpc_addr"` // gRPC address (host:port)
	ControlURL            string `yaml:"control_url"`             // deprecated: use CommodoreGRPCAddr
	CommodoreGRPCAddr     string `yaml:"commodore_grpc_addr"`     // gRPC address (host:port)
	FoghornHTTPURL        string `yaml:"foghorn_http_url"`        // deprecated
	FoghornGRPCAddr       string `yaml:"foghorn_grpc_addr"`
	DecklogGRPCAddr       string `yaml:"decklog_grpc_addr"`
	PeriscopeQueryURL     string `yaml:"periscope_query_url"`  // deprecated: use PeriscopeGRPCAddr
	PeriscopeGRPCAddr     string `yaml:"periscope_grpc_addr"`  // gRPC address (host:port)
	PeriscopeIngestURL    string `yaml:"periscope_ingest_url"` // deprecated
	PurserURL             string `yaml:"purser_url"`           // deprecated: use PurserGRPCAddr
	PurserGRPCAddr        string `yaml:"purser_grpc_addr"`     // gRPC address (host:port)
	SignalmanWSURL        string `yaml:"signalman_ws_url"`
	SignalmanGRPCAddr     string `yaml:"signalman_grpc_addr"` // gRPC address (host:port)
	NavigatorGRPCAddr     string `yaml:"navigator_grpc_addr"` // gRPC address for DNS/cert service (host:port)

	// TLS configuration for external (non-mesh) connections
	UseTLS        bool   `yaml:"use_tls"`         // Enable TLS for gRPC connections (via Caddy proxy)
	TLSSkipVerify bool   `yaml:"tls_skip_verify"` // Skip TLS certificate verification (dev only!)
	TLSCACert     string `yaml:"tls_ca_cert"`     // Path to CA certificate for custom CAs
}

type Executor struct {
	Type         string `yaml:"type"` // local | ssh
	SSHHost      string `yaml:"ssh_host,omitempty"`
	SSHUser      string `yaml:"ssh_user,omitempty"`
	SSHPort      int    `yaml:"ssh_port,omitempty"`
	ProxyCommand string `yaml:"proxy_command,omitempty"`
}

type Auth struct {
	ServiceToken string `yaml:"service_token,omitempty"`
	JWT          string `yaml:"jwt,omitempty"`
}

type Context struct {
	Name      string    `yaml:"name"`
	Endpoints Endpoints `yaml:"endpoints"`
	Executor  Executor  `yaml:"executor"`
	Auth      Auth      `yaml:"auth"`
}

type Config struct {
	Current  string             `yaml:"current"`
	Contexts map[string]Context `yaml:"contexts"`
}
