package provisioner

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func NewReverseProxyProvisioner(serviceName string, defaultPort int, pool *ssh.Pool) (Provisioner, error) {
	root, err := FindAnsibleRoot()
	if err != nil {
		return nil, fmt.Errorf("%s: locate ansible root: %w", serviceName, err)
	}
	exec, err := ansiblerun.NewExecutor()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", serviceName, err)
	}
	return &RolePlaybookProvisioner{
		BaseProvisioner:  NewBaseProvisioner(serviceName, pool),
		RoleName:         "frameworks.infra.reverse_proxy:" + serviceName,
		PlaybookSelector: reverseProxyPlaybookSelector(serviceName),
		Builder:          reverseProxyVarsBuilder(serviceName, defaultPort),
		Detector:         nil,
		AnsibleRoot:      root,
		Executor:         exec,
		Ensurer: &ansiblerun.CollectionEnsurer{
			RequirementsFile: root + "/requirements.yml",
		},
	}, nil
}

func reverseProxyPlaybookSelector(serviceName string) func(ServiceConfig) string {
	return func(config ServiceConfig) string {
		switch config.Mode {
		case "docker":
			return "playbooks/compose_stack.yml"
		case "native":
			return "playbooks/" + serviceName + ".yml"
		default:
			return ""
		}
	}
}

func reverseProxyVarsBuilder(serviceName string, defaultPort int) RoleVarsBuilder {
	return func(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
		switch config.Mode {
		case "docker":
			return reverseProxyComposeVars(serviceName, defaultPort, config)
		case "native":
			switch serviceName {
			case "caddy":
				return caddyRoleVars(ctx, host, config, helpers)
			case "nginx":
				return nginxRoleVars(ctx, host, config, helpers)
			default:
				return nil, fmt.Errorf("%s: unsupported reverse proxy", serviceName)
			}
		default:
			return nil, fmt.Errorf("%s: unsupported mode %q (want docker|native)", serviceName, config.Mode)
		}
	}
}

func reverseProxyComposeVars(serviceName string, defaultPort int, config ServiceConfig) (map[string]any, error) {
	port := config.Port
	if port == 0 {
		port = defaultPort
	}
	image := config.Image
	if image == "" {
		switch serviceName {
		case "caddy":
			image = "caddy:2"
		case "nginx":
			var err error
			image, err = imageFromReleaseManifest("nginx", config.Version, config.Metadata)
			if err != nil {
				return nil, fmt.Errorf("resolve nginx image: %w", err)
			}
		default:
			return nil, fmt.Errorf("%s: unsupported reverse proxy", serviceName)
		}
	}

	sites := normalizeProxySites(config.Metadata, "docker")
	containerPort := 80
	httpsPort := metaInt(config.Metadata, "https_port")
	if httpsPort == 0 && proxySitesNeedHTTPS(serviceName, sites) {
		httpsPort = 443
	}
	configMounts, configFiles := reverseProxyContainerConfigs(serviceName, containerPort, sites)
	compose := reverseProxyComposeContent(
		serviceName,
		image,
		port,
		containerPort,
		httpsPort,
		configMounts,
		proxySiteVolumeMounts(sites),
	)
	return map[string]any{
		"compose_stack_name":            serviceName,
		"compose_stack_project_dir":     "/opt/frameworks/" + serviceName,
		"compose_stack_compose_content": compose,
		"compose_stack_files":           configFiles,
	}, nil
}

func reverseProxyContainerConfigs(serviceName string, port int, sites []proxySite) (map[string]string, map[string]any) {
	switch serviceName {
	case "caddy":
		return map[string]string{"Caddyfile": "/etc/caddy/Caddyfile"}, map[string]any{"Caddyfile": renderCaddyfile(sites)}
	default:
		mounts := map[string]string{
			"nginx.conf":      "/etc/nginx/nginx.conf",
			"frameworks.conf": "/etc/nginx/conf.d/frameworks.conf",
		}
		files := map[string]any{
			"nginx.conf":      renderNginxRootConfig("/etc/nginx/conf.d/frameworks.conf"),
			"frameworks.conf": renderNginxConfig(port, sites),
		}
		return mounts, files
	}
}

