package provisioner

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/artifacts"
	"frameworks/cli/pkg/inventory"
)

// ServiceRuntimeIgnoreKeys returns env keys that apply-time injects from
// runtime data (minted tokens, host-discovered values). Drift excludes
// these from env comparison so they don't report permanent false drift.
// The returned slice is safe to pass directly as DesiredArtifact.IgnoreKeys.
func ServiceRuntimeIgnoreKeys(serviceName string) []string {
	switch serviceName {
	case "privateer":
		return []string{"ENROLLMENT_TOKEN", "CERT_ISSUANCE_TOKEN", "UPSTREAM_DNS"}
	case "foghorn", "vmauth":
		return []string{"EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64", "EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64"}
	}
	return nil
}

// BuildServiceEnvFileBytes returns the env-file content FlexibleProvisioner
// writes to /etc/frameworks/<service>.env. Keys are sorted; empty config
// yields nil.
func BuildServiceEnvFileBytes(config ServiceConfig) []byte {
	envVars := config.EnvVars
	if len(envVars) == 0 {
		envVars = map[string]string{}
		if clusterID, ok := config.Metadata["cluster_id"].(string); ok && clusterID != "" {
			envVars["CLUSTER_ID"] = clusterID
		}
		if nodeID, ok := config.Metadata["node_id"].(string); ok && nodeID != "" {
			envVars["NODE_ID"] = nodeID
		}
	}
	if len(envVars) == 0 {
		return nil
	}
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, envVars[k]))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// ServiceUsesCustomCompose reports whether a deploy name uses buildCustomCompose
// rather than GenerateDockerCompose. ArtifactsForFlexible skips the compose
// artifact for these services; dedicated ArtifactsFor<Name> functions handle
// them with the matching compose builder.
func ServiceUsesCustomCompose(deployName string) bool {
	switch deployName {
	case "victoriametrics", "vmagent", "vmauth", "grafana":
		return true
	}
	return false
}

// ArtifactsForFlexible returns the files FlexibleProvisioner writes on a
// host for the given config. Empty env files are omitted so drift does
// not report spurious missing_on_host for services that write nothing.
func ArtifactsForFlexible(host inventory.Host, serviceName string, port int, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	effectivePort := port
	if config.Port != 0 {
		effectivePort = config.Port
	}
	envPath := fmt.Sprintf("/etc/frameworks/%s.env", serviceName)
	envBytes := BuildServiceEnvFileBytes(config)

	var out []artifacts.DesiredArtifact
	if len(envBytes) > 0 {
		out = append(out, artifacts.DesiredArtifact{
			Path:       envPath,
			Kind:       artifacts.KindEnv,
			Content:    envBytes,
			IgnoreKeys: ServiceRuntimeIgnoreKeys(serviceName),
		})
	}

	if config.Mode == "native" {
		unit, err := GenerateSystemdUnit(SystemdUnitData{
			ServiceName: serviceName,
			Description: fmt.Sprintf("Frameworks %s", serviceName),
			WorkingDir:  fmt.Sprintf("/opt/frameworks/%s", serviceName),
			ExecStart:   fmt.Sprintf("/opt/frameworks/%s/%s", serviceName, serviceName),
			User:        "frameworks",
			EnvFile:     envPath,
			After:       []string{"network-online"},
		})
		if err == nil {
			out = append(out, artifacts.DesiredArtifact{
				Path:    fmt.Sprintf("/etc/systemd/system/frameworks-%s.service", serviceName),
				Kind:    artifacts.KindFileHash,
				Content: []byte(unit),
			})
		}
		return out
	}

	if ServiceUsesCustomCompose(serviceName) || imageRef == "" {
		return out
	}

	envFile := config.EnvFile
	if envFile == "" {
		envFile = envPath
	}
	composeEnv := maps.Clone(config.EnvVars)
	if composeEnv == nil {
		composeEnv = map[string]string{}
	}
	if len(composeEnv) == 0 {
		if clusterID, ok := config.Metadata["cluster_id"].(string); ok && clusterID != "" {
			composeEnv["CLUSTER_ID"] = clusterID
		}
		if nodeID, ok := config.Metadata["node_id"].(string); ok && nodeID != "" {
			composeEnv["NODE_ID"] = nodeID
		}
	}
	compose, err := GenerateDockerCompose(DockerComposeData{
		ServiceName: serviceName,
		Image:       imageRef,
		Port:        effectivePort,
		EnvFile:     envFile,
		Environment: composeEnv,
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", "curl", "-f", fmt.Sprintf("http://localhost:%d/health", effectivePort)},
			Interval: "30s",
			Timeout:  "10s",
			Retries:  3,
		},
		Networks: []string{"frameworks"},
		Volumes: []string{
			fmt.Sprintf("/var/log/frameworks/%s:/var/log/frameworks", serviceName),
			fmt.Sprintf("/var/lib/frameworks/%s:/var/lib/frameworks", serviceName),
		},
	})
	if err == nil {
		out = append(out, artifacts.DesiredArtifact{
			Path:    fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", serviceName),
			Kind:    artifacts.KindFileHash,
			Content: []byte(compose),
		})
	}
	return out
}

