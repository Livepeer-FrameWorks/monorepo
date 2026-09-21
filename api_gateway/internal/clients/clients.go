package clients

import (
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	bosunclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/bosun"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/deckhand"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	lookoutclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/lookout"
	navclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	skipperclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/skipper"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// ServiceClients holds all downstream service gRPC clients. Fields are interfaces
// (satisfied by the concrete *GRPCClient/*Client) so tests can inject fakes and
// exercise resolver real paths without a live backend.
type ServiceClients struct {
	Bosun         bosunclient.Interface
	Commodore     commodore.Interface
	Deckhand      deckhand.Interface
	Decklog       decklog.Interface
	Lookout       lookoutclient.Interface
	Navigator     navclient.Interface
	Periscope     periscope.Interface
	Purser        purser.Interface
	Quartermaster quartermaster.Interface
	Signalman     signalman.Interface
	Skipper       skipperclient.Interface
}

// Endpoint is one downstream gRPC service.
type Endpoint struct {
	Addr string
	// TLSServerName overrides the canonical internal TLS name when non-empty.
	TLSServerName string
}

// Config represents the configuration for all service clients
type Config struct {
	ServiceToken  string
	JWTSecret     []byte
	Timeout       time.Duration
	Logger        logging.Logger
	AllowInsecure bool
	CACertFile    string

	Commodore     Endpoint
	Periscope     Endpoint
	Purser        Endpoint
	Quartermaster Endpoint
	Signalman     Endpoint
	Decklog       Endpoint
	// Navigator, Deckhand, Skipper, Lookout, and Bosun are optional: an empty
	// Addr leaves the client unset.
	Navigator Endpoint
	Deckhand  Endpoint
	Skipper   Endpoint
	Lookout   Endpoint
	Bosun     Endpoint

	// QuartermasterCache backs the Commodore client's Quartermaster lookups.
	QuartermasterCache cache.Options
	// ClusterID and Region label API usage events sent to Decklog.
	ClusterID string
	Region    string
}

// NewServiceClients creates and initializes all downstream service gRPC clients
func NewServiceClients(cfg Config) (*ServiceClients, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	grpcAllowInsecure := cfg.AllowInsecure
	grpcCACertFile := cfg.CACertFile

	qmCache := cache.New(cfg.QuartermasterCache, cache.MetricsHooks{})

	// Initialize Commodore gRPC client
	commodoreClient, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:           cfg.Commodore.Addr,
		Timeout:            cfg.Timeout,
		Logger:             cfg.Logger,
		Cache:              qmCache,
		ServiceToken:       cfg.ServiceToken,
		DelegatedJWTSecret: cfg.JWTSecret,
		AllowInsecure:      grpcAllowInsecure,
		CACertFile:         grpcCACertFile,
		ServerName:         cfg.Commodore.TLSServerName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Commodore gRPC client: %w", err)
	}

	// Initialize Periscope gRPC client
	periscopeClient, err := periscope.NewGRPCClient(periscope.GRPCConfig{
		GRPCAddr:           cfg.Periscope.Addr,
		Timeout:            cfg.Timeout,
		Logger:             cfg.Logger,
		ServiceToken:       cfg.ServiceToken,
		DelegatedJWTSecret: cfg.JWTSecret,
		AllowInsecure:      grpcAllowInsecure,
		CACertFile:         grpcCACertFile,
		ServerName:         cfg.Periscope.TLSServerName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Periscope gRPC client: %w", err)
	}

	// Initialize Purser gRPC client
	purserClient, err := purser.NewGRPCClient(purser.GRPCConfig{
		GRPCAddr:           cfg.Purser.Addr,
		Timeout:            cfg.Timeout,
		Logger:             cfg.Logger,
		ServiceToken:       cfg.ServiceToken,
		DelegatedJWTSecret: cfg.JWTSecret,
		AllowInsecure:      grpcAllowInsecure,
		CACertFile:         grpcCACertFile,
		ServerName:         cfg.Purser.TLSServerName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Purser gRPC client: %w", err)
	}

	// Initialize Quartermaster gRPC client
	quartermasterClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:           cfg.Quartermaster.Addr,
		Timeout:            cfg.Timeout,
		Logger:             cfg.Logger,
		ServiceToken:       cfg.ServiceToken,
		DelegatedJWTSecret: cfg.JWTSecret,
		AllowInsecure:      grpcAllowInsecure,
		CACertFile:         grpcCACertFile,
		ServerName:         cfg.Quartermaster.TLSServerName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Quartermaster gRPC client: %w", err)
	}

	var navigatorClient *navclient.Client
	if navigatorAddr := cfg.Navigator.Addr; navigatorAddr != "" {
		navigatorClient, err = navclient.NewClient(navclient.Config{
			Addr:          navigatorAddr,
			Timeout:       cfg.Timeout,
			Logger:        cfg.Logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: grpcAllowInsecure,
			CACertFile:    grpcCACertFile,
			ServerName:    cfg.Navigator.TLSServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Navigator gRPC client: %w", err)
		}
	}

	// Initialize Signalman gRPC client
	signalmanClient, err := signalman.NewGRPCClient(signalman.GRPCConfig{
		GRPCAddr:      cfg.Signalman.Addr,
		Timeout:       cfg.Timeout,
		Logger:        cfg.Logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: grpcAllowInsecure,
		CACertFile:    grpcCACertFile,
		ServerName:    cfg.Signalman.TLSServerName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Signalman gRPC client: %w", err)
	}

	// Initialize Decklog gRPC client (for API usage tracking)
	decklogClient, err := decklog.NewBatchedClient(decklog.BatchedClientConfig{
		Target:        cfg.Decklog.Addr,
		AllowInsecure: grpcAllowInsecure,
		CACertFile:    grpcCACertFile,
		ServerName:    cfg.Decklog.TLSServerName,
		Timeout:       cfg.Timeout,
		Source:        "bridge",
		ServiceToken:  cfg.ServiceToken,
		ClusterID:     cfg.ClusterID,
		SourceRegion:  cfg.Region,
	}, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create Decklog gRPC client: %w", err)
	}

	// Initialize Deckhand gRPC client (for support messaging)
	// Optional: only initialize if DECKHAND_GRPC_ADDR is configured
	var deckhandClient *deckhand.GRPCClient
	if deckhandAddr := cfg.Deckhand.Addr; deckhandAddr != "" {
		deckhandClient, err = deckhand.NewGRPCClient(deckhand.GRPCConfig{
			GRPCAddr:      deckhandAddr,
			Timeout:       cfg.Timeout,
			Logger:        cfg.Logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: grpcAllowInsecure,
			CACertFile:    grpcCACertFile,
			ServerName:    cfg.Deckhand.TLSServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Deckhand gRPC client: %w", err)
		}
	}

	// Initialize Skipper gRPC client (for AI consultant)
	// Optional: only initialize if SKIPPER_GRPC_ADDR is configured
	var skipperClient *skipperclient.GRPCClient
	if skipperAddr := cfg.Skipper.Addr; skipperAddr != "" {
		skipperClient, err = skipperclient.NewGRPCClient(skipperclient.GRPCConfig{
			GRPCAddr:      skipperAddr,
			Timeout:       cfg.Timeout,
			Logger:        cfg.Logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: grpcAllowInsecure,
			CACertFile:    grpcCACertFile,
			ServerName:    cfg.Skipper.TLSServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Skipper gRPC client: %w", err)
		}
	}

	// Lookout gRPC client (incidents). Optional: only when LOOKOUT_GRPC_ADDR is set.
	var lookoutClient *lookoutclient.GRPCClient
	if lookoutAddr := cfg.Lookout.Addr; lookoutAddr != "" {
		lookoutClient, err = lookoutclient.NewGRPCClient(lookoutclient.GRPCConfig{
			GRPCAddr:      lookoutAddr,
			Timeout:       cfg.Timeout,
			Logger:        cfg.Logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: grpcAllowInsecure,
			CACertFile:    grpcCACertFile,
			ServerName:    cfg.Lookout.TLSServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Lookout gRPC client: %w", err)
		}
	}

	// Bosun gRPC client (outbound webhooks). Optional: only when BOSUN_GRPC_ADDR is set.
	var bosunClient *bosunclient.GRPCClient
	if bosunAddr := cfg.Bosun.Addr; bosunAddr != "" {
		bosunClient, err = bosunclient.NewGRPCClient(bosunclient.GRPCConfig{
			GRPCAddr:      bosunAddr,
			Timeout:       cfg.Timeout,
			Logger:        cfg.Logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: grpcAllowInsecure,
			CACertFile:    grpcCACertFile,
			ServerName:    cfg.Bosun.TLSServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create Bosun gRPC client: %w", err)
		}
	}

	sc := &ServiceClients{
		Commodore:     commodoreClient,
		Decklog:       decklogClient,
		Periscope:     periscopeClient,
		Purser:        purserClient,
		Quartermaster: quartermasterClient,
		Signalman:     signalmanClient,
	}
	// Optional clients: assign only when constructed. Storing a nil concrete
	// pointer in the interface field would make it non-nil (typed nil), so
	// callers' nil checks would pass and calls would panic.
	if navigatorClient != nil {
		sc.Navigator = navigatorClient
	}
	if deckhandClient != nil {
		sc.Deckhand = deckhandClient
	}
	if skipperClient != nil {
		sc.Skipper = skipperClient
	}
	if lookoutClient != nil {
		sc.Lookout = lookoutClient
	}
	if bosunClient != nil {
		sc.Bosun = bosunClient
	}
	return sc, nil
}

// Close closes all gRPC connections
func (c *ServiceClients) Close() error {
	var errs []error

	if c.Bosun != nil {
		if err := c.Bosun.Close(); err != nil {
			errs = append(errs, fmt.Errorf("bosun: %w", err))
		}
	}

	if c.Commodore != nil {
		if err := c.Commodore.Close(); err != nil {
			errs = append(errs, fmt.Errorf("commodore: %w", err))
		}
	}
	if c.Deckhand != nil {
		if err := c.Deckhand.Close(); err != nil {
			errs = append(errs, fmt.Errorf("deckhand: %w", err))
		}
	}
	if c.Decklog != nil {
		if err := c.Decklog.Close(); err != nil {
			errs = append(errs, fmt.Errorf("decklog: %w", err))
		}
	}
	if c.Lookout != nil {
		if err := c.Lookout.Close(); err != nil {
			errs = append(errs, fmt.Errorf("lookout: %w", err))
		}
	}
	if c.Navigator != nil {
		if err := c.Navigator.Close(); err != nil {
			errs = append(errs, fmt.Errorf("navigator: %w", err))
		}
	}
	if c.Periscope != nil {
		if err := c.Periscope.Close(); err != nil {
			errs = append(errs, fmt.Errorf("periscope: %w", err))
		}
	}
	if c.Purser != nil {
		if err := c.Purser.Close(); err != nil {
			errs = append(errs, fmt.Errorf("purser: %w", err))
		}
	}
	if c.Quartermaster != nil {
		if err := c.Quartermaster.Close(); err != nil {
			errs = append(errs, fmt.Errorf("quartermaster: %w", err))
		}
	}
	if c.Signalman != nil {
		if err := c.Signalman.Close(); err != nil {
			errs = append(errs, fmt.Errorf("signalman: %w", err))
		}
	}
	if c.Skipper != nil {
		if err := c.Skipper.Close(); err != nil {
			errs = append(errs, fmt.Errorf("skipper: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing clients: %v", errs)
	}
	return nil
}
