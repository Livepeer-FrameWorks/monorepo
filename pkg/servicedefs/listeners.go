package servicedefs

// Ports of the listeners the platform runs beside its services' primary and
// gRPC ports: third-party data and observability components, their exporters,
// and the auxiliary listeners of FrameWorks services. The CLI's provisioning,
// scrape targets, port-collision accounting, diagnose and doctor read these
// instead of repeating the numbers.
const (
	PostgresPort = 5432

	YugabyteMasterRPCPort  = 7100
	YugabyteMasterWebPort  = 7000
	YugabyteTServerRPCPort = 9100
	YugabyteTServerWebPort = 11000
	YugabyteYSQLPort       = 5433
	YugabyteYCQLPort       = 9042
	// YugabyteYCQLWebPort and YugabyteYSQLWebPort are the tserver's
	// compiled-in CQL proxy and YSQL metrics webservers.
	YugabyteYCQLWebPort = 12000
	YugabyteYSQLWebPort = 13000

	KafkaBrokerPort          = 9092
	KafkaControllerPort      = 9093
	KafkaBrokerJMXPort       = 9405
	KafkaControllerJMXPort   = 9406
	KafkaMirrorMakerRESTPort = 8083
	KafkaMirrorMakerJMXPort  = 9404

	ClickHousePrometheusPort = 9363

	RedisPort         = 6379
	RedisSentinelPort = 26379

	// ExporterPortOffset places a Postgres or Redis exporter at its database
	// port plus this offset.
	ExporterPortOffset = 10000

	NodeExporterPort = 9110

	// DecklogMetricsPort is Decklog's HTTP listener for health, readiness and
	// metrics; its API is gRPC on its primary port.
	DecklogMetricsPort = 18026
	// NetworkObserverInternalPort is network-observer's internal-CA HTTPS
	// copy of its API.
	NetworkObserverInternalPort = 18034

	LivepeerGatewayCLIPort = 7935
	// LivepeerGatewayRTMPPort keeps go-livepeer's RTMP listener, which an empty
	// address would put on Mist's 1935, on its own loopback port.
	LivepeerGatewayRTMPPort = 1936
	LivepeerSignerCLIPort   = 3935

	// CaddyAdminPort is the admin API of a native edge's Caddy.
	CaddyAdminPort = 2019

	// MetabaseContainerPort is where the Metabase image listens inside its
	// container; the host publishes it on Metabase's DefaultPort.
	MetabaseContainerPort = 3000
	ChatwootContainerPort = 3000
	ListmonkContainerPort = 9000

	// PrivateerDNSPort is Privateer's loopback resolver for .internal names.
	PrivateerDNSPort = 53
)

// ExporterPort returns the exporter port of a Postgres or Redis instance
// listening on dbPort.
func ExporterPort(dbPort int) int {
	return dbPort + ExporterPortOffset
}

// ContainerPorts are the in-container listeners of third-party images whose
// host port (DefaultPort) differs from the port the image binds.
var ContainerPorts = map[string]int{
	"metabase": MetabaseContainerPort,
	"chatwoot": ChatwootContainerPort,
	"listmonk": ListmonkContainerPort,
}

// InfrastructurePorts are the default ports of components the CLI provisions
// that are not FrameWorks services in Services.
var InfrastructurePorts = map[string]int{
	"kafka-controller":  KafkaControllerPort,
	"kafka-mirrormaker": KafkaMirrorMakerRESTPort,
	"redis":             RedisPort,
	"yugabyte":          YugabyteYSQLPort,
}

// RegisteredPorts returns every port number this package owns: service
// primary, gRPC and auxiliary ports, Mist's, WireGuard's and the constants
// above. Callers that must not repeat a registered number check against it.
func RegisteredPorts() map[int]bool {
	ports := map[int]bool{}
	for _, svc := range Services {
		if svc.DefaultPort > 0 {
			ports[svc.DefaultPort] = true
		}
	}
	for _, svc := range grpcServices {
		ports[svc.Port] = true
	}
	for _, aux := range auxiliaryPorts {
		for _, spec := range aux {
			ports[spec.Port] = true
		}
	}
	for _, spec := range ClickHousePorts(0, true) {
		ports[spec.Port] = true
	}
	for _, port := range InfrastructurePorts {
		ports[port] = true
	}
	for _, port := range []int{
		FoghornInternalGRPCPort, MistAPIPort, MistRTMPPort, MistDTSCPort, MistRTSPPort, MistSRTPort, MistWebRTCPort,
		WireGuardPort, PostgresPort, YugabyteMasterRPCPort, YugabyteMasterWebPort, YugabyteTServerRPCPort,
		YugabyteTServerWebPort, YugabyteYSQLPort, YugabyteYCQLPort, YugabyteYCQLWebPort, YugabyteYSQLWebPort,
		KafkaBrokerPort, KafkaControllerPort, KafkaBrokerJMXPort, KafkaControllerJMXPort, KafkaMirrorMakerRESTPort,
		KafkaMirrorMakerJMXPort, ClickHousePrometheusPort, RedisPort, RedisSentinelPort, ExporterPort(PostgresPort),
		ExporterPort(RedisPort), NodeExporterPort, DecklogMetricsPort, NetworkObserverInternalPort,
		LivepeerGatewayCLIPort, LivepeerGatewayRTMPPort, LivepeerSignerCLIPort, CaddyAdminPort,
	} {
		ports[port] = true
	}
	return ports
}