// ArtifactsForCaddy returns the files CaddyProvisioner writes. Native mode
// produces a ManagedInvariant for /etc/caddy/Caddyfile since the provisioner
// only asserts one import line exists in a package-owned file.
func ArtifactsForCaddy(host inventory.Host, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	if config.Mode == "native" {
		return []artifacts.DesiredArtifact{{
			Path:      "/etc/caddy/Caddyfile",
			Kind:      artifacts.KindManagedInvariant,
			Invariant: &artifacts.Invariant{MustContain: [][]byte{[]byte("/etc/caddy/conf.d/*.caddyfile")}},
		}}
	}

	email := metaString(config.Metadata, "acme_email")
	if email == "" {
		email = "caddy@example.com"
	}
	rootDomain := metaString(config.Metadata, "root_domain")
	if rootDomain == "" {
		return nil
	}

	envContent := GenerateEnvFile("caddy", map[string]string{
		"CADDY_EMAIL":       email,
		"CADDY_ROOT_DOMAIN": rootDomain,
	})
	out := []artifacts.DesiredArtifact{{
		Path:    "/etc/frameworks/caddy.env",
		Kind:    artifacts.KindEnv,
		Content: []byte(envContent),
	}}

	listenAddr := ":80"
	if config.Port != 0 {
		listenAddr = fmt.Sprintf(":%d", config.Port)
	}
	routes := localServicePorts(config.Metadata)
	proxyRoutes := BuildLocalProxyRoutes(rootDomain, routes)
	proxyRoutes = append(proxyRoutes, BuildExtraProxyRoutes(config.Metadata["extra_proxy_routes"])...)
	caddyfile, err := GenerateCentralCaddyfile(CaddyfileData{
		Email:         email,
		RootDomain:    rootDomain,
		ListenAddress: listenAddr,
		Routes:        proxyRoutes,
	})
	if err == nil {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/etc/frameworks/caddy/Caddyfile",
			Kind:    artifacts.KindFileHash,
			Content: []byte(caddyfile),
		})
	}

	compose, err := GenerateDockerCompose(DockerComposeData{
		ServiceName: "caddy",
		Image:       imageRef,
		EnvFile:     "/etc/frameworks/caddy.env",
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", "curl", "-f", "http://localhost:2019/metrics"},
			Interval: "30s",
			Timeout:  "10s",
			Retries:  3,
		},
		Networks: []string{"frameworks"},
		Ports:    []string{"80:80", "443:443"},
		Volumes: []string{
			"/etc/frameworks/caddy/Caddyfile:/etc/caddy/Caddyfile",
			"/var/lib/frameworks/caddy/data:/data",
			"/var/lib/frameworks/caddy/config:/config",
		},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	})
	if err == nil {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/opt/frameworks/caddy/docker-compose.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(compose),
		})
	}
	return out
}