func reverseProxyComposeContent(
	serviceName, image string,
	hostPort, containerPort, httpsPort int,
	configMounts map[string]string,
	certMounts []string,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, `services:
  %s:
    image: %s
    container_name: frameworks-%s
    restart: always
    ulimits:
      nofile:
        soft: 200000
        hard: 200000
    extra_hosts:
      - "host.docker.internal:host-gateway"
    ports:
      - "%d:%d"
`, serviceName, image, serviceName, hostPort, containerPort)
	if httpsPort > 0 {
		fmt.Fprintf(&b, "      - \"%d:443\"\n", httpsPort)
	}
	fmt.Fprintf(&b, `    volumes:
`)
	configNames := make([]string, 0, len(configMounts))
	for name := range configMounts {
		configNames = append(configNames, name)
	}
	sort.Strings(configNames)
	for _, name := range configNames {
		fmt.Fprintf(&b, "      - ./%s:%s:ro\n", name, configMounts[name])
	}
	for _, mount := range certMounts {
		fmt.Fprintf(&b, "      - %s:%s:ro\n", mount, mount)
	}
	if serviceName == "caddy" {
		b.WriteString("      - ./data:/data\n      - ./config:/config\n")
	}
	b.WriteString(`    networks:
      - frameworks
networks:
  frameworks:
    driver: bridge
`)
	return b.String()
}

func renderNginxRootConfig(includePath string) string {
	return fmt.Sprintf(`user nginx;
worker_processes auto;
worker_rlimit_nofile 200000;

events {
    worker_connections 16384;
    multi_accept on;
}

http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;

    server_tokens off;
    server_names_hash_bucket_size 128;
    types_hash_max_size 4096;
    types_hash_bucket_size 128;

    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;

    keepalive_timeout 65s;
    keepalive_requests 10000;
    send_timeout 60s;

    include %s;
}
`, includePath)
}

func renderCaddyfile(sites []proxySite) string {
	var b strings.Builder
	b.WriteString("{\n    admin localhost:2019\n}\n\n")
	if len(sites) == 0 {
		b.WriteString(":80 {\n    respond \"FrameWorks reverse proxy\" 200\n}\n")
		return b.String()
	}
	for _, site := range sites {
		if len(site.Domains) == 0 || site.Upstream == "" {
			continue
		}
		b.WriteString(strings.Join(site.Domains, ", "))
		b.WriteString(" {\n")
		if directive := caddyTLSDirective(site); directive != "" {
			b.WriteString("    ")
			b.WriteString(directive)
			b.WriteString("\n")
		}
		writeCaddyProxyDirectives(&b, site)
		for _, directive := range site.ExtraDirectives {
			b.WriteString("    ")
			b.WriteString(directive)
			b.WriteString("\n")
		}
		b.WriteString("}\n\n")
	}
	return b.String()
}

func renderNginxConfig(port int, sites []proxySite) string {
	var b strings.Builder
	if len(sites) == 0 {
		fmt.Fprintf(&b, "server {\n    listen %d default_server;\n    server_name _;\n    return 404;\n}\n", port)
		return b.String()
	}
	for _, site := range sites {
		if len(site.Domains) == 0 || site.Upstream == "" {
			continue
		}
		writeNginxServer(&b, port, "", site)
		if site.TLSMode == "files" && site.TLSCertPath != "" && site.TLSKeyPath != "" {
			writeNginxServer(&b, 443, " ssl", site)
		}
	}
	return b.String()
}

func writeCaddyProxyDirectives(b *strings.Builder, site proxySite) {
	paths := site.PathPrefixes
	if len(paths) == 0 {
		b.WriteString("    reverse_proxy ")
		b.WriteString(site.Upstream)
		b.WriteString("\n")
		return
	}
	for _, path := range paths {
		b.WriteString("    reverse_proxy ")
		b.WriteString(path)
		b.WriteString(" ")
		b.WriteString(site.Upstream)
		b.WriteString("\n")
	}
}

