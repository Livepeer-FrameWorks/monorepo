// Package appconfig holds the typed startup configuration of the binaries in
// this module. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// PeriscopeQuery is the startup configuration of the Periscope Query service.
//
//configref:service periscope-query cmd=cmd/periscope
type PeriscopeQuery struct {
	config.ListenAddrs
	config.HTTPListen
	config.GRPCListen
	config.HTTPRuntime
	config.Logging
	config.ServiceAuth
	config.GRPCTLS
	config.Postgres
	config.ClickHouse
	config.Registration

	CommodoreGRPCAddr          string `env:"COMMODORE_GRPC_ADDR" default:"commodore:@port(commodore.grpc)" desc:"Commodore gRPC address used to validate interactive user sessions." introduced:"v0.3.15"`
	CommodoreGRPCTLSServerName string `env:"COMMODORE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Commodore session-validation connection." introduced:"v0.3.15"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:@port(quartermaster.grpc)" desc:"Quartermaster gRPC address used for service registration." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	AdvertiseHost                  string `env:"PERISCOPE_QUERY_HOST" default:"periscope-query" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`
}