// ArtifactsForRedisDocker returns the files RedisProvisioner writes in
// docker mode for one instance. Native mode artifacts are produced by a
// separate extraction path (see redis.go's GenerateRedisPlaybook).
func ArtifactsForRedisDocker(host inventory.Host, instanceName string, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	serviceName := fmt.Sprintf("redis-%s", instanceName)
	password := metaString(config.Metadata, "password")
	port := config.Port
	if port == 0 {
		port = 6379
	}

	envVars := map[string]string{}
	if password != "" {
		envVars["REDIS_PASSWORD"] = password
		envVars["REDISCLI_AUTH"] = password
	}
	envContent := GenerateEnvFile(serviceName, envVars)
	out := []artifacts.DesiredArtifact{{
		Path:       fmt.Sprintf("/etc/frameworks/%s.env", serviceName),
		Kind:       artifacts.KindEnv,
		Content:    []byte(envContent),
		IgnoreKeys: []string{"REDIS_PASSWORD", "REDISCLI_AUTH"},
	}}

	engine, err := resolveRedisEngine(config.Metadata)
	if err != nil {
		return out
	}

	redisConf := buildRedisConf(password)
	volumes := []string{fmt.Sprintf("/var/lib/frameworks/%s:/data", serviceName)}
	cmdArgs := buildRedisCommandArgs(engine, config.Metadata)
	if redisConf != "" {
		confPath := fmt.Sprintf("/etc/frameworks/%s.conf", serviceName)
		volumes = append(volumes, fmt.Sprintf("%s:/etc/redis/redis.conf:ro", confPath))
		cmdArgs += " /etc/redis/redis.conf"
		out = append(out, artifacts.DesiredArtifact{
			Path:    confPath,
			Kind:    artifacts.KindFileHash,
			Content: []byte(redisConf),
		})
	}

	compose, err := GenerateDockerCompose(DockerComposeData{
		ServiceName: serviceName,
		Image:       imageRef,
		Port:        port,
		EnvFile:     fmt.Sprintf("/etc/frameworks/%s.env", serviceName),
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", redisCLIName(engine), "ping"},
			Interval: "10s",
			Timeout:  "5s",
			Retries:  5,
		},
		Networks: []string{"frameworks"},
		Volumes:  volumes,
	})
	if err == nil {
		if cmdArgs != "" {
			compose = appendComposeCommand(compose, serviceName, cmdArgs)
		}
		out = append(out, artifacts.DesiredArtifact{
			Path:    fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", serviceName),
			Kind:    artifacts.KindFileHash,
			Content: []byte(compose),
		})
	}
	return out
}

// ArtifactsForPostgres declares the FrameWorks-managed sections of
// postgresql.conf and pg_hba.conf. The provisioner only owns the content
// between the "# frameworks managed begin/end" markers, so drift asserts
// the managed section's invariants (key directives are present) rather
// than the whole file.
func ArtifactsForPostgres(host inventory.Host, config ServiceConfig) []artifacts.DesiredArtifact {
	confInvariants := [][]byte{
		[]byte("# frameworks managed begin"),
		[]byte("# frameworks managed end"),
		[]byte("listen_addresses = '*'"),
		[]byte("password_encryption = 'scram-sha-256'"),
	}
	hbaInvariants := [][]byte{
		[]byte("# frameworks managed begin"),
		[]byte("# frameworks managed end"),
		[]byte("host all all 127.0.0.1/32 scram-sha-256"),
		[]byte("host all all 0.0.0.0/0 scram-sha-256"),
	}
	return []artifacts.DesiredArtifact{
		{Path: "/etc/postgresql/postgresql.conf", Kind: artifacts.KindManagedInvariant, Invariant: &artifacts.Invariant{MustContain: confInvariants}},
		{Path: "/etc/postgresql/pg_hba.conf", Kind: artifacts.KindManagedInvariant, Invariant: &artifacts.Invariant{MustContain: hbaInvariants}},
	}
}

// ArtifactsForKafka returns the files written by either combined or dedicated
// broker mode. Controller targets are covered by ArtifactsForKafkaController.
func ArtifactsForKafka(host inventory.Host, config ServiceConfig) []artifacts.DesiredArtifact {
	nodeID := metaIntOr(config.Metadata, "broker_id", 0)
	port := config.Port
	if port == 0 {
		port = 9092
	}
	brokerCount := max(metaIntOr(config.Metadata, "broker_count", 1), 1)
	defaultRF := min(brokerCount, 3)
	role := metaString(config.Metadata, "role")
	bootstrapServers := metaString(config.Metadata, "bootstrap_servers")
	clusterQuorum := metaString(config.Metadata, "controller_quorum_voters")

	var serverProps []byte
	if role == "broker" {
		serverProps = ansible.BuildKafkaBrokerServerProperties(ansible.KafkaBrokerParams{
			NodeID:           nodeID,
			ListenerHost:     host.ExternalIP,
			ListenerPort:     port,
			BootstrapServers: bootstrapServers,
			MinISR:           max(defaultRF-1, 1),
			OffsetsRF:        max(defaultRF, 1),
			TxRF:             max(defaultRF, 1),
			TxMinISR:         max(defaultRF-1, 1),
		})
	} else {
		controllerPort := metaIntOr(config.Metadata, "controller_port", 9093)
		if controllerPort <= 0 {
			controllerPort = 9093
		}
		serverProps = ansible.BuildKafkaCombinedServerProperties(ansible.KafkaCombinedParams{
			NodeID:           nodeID,
			ListenerHost:     host.ExternalIP,
			ListenerPort:     port,
			ControllerPort:   controllerPort,
			ControllerQuorum: clusterQuorum,
			MinISR:           max(defaultRF-1, 1),
			OffsetsRF:        max(defaultRF, 1),
			TxRF:             max(defaultRF, 1),
			TxMinISR:         max(defaultRF-1, 1),
		})
	}
	return []artifacts.DesiredArtifact{
		{Path: "/etc/kafka/server.properties", Kind: artifacts.KindFileHash, Content: serverProps},
		{Path: "/etc/systemd/system/frameworks-kafka.service", Kind: artifacts.KindFileHash, Content: ansible.BuildKafkaBrokerUnit()},
	}
}

