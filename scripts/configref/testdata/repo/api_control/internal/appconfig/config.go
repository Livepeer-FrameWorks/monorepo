package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// Commodore is the fixture control configuration.
//
//configref:service commodore cmd=cmd/commodore
type Commodore struct {
	config.HTTPListen
	config.ServiceAuth
	Timeout string `env:"COMMODORE_TIMEOUT" default:"5s" desc:"Timeout value for the fixture." introduced:"v0.3.0"`
}

// CommodoreBootstrap is the fixture bootstrap subcommand configuration.
//
//configref:service commodore cmd=cmd/commodore variant=bootstrap
type CommodoreBootstrap struct {
	config.ServiceAuth
}
