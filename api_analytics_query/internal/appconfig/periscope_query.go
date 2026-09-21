// Package appconfig holds the typed startup configuration of the binaries in
// this module. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// PeriscopeQuery is the startup configuration of the Periscope Query service.
//
//configref:service periscope-query cmd=cmd/periscope
type PeriscopeQuery struct {
	config.HTTPListen
	config.GRPCListen
	config.HTTPRuntime
	config.Logging
	config.ServiceAuth
	config.GRPCTLS
	config.Postgres
	config.ClickHouse
	config.Registration

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address used for service registration." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	AdvertiseHost                  string `env:"PERISCOPE_QUERY_HOST" default:"periscope-query" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`
}