func writeNginxServer(b *strings.Builder, port int, listenSuffix string, site proxySite) {
	fmt.Fprintf(b, "server {\n    listen %d%s;\n    server_name %s;\n", port, listenSuffix, strings.Join(site.Domains, " "))
	if listenSuffix != "" {
		fmt.Fprintf(b, "    http2 on;\n    ssl_certificate %s;\n    ssl_certificate_key %s;\n", site.TLSCertPath, site.TLSKeyPath)
	}
	paths := site.PathPrefixes
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	for _, path := range paths {
		fmt.Fprintf(b, "\n    location %s {\n", path)
		writeNginxProxyBlock(b, site)
		b.WriteString("    }\n")
	}
	b.WriteString("}\n\n")
}

func writeNginxProxyBlock(b *strings.Builder, site proxySite) {
	profile := nginxProxyProfile(site.Profile)
	fmt.Fprintf(b, `        proxy_pass %s;
        proxy_http_version 1.1;
        client_max_body_size %s;
        client_body_timeout %s;
        send_timeout %s;
        proxy_request_buffering %s;
        proxy_buffering %s;
        proxy_connect_timeout %s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
`,
		nginxProxyPassTarget(site.Upstream),
		firstNonEmpty(site.ClientMaxBodySize, profile.ClientMaxBodySize),
		firstNonEmpty(site.ClientBodyTimeout, profile.ClientBodyTimeout),
		firstNonEmpty(site.SendTimeout, profile.SendTimeout),
		onOff(site.ProxyRequestBuffering.Or(profile.ProxyRequestBuffering)),
		onOff(site.ProxyBuffering.Or(profile.ProxyBuffering)),
		firstNonEmpty(site.ProxyConnectTimeout, profile.ProxyConnectTimeout),
	)
	if site.Websocket.Or(profile.Websocket) {
		b.WriteString(`        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
`)
	}
	fmt.Fprintf(b, `        proxy_read_timeout %s;
        proxy_send_timeout %s;
`,
		firstNonEmpty(site.ProxyReadTimeout, profile.ProxyReadTimeout),
		firstNonEmpty(site.ProxySendTimeout, profile.ProxySendTimeout),
	)
}

type optionalBool struct {
	set   bool
	value bool
}

func boolValue(v any) optionalBool {
	switch typed := v.(type) {
	case bool:
		return optionalBool{set: true, value: typed}
	case string:
		if typed == "" {
			return optionalBool{}
		}
		return optionalBool{set: true, value: truthyString(typed)}
	default:
		return optionalBool{}
	}
}

func (b optionalBool) Or(fallback bool) bool {
	if b.set {
		return b.value
	}
	return fallback
}

type nginxProxyProfileConfig struct {
	ClientMaxBodySize     string
	ClientBodyTimeout     string
	SendTimeout           string
	ProxyRequestBuffering bool
	ProxyBuffering        bool
	ProxyConnectTimeout   string
	ProxyReadTimeout      string
	ProxySendTimeout      string
	Websocket             bool
}

