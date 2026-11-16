package config

// SessionRevocationProjection configures internal per-replica auth replication.
type SessionRevocationProjection struct {
	RevocationKafkaBrokers         []string `env:"AUTH_REVOCATION_KAFKA_BROKERS" default:"kafka:@port(kafka)" desc:"Kafka bootstrap brokers for this replica's internal session-revocation projection." introduced:"v0.3.15"`
	RevocationMirrorRegionPrefixes []string `env:"AUTH_REVOCATION_MIRROR_REGION_PREFIXES" desc:"MirrorMaker2 source aliases whose domain.events copies carry Commodore revocations into this replica's Kafka cluster." introduced:"v0.3.15"`
}