// ArtifactsForKafkaController returns the files written by dedicated controller mode.
func ArtifactsForKafkaController(host inventory.Host, config ServiceConfig) []artifacts.DesiredArtifact {
	nodeID := metaIntOr(config.Metadata, "broker_id", 0)
	controllerPort := config.Port
	if controllerPort == 0 {
		controllerPort = 9093
	}
	bootstrapServers := metaString(config.Metadata, "bootstrap_servers")
	serverProps := ansible.BuildKafkaControllerServerProperties(ansible.KafkaControllerParams{
		NodeID:           nodeID,
		ControllerPort:   controllerPort,
		BootstrapServers: bootstrapServers,
	})
	return []artifacts.DesiredArtifact{
		{Path: "/etc/kafka-controller/server.properties", Kind: artifacts.KindFileHash, Content: serverProps},
		{Path: "/etc/systemd/system/frameworks-kafka-controller.service", Kind: artifacts.KindFileHash, Content: ansible.BuildKafkaControllerUnit()},
	}
}

// ArtifactsForPrivateer returns the files PrivateerProvisioner writes.
// Runtime-injected env keys (enrollment / cert issuance tokens, host-
// discovered UPSTREAM_DNS) are declared in IgnoreKeys.
func ArtifactsForPrivateer(host inventory.Host, config ServiceConfig) []artifacts.DesiredArtifact {
	inputs := privateerInputsFromConfig(host, config)
	out := []artifacts.DesiredArtifact{
		{
			Path:       "/etc/frameworks/privateer.env",
			Kind:       artifacts.KindEnv,
			Content:    BuildPrivateerEnv(inputs),
			IgnoreKeys: []string{"ENROLLMENT_TOKEN", "CERT_ISSUANCE_TOKEN", "UPSTREAM_DNS"},
		},
	}
	if unit, err := BuildPrivateerSystemdUnit(); err == nil {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/etc/systemd/system/frameworks-privateer.service",
			Kind:    artifacts.KindFileHash,
			Content: unit,
		})
	}
	return out
}

// ArtifactsForVictoriaMetrics returns the files VictoriaMetricsProvisioner
// writes. Password file is KindFileHash so drift never emits the content.
func ArtifactsForVictoriaMetrics(host inventory.Host, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	var out []artifacts.DesiredArtifact
	if env := BuildServiceEnvFileBytes(config); len(env) > 0 {
		out = append(out, artifacts.DesiredArtifact{
			Path:       "/etc/frameworks/victoriametrics.env",
			Kind:       artifacts.KindEnv,
			Content:    env,
			IgnoreKeys: []string{"VM_HTTP_AUTH_PASSWORD"},
		})
	}
	if password := strings.TrimSpace(config.EnvVars["VM_HTTP_AUTH_PASSWORD"]); password != "" {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/etc/frameworks/victoriametrics.password",
			Kind:    artifacts.KindFileHash,
			Content: []byte(password + "\n"),
		})
	}
	if imageRef != "" {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/opt/frameworks/victoriametrics/docker-compose.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(buildCustomCompose("victoriametrics", imageRef, buildVictoriaMetricsComposeOptions(config))),
		})
	}
	return out
}

// ArtifactsForVMAgent returns the files VMAgentProvisioner writes.
func ArtifactsForVMAgent(host inventory.Host, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	var out []artifacts.DesiredArtifact
	if env := BuildServiceEnvFileBytes(config); len(env) > 0 {
		out = append(out, artifacts.DesiredArtifact{
			Path:       "/etc/frameworks/vmagent.env",
			Kind:       artifacts.KindEnv,
			Content:    env,
			IgnoreKeys: []string{"VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD"},
		})
	}
	if scrape, err := buildVMAgentScrapeConfig(config.Metadata["scrape_targets"], config.EnvVars["VMAGENT_SCRAPE_INTERVAL"]); err == nil {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/etc/frameworks/vmagent.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(scrape),
		})
	}
	if imageRef != "" {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/opt/frameworks/vmagent/docker-compose.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(buildCustomCompose("vmagent", imageRef, buildVMAgentComposeOptions(config))),
		})
	}
	return out
}

