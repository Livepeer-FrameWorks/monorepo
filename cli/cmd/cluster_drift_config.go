package cmd

import (
	"frameworks/cli/pkg/artifacts"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
)

// ClusterArtifactsFor returns the desired artifacts for one drift target
// on one host, dispatching per target.Deploy. Application services that
// share the flexible render path use ArtifactsForFlexible; infrastructure
// services each have dedicated renderers.
func ClusterArtifactsFor(target clusterDriftTarget, host inventory.Host, config provisioner.ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	switch target.Deploy {
	case "postgres":
		return provisioner.ArtifactsForPostgres(host, config)
	case "yugabyte":
		return provisioner.ArtifactsForYugabyte(host, config)
	case "clickhouse":
		return provisioner.ArtifactsForClickHouse(host, config)
	case "kafka":
		return provisioner.ArtifactsForKafka(host, config)
	case "kafka-controller":
		return provisioner.ArtifactsForKafkaController(host, config)
	case "zookeeper":
		if config.Mode == "docker" {
			return provisioner.ArtifactsForZookeeperDocker(host, config, imageRef)
		}
		return provisioner.ArtifactsForZookeeperNative(host, config)
	case "caddy":
		return provisioner.ArtifactsForCaddy(host, config, imageRef)
	case "privateer":
		return provisioner.ArtifactsForPrivateer(host, config)
	case "victoriametrics":
		return provisioner.ArtifactsForVictoriaMetrics(host, config, imageRef)
	case "vmagent":
		return provisioner.ArtifactsForVMAgent(host, config, imageRef)
	case "vmauth":
		return provisioner.ArtifactsForVMAuth(host, config, imageRef)
	case "grafana":
		return provisioner.ArtifactsForGrafana(host, config, imageRef)
	}
	if instance, ok := redisInstanceFromDeploy(target.Deploy); ok {
		if config.Mode == "docker" {
			return provisioner.ArtifactsForRedisDocker(host, instance, config, imageRef)
		}
		family := ""
		if v, ok := config.Metadata["distro_family"].(string); ok {
			family = v
		}
		return provisioner.ArtifactsForRedisNative(host, instance, family, config)
	}
	return provisioner.ArtifactsForFlexible(host, target.Deploy, config.Port, config, imageRef)
}

// redisInstanceFromDeploy extracts the instance name from deploy names of
// the form "redis-<instance>". Returns ("", false) for any other deploy.
func redisInstanceFromDeploy(deploy string) (string, bool) {
	const prefix = "redis-"
	if len(deploy) <= len(prefix) || deploy[:len(prefix)] != prefix {
		return "", false
	}
	return deploy[len(prefix):], true
}
