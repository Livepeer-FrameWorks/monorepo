package config

type HTTPListen struct {
	Port string `env:"PORT" default:"@servicedefs.http_port" desc:"HTTP listener port for the fixture." introduced:"v0.3.0"`
}

type ServiceAuth struct {
	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service token for the fixture." introduced:"v0.3.0"`
}