// ArtifactsForVMAuth returns the files VMAAuthProvisioner writes.
func ArtifactsForVMAuth(host inventory.Host, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	var out []artifacts.DesiredArtifact
	if env := BuildServiceEnvFileBytes(config); len(env) > 0 {
		out = append(out, artifacts.DesiredArtifact{
			Path:       "/etc/frameworks/vmauth.env",
			Kind:       artifacts.KindEnv,
			Content:    env,
			IgnoreKeys: []string{"VM_HTTP_AUTH_PASSWORD", "EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64", "EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64"},
		})
	}
	if authConfig, err := buildVMAAuthConfig(
		config.EnvVars["VMAUTH_UPSTREAM_WRITE_URL"],
		config.EnvVars["EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64"],
		config.EnvVars["VM_HTTP_AUTH_USERNAME"],
		config.EnvVars["VM_HTTP_AUTH_PASSWORD"],
	); err == nil {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/etc/frameworks/vmauth.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(authConfig),
		})
	}
	if imageRef != "" {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/opt/frameworks/vmauth/docker-compose.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(buildCustomCompose("vmauth", imageRef, buildVMAuthComposeOptions(config))),
		})
	}
	return out
}

// ArtifactsForGrafana returns the files GrafanaProvisioner writes.
func ArtifactsForGrafana(host inventory.Host, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	var out []artifacts.DesiredArtifact
	if env := BuildServiceEnvFileBytes(config); len(env) > 0 {
		out = append(out, artifacts.DesiredArtifact{
			Path:       "/etc/frameworks/grafana.env",
			Kind:       artifacts.KindEnv,
			Content:    env,
			IgnoreKeys: []string{"VM_HTTP_AUTH_PASSWORD", "GF_SECURITY_ADMIN_PASSWORD"},
		})
	}
	if url := strings.TrimSpace(config.EnvVars["VICTORIAMETRICS_URL"]); url != "" {
		ds := buildGrafanaDatasource(url, config.EnvVars["VM_HTTP_AUTH_USERNAME"], config.EnvVars["VM_HTTP_AUTH_PASSWORD"])
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/opt/frameworks/grafana/provisioning/datasources/frameworks.yaml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(ds),
		})
	}
	if imageRef != "" {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/opt/frameworks/grafana/docker-compose.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(buildCustomCompose("grafana", imageRef, buildGrafanaComposeOptions(config))),
		})
	}
	return out
}

// ArtifactsForClickHouse returns the ClickHouse config files.
func ArtifactsForClickHouse(host inventory.Host, config ServiceConfig) []artifacts.DesiredArtifact {
	password := metaString(config.Metadata, "clickhouse_password")
	if password == "" {
		return nil
	}
	readonlyPassword := metaString(config.Metadata, "clickhouse_readonly_password")
	if readonlyPassword == "" {
		readonlyPassword = password
	}
	return []artifacts.DesiredArtifact{
		{
			Path:    "/etc/clickhouse-server/config.d/listen-host.xml",
			Kind:    artifacts.KindFileHash,
			Content: BuildClickHouseListenHostConfig(),
		},
		{
			Path:    "/etc/systemd/system/clickhouse-server.service",
			Kind:    artifacts.KindFileHash,
			Content: BuildClickHouseSystemdUnit(),
		},
		{
			Path:    "/etc/clickhouse-server/users.xml",
			Kind:    artifacts.KindFileHash,
			Content: BuildClickHouseUsersXML(),
		},
		{
			Path:    "/etc/systemd/system/clickhouse-server.service.d/passwords.conf",
			Kind:    artifacts.KindFileHash,
			Content: BuildClickHousePasswordsDropIn(password, readonlyPassword),
		},
	}
}