func nginxProxyProfile(profile string) nginxProxyProfileConfig {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "web_ui":
		return nginxProxyProfileConfig{
			ClientMaxBodySize:     "16m",
			ClientBodyTimeout:     "60s",
			SendTimeout:           "60s",
			ProxyRequestBuffering: true,
			ProxyBuffering:        true,
			ProxyConnectTimeout:   "5s",
			ProxyReadTimeout:      "300s",
			ProxySendTimeout:      "300s",
			Websocket:             true,
		}
	case "media_ingest":
		return nginxProxyProfileConfig{
			ClientMaxBodySize:     "512m",
			ClientBodyTimeout:     "900s",
			SendTimeout:           "900s",
			ProxyRequestBuffering: false,
			ProxyBuffering:        false,
			ProxyConnectTimeout:   "5s",
			ProxyReadTimeout:      "900s",
			ProxySendTimeout:      "900s",
			Websocket:             false,
		}
	case "media_delivery":
		return nginxProxyProfileConfig{
			ClientMaxBodySize:     "16m",
			ClientBodyTimeout:     "300s",
			SendTimeout:           "300s",
			ProxyRequestBuffering: false,
			ProxyBuffering:        true,
			ProxyConnectTimeout:   "5s",
			ProxyReadTimeout:      "300s",
			ProxySendTimeout:      "300s",
			Websocket:             false,
		}
	default:
		return nginxProxyProfileConfig{
			ClientMaxBodySize:     "16m",
			ClientBodyTimeout:     "60s",
			SendTimeout:           "60s",
			ProxyRequestBuffering: true,
			ProxyBuffering:        true,
			ProxyConnectTimeout:   "5s",
			ProxyReadTimeout:      "300s",
			ProxySendTimeout:      "300s",
			Websocket:             true,
		}
	}
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func truthyString(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func caddyTLSDirective(site proxySite) string {
	switch site.TLSMode {
	case "", "auto":
		return ""
	case "off":
		return "tls off"
	case "internal":
		return "tls internal"
	case "files":
		if site.TLSCertPath != "" && site.TLSKeyPath != "" {
			return "tls " + site.TLSCertPath + " " + site.TLSKeyPath
		}
	}
	return ""
}

type proxySite struct {
	Name                  string
	Domains               []string
	Upstream              string
	Profile               string
	PathPrefixes          []string
	TLSBundleID           string
	TLSMode               string
	TLSCertPath           string
	TLSKeyPath            string
	ClientMaxBodySize     string
	ClientBodyTimeout     string
	SendTimeout           string
	ProxyRequestBuffering optionalBool
	ProxyBuffering        optionalBool
	ProxyConnectTimeout   string
	ProxyReadTimeout      string
	ProxySendTimeout      string
	Websocket             optionalBool
	ExtraDirectives       []string
}

func proxySiteMapsForMode(metadata map[string]any, mode string) []map[string]any {
	sites := normalizeProxySites(metadata, mode)
	out := make([]map[string]any, 0, len(sites))
	for _, site := range sites {
		item := map[string]any{
			"domains":    site.Domains,
			"upstream":   site.Upstream,
			"proxy_pass": nginxProxyPassTarget(site.Upstream),
		}
		if site.Profile != "" {
			item["profile"] = site.Profile
		}
		if site.Name != "" {
			item["name"] = site.Name
		}
		if len(site.PathPrefixes) > 0 {
			item["path_prefixes"] = site.PathPrefixes
		}
		if site.TLSBundleID != "" {
			item["tls_bundle_id"] = site.TLSBundleID
		}
		if site.TLSMode != "" {
			item["tls_mode"] = site.TLSMode
		}
		if site.TLSCertPath != "" {
			item["tls_cert_path"] = site.TLSCertPath
		}
		if site.TLSKeyPath != "" {
			item["tls_key_path"] = site.TLSKeyPath
		}
		if site.ClientMaxBodySize != "" {
			item["client_max_body_size"] = site.ClientMaxBodySize
		}
		if site.ClientBodyTimeout != "" {
			item["client_body_timeout"] = site.ClientBodyTimeout
		}
		if site.SendTimeout != "" {
			item["send_timeout"] = site.SendTimeout
		}
		if site.ProxyRequestBuffering.set {
			item["proxy_request_buffering"] = site.ProxyRequestBuffering.value
		}
		if site.ProxyBuffering.set {
			item["proxy_buffering"] = site.ProxyBuffering.value
		}
		if site.ProxyConnectTimeout != "" {
			item["proxy_connect_timeout"] = site.ProxyConnectTimeout
		}
		if site.ProxyReadTimeout != "" {
			item["proxy_read_timeout"] = site.ProxyReadTimeout
		}
		if site.ProxySendTimeout != "" {
			item["proxy_send_timeout"] = site.ProxySendTimeout
		}
		if site.Websocket.set {
			item["websocket"] = site.Websocket.value
		}
		if len(site.ExtraDirectives) > 0 {
			item["extra_directives"] = site.ExtraDirectives
		}
		out = append(out, item)
	}
	return out
}

func normalizeProxySites(metadata map[string]any, mode string) []proxySite {
	rawSites := rawProxySitesFromMetadata(metadata)
	sites := make([]proxySite, 0, len(rawSites))
	for _, raw := range rawSites {
		site := proxySite{
			Name:                  stringValue(raw["name"]),
			Domains:               stringSliceValue(raw["domains"]),
			Upstream:              normalizeProxyUpstream(stringValue(raw["upstream"]), mode),
			Profile:               stringValue(raw["profile"]),
			PathPrefixes:          normalizePathPrefixes(raw),
			TLSBundleID:           stringValue(raw["tls_bundle_id"]),
			TLSMode:               strings.ToLower(stringValue(raw["tls_mode"])),
			TLSCertPath:           stringValue(raw["tls_cert_path"]),
			TLSKeyPath:            stringValue(raw["tls_key_path"]),
			ClientMaxBodySize:     stringValue(raw["client_max_body_size"]),
			ClientBodyTimeout:     stringValue(raw["client_body_timeout"]),
			SendTimeout:           stringValue(raw["send_timeout"]),
			ProxyRequestBuffering: boolValue(raw["proxy_request_buffering"]),
			ProxyBuffering:        boolValue(raw["proxy_buffering"]),
			ProxyConnectTimeout:   stringValue(raw["proxy_connect_timeout"]),
			ProxyReadTimeout:      stringValue(raw["proxy_read_timeout"]),
			ProxySendTimeout:      stringValue(raw["proxy_send_timeout"]),
			Websocket:             boolValue(raw["websocket"]),
			ExtraDirectives:       stringSliceValue(raw["extra_directives"]),
		}
		if site.TLSMode == "" && site.TLSCertPath != "" && site.TLSKeyPath != "" {
			site.TLSMode = "files"
		}
		if len(site.Domains) == 0 || site.Upstream == "" {
			continue
		}
		sites = append(sites, site)
	}
	return sites
}

func rawProxySitesFromMetadata(metadata map[string]any) []map[string]any {
	if sites, ok := metadata["proxy_sites"].([]map[string]any); ok {
		return sites
	}
	if sites, ok := metadata["sites"].([]map[string]any); ok {
		return sites
	}
	return nil
}

func normalizeProxyUpstream(upstream, mode string) string {
	upstream = strings.TrimSpace(upstream)
	scheme := ""
	for _, candidate := range []string{"http://", "https://"} {
		if strings.HasPrefix(upstream, candidate) {
			scheme = candidate
			upstream = strings.TrimPrefix(upstream, candidate)
			break
		}
	}
	if mode != "docker" {
		return scheme + upstream
	}
	for _, prefix := range []string{"127.0.0.1:", "localhost:"} {
		if strings.HasPrefix(upstream, prefix) {
			return scheme + "host.docker.internal:" + strings.TrimPrefix(upstream, prefix)
		}
	}
	return scheme + upstream
}

func nginxProxyPassTarget(upstream string) string {
	if strings.HasPrefix(upstream, "http://") || strings.HasPrefix(upstream, "https://") {
		return upstream
	}
	return "http://" + upstream
}

func normalizePathPrefixes(site map[string]any) []string {
	paths := stringSliceValue(site["path_prefixes"])
	if len(paths) == 0 {
		if path := stringValue(site["path_prefix"]); path != "" {
			paths = []string{path}
		}
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		out = append(out, path)
	}
	return out
}

func proxySitesNeedHTTPS(serviceName string, sites []proxySite) bool {
	for _, site := range sites {
		if serviceName == "caddy" && site.TLSMode != "off" {
			return true
		}
		if site.TLSMode == "files" {
			return true
		}
	}
	return false
}

func proxySiteVolumeMounts(sites []proxySite) []string {
	seen := map[string]struct{}{}
	var mounts []string
	for _, site := range sites {
		for _, path := range []string{site.TLSCertPath, site.TLSKeyPath} {
			if path == "" || !filepath.IsAbs(path) {
				continue
			}
			dir := filepath.Dir(path)
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			mounts = append(mounts, dir)
		}
	}
	return mounts
}

func stringValue(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func stringSliceValue(v any) []string {
	switch typed := v.(type) {
	case []string:
		out := append([]string{}, typed...)
		return sortedNonEmptyStrings(out)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return sortedNonEmptyStrings(out)
	default:
		return nil
	}
}

func sortedNonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func metaInt(metadata map[string]any, key string) int {
	switch v := metadata[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
