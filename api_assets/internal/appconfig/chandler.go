// Package appconfig holds the typed startup configuration of the Chandler
// asset server. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import (
	"net"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Chandler is the startup configuration of the Chandler asset server. Every
// field applies at startup; a SIGHUP env-file reload updates the process
// environment but does not change a running instance.
//
//configref:service chandler cmd=cmd/chandler
type Chandler struct {
	config.HTTPListen
	config.HTTPRuntime
	config.Logging
	config.Registration
	config.GRPCClientTLS

	ServiceToken string `env:"SERVICE_TOKEN" secret:"true" desc:"Service token for Quartermaster calls and the bearer required by the internal cache-invalidation endpoint. Empty leaves that endpoint unregistered and disables /debug." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" desc:"Quartermaster gRPC address for the cluster storage lookup and registration. Empty joins QUARTERMASTER_HOST and QUARTERMASTER_GRPC_PORT." introduced:"v0.3.0"`
	QuartermasterHost              string `env:"QUARTERMASTER_HOST" default:"quartermaster" desc:"Quartermaster host used when QUARTERMASTER_GRPC_ADDR is empty." introduced:"v0.3.0"`
	QuartermasterGRPCPort          string `env:"QUARTERMASTER_GRPC_PORT" default:"19002" desc:"Quartermaster gRPC port used when QUARTERMASTER_GRPC_ADDR is empty." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	AdvertiseHost                  string `env:"CHANDLER_HOST" default:"chandler" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`

	S3Bucket    string `env:"STORAGE_S3_BUCKET" desc:"S3 bucket served without a CLUSTER_ID when CHANDLER_DEV_ALLOW_ENV_S3 is true. With a CLUSTER_ID the Quartermaster cluster row supplies the bucket." introduced:"v0.3.0"`
	S3Prefix    string `env:"STORAGE_S3_PREFIX" desc:"Object key prefix paired with STORAGE_S3_BUCKET. With a CLUSTER_ID the Quartermaster cluster row supplies the prefix." introduced:"v0.3.0"`
	S3Region    string `env:"STORAGE_S3_REGION" default:"us-east-1" desc:"S3 region paired with STORAGE_S3_BUCKET. With a CLUSTER_ID the Quartermaster cluster row supplies the region." introduced:"v0.3.0"`
	S3Endpoint  string `env:"STORAGE_S3_ENDPOINT" desc:"S3-compatible endpoint paired with STORAGE_S3_BUCKET, addressed path-style. With a CLUSTER_ID the Quartermaster cluster row supplies the endpoint." introduced:"v0.3.0"`
	S3AccessKey string `env:"STORAGE_S3_ACCESS_KEY" secret:"true" desc:"Static S3 access key. Used together with STORAGE_S3_SECRET_KEY; otherwise the AWS default credential chain applies." introduced:"v0.3.0"`
	S3SecretKey string `env:"STORAGE_S3_SECRET_KEY" secret:"true" desc:"Static S3 secret key paired with STORAGE_S3_ACCESS_KEY." introduced:"v0.3.0"`

	DevAllowEnvS3 bool `env:"CHANDLER_DEV_ALLOW_ENV_S3" default:"false" desc:"Serves the STORAGE_S3_* descriptor when CLUSTER_ID is empty. Without it a cluster-less instance refuses to serve an env bucket. Development only." introduced:"v0.3.0"`

	CacheMaxBytes   int `env:"CACHE_MAX_BYTES" default:"52428800" desc:"Maximum total size in bytes of the in-memory object cache." introduced:"v0.3.0"`
	CacheTTLSeconds int `env:"CACHE_TTL_SECONDS" default:"30" desc:"Seconds an object stays in the in-memory cache before it is fetched again." introduced:"v0.3.0"`
}

// QuartermasterAddr resolves the Quartermaster gRPC address.
func (c *Chandler) QuartermasterAddr() string {
	if c.QuartermasterGRPCAddr != "" {
		return c.QuartermasterGRPCAddr
	}
	return net.JoinHostPort(c.QuartermasterHost, c.QuartermasterGRPCPort)
}
