package wireguard

// Config represents the desired state of the WireGuard interface
type Config struct {
	PrivateKey string
	Address    string // IP Address with CIDR (e.g., 10.200.0.5/32)
	ListenPort int
	Peers      []Peer
}

// Peer represents a remote WireGuard peer
type Peer struct {
	PublicKey  string
	Endpoint   string
	AllowedIPs []string
	KeepAlive  int
}

// Manager defines the interface for managing the WireGuard device
type Manager interface {
	// Init ensures the interface exists and is up
	Init() error
	// Apply configures the interface with the given config (full sync)
	Apply(cfg Config) error
	// Close tears down the interface (if applicable)
	Close() error
	// GetPublicKey returns the public key of the current private key (or generates one)
	GetPublicKey() (string, error)
	// GetPrivateKey returns the private key (reading from storage)
	GetPrivateKey() (string, error)
}