// ArtifactsForYugabyte returns the native-mode files for a Yugabyte node.
// Host-wide tuning (limits.d, sysctl.d) is intentionally omitted —
// operators commonly customize these and config-drift should not fight
// intentional host tuning.
func ArtifactsForYugabyte(host inventory.Host, config ServiceConfig) []artifacts.DesiredArtifact {
	masterAddresses := metaString(config.Metadata, "master_addresses")
	nodeID := metaIntOr(config.Metadata, "node_id", 0)
	rf := metaIntOr(config.Metadata, "replication_factor", 0)
	if rf == 0 {
		rf = 3
	}
	port := config.Port
	if port == 0 {
		port = 5433
	}
	params := YugabyteNativeParams{
		MasterAddresses: masterAddresses,
		NodeIP:          host.ExternalIP,
		DataDir:         "/var/lib/yugabyte/data",
		RF:              rf,
		YSQLPort:        port,
		Cloud:           "frameworks",
		Region:          "eu",
		Zone:            fmt.Sprintf("eu-%d", nodeID),
	}
	return []artifacts.DesiredArtifact{
		{Path: "/opt/yugabyte/conf/master.conf", Kind: artifacts.KindFileHash, Content: BuildYugabyteMasterConf(params)},
		{Path: "/opt/yugabyte/conf/tserver.conf", Kind: artifacts.KindFileHash, Content: BuildYugabyteTServerConf(params)},
		{Path: "/etc/systemd/system/yb-master.service", Kind: artifacts.KindFileHash, Content: BuildYugabyteMasterUnit()},
		{Path: "/etc/systemd/system/yb-tserver.service", Kind: artifacts.KindFileHash, Content: BuildYugabyteTServerUnit()},
	}
}

// ArtifactsForRedisNative returns the native-mode files for a Redis instance.
func ArtifactsForRedisNative(host inventory.Host, instanceName, family string, config ServiceConfig) []artifacts.DesiredArtifact {
	port := config.Port
	if port == 0 {
		port = 6379
	}
	password := metaString(config.Metadata, "password")
	engine, err := resolveRedisEngine(config.Metadata)
	if err != nil {
		return nil
	}
	paths := BuildRedisNativePaths(engine, instanceName, family)
	return []artifacts.DesiredArtifact{
		{
			Path:    paths.ConfigPath,
			Kind:    artifacts.KindFileHash,
			Content: BuildRedisNativeConfig(engine, instanceName, port, password, family, config.Metadata),
		},
		{
			Path:    paths.SystemdUnitPath,
			Kind:    artifacts.KindFileHash,
			Content: BuildRedisNativeSystemdUnit(engine, instanceName, family),
		},
	}
}

// ArtifactsForZookeeperNative returns the native-mode files written by
// ZookeeperProvisioner.
func ArtifactsForZookeeperNative(host inventory.Host, config ServiceConfig) []artifacts.DesiredArtifact {
	port := config.Port
	if port == 0 {
		port = 2181
	}
	serverLines := strings.Join(zookeeperServerList(config.Metadata["servers"]), "\n")
	return []artifacts.DesiredArtifact{
		{
			Path:    "/etc/zookeeper/zoo.cfg",
			Kind:    artifacts.KindFileHash,
			Content: BuildZookeeperConfig(port, serverLines),
		},
		{
			Path:    "/etc/systemd/system/frameworks-zookeeper.service",
			Kind:    artifacts.KindFileHash,
			Content: BuildZookeeperSystemdUnit(),
		},
	}
}

// ArtifactsForZookeeperDocker returns the files ZookeeperProvisioner writes
// in docker mode.
func ArtifactsForZookeeperDocker(host inventory.Host, config ServiceConfig, imageRef string) []artifacts.DesiredArtifact {
	port := config.Port
	if port == 0 {
		port = 2181
	}
	envVars := map[string]string{
		"ZOO_SERVER_ID": fmt.Sprintf("%v", config.Metadata["server_id"]),
	}
	envContent := GenerateEnvFile("zookeeper", envVars)
	out := []artifacts.DesiredArtifact{{
		Path:    "/etc/frameworks/zookeeper.env",
		Kind:    artifacts.KindEnv,
		Content: []byte(envContent),
	}}

	compose, err := GenerateDockerCompose(DockerComposeData{
		ServiceName: "zookeeper",
		Image:       imageRef,
		Port:        port,
		EnvFile:     "/etc/frameworks/zookeeper.env",
		Networks:    []string{"frameworks"},
		Volumes: []string{
			"/var/lib/zookeeper:/var/lib/zookeeper",
		},
	})
	if err == nil {
		out = append(out, artifacts.DesiredArtifact{
			Path:    "/opt/frameworks/zookeeper/docker-compose.yml",
			Kind:    artifacts.KindFileHash,
			Content: []byte(compose),
		})
	}
	return out
}
