package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// Bridge is the fixture gateway configuration.
//
//configref:service bridge cmd=cmd/bridge
type Bridge struct {
	config.HTTPListen
	config.ServiceAuth
	Limits

	unexported string
}

// Limits is a local block embedded by Bridge.
type Limits struct {
	MaxDepth int    `env:"GRAPHQL_MAX_DEPTH" default:"10" desc:"Maximum query depth for the fixture." introduced:"v0.3.1"`
	OldDepth string `env:"GRAPHQL_DEPTH" deprecated:"v0.3.1" replacement:"GRAPHQL_MAX_DEPTH" desc:"Deprecated depth key for the fixture." introduced:"v0.3.0"`
}
