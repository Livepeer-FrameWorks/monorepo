package config

type Endpoints struct {
	GatewayURL         string `yaml:"gateway_url"`
	QuartermasterURL   string `yaml:"quartermaster_url"`
	ControlURL         string `yaml:"control_url"`
	FoghornHTTPURL     string `yaml:"foghorn_http_url"`
	FoghornGRPCAddr    string `yaml:"foghorn_grpc_addr"`
	DecklogGRPCAddr    string `yaml:"decklog_grpc_addr"`
	PeriscopeQueryURL  string `yaml:"periscope_query_url"`
	PeriscopeIngestURL string `yaml:"periscope_ingest_url"`
	PurserURL          string `yaml:"purser_url"`
	SignalmanWSURL     string `yaml:"signalman_ws_url"`
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
