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
	configName, configPath, configContent := reverseProxyContainerConfig(serviceName, containerPort, sites)
	compose := reverseProxyComposeContent(serviceName, image, port, containerPort, httpsPort, configName, configPath, proxySiteVolumeMounts(sites))
	return map[string]any{
		"compose_stack_name":            serviceName,
		"compose_stack_project_dir":     "/opt/frameworks/" + serviceName,
		"compose_stack_compose_content": compose,
		"compose_stack_files": map[string]any{
			configName: configContent,
		},
	}, nil
}

func reverseProxyContainerConfig(serviceName string, port int, sites []proxySite) (string, string, string) {
	switch serviceName {
	case "caddy":
		return "Caddyfile", "/etc/caddy/Caddyfile", renderCaddyfile(sites)
	default:
		return "frameworks.conf", "/etc/nginx/conf.d/default.conf", renderNginxConfig(port, sites)
	}
}

func reverseProxyComposeContent(serviceName, image string, hostPort, containerPort, httpsPort int, configName, configPath string, certMounts []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `services:
  %s:
    image: %s
    container_name: frameworks-%s
    restart: always
    extra_hosts:
      - "host.docker.internal:host-gateway"
    ports:
      - "%d:%d"
`, serviceName, image, serviceName, hostPort, containerPort)
	if httpsPort > 0 {
		fmt.Fprintf(&b, "      - \"%d:443\"\n", httpsPort)
	}
	fmt.Fprintf(&b, `    volumes:
      - ./%s:%s:ro
`, configName, configPath)
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
			writeNginxServer(&b, 443, " ssl http2", site)
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
		fmt.Fprintf(b, "    ssl_certificate %s;\n    ssl_certificate_key %s;\n", site.TLSCertPath, site.TLSKeyPath)
	}
	paths := site.PathPrefixes
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	for _, path := range paths {
		fmt.Fprintf(b, "\n    location %s {\n", path)
		writeNginxProxyBlock(b, site.Upstream)
		b.WriteString("    }\n")
	}
	b.WriteString("}\n\n")
}

func writeNginxProxyBlock(b *strings.Builder, upstream string) {
	fmt.Fprintf(b, `        proxy_pass %s;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
`, nginxProxyPassTarget(upstream))
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
	Name            string
	Domains         []string
	Upstream        string
	PathPrefixes    []string
	TLSMode         string
	TLSCertPath     string
	TLSKeyPath      string
	ExtraDirectives []string
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
		if site.Name != "" {
			item["name"] = site.Name
		}
		if len(site.PathPrefixes) > 0 {
			item["path_prefixes"] = site.PathPrefixes
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
			Name:            stringValue(raw["name"]),
			Domains:         stringSliceValue(raw["domains"]),
			Upstream:        normalizeProxyUpstream(stringValue(raw["upstream"]), mode),
			PathPrefixes:    normalizePathPrefixes(raw),
			TLSMode:         strings.ToLower(stringValue(raw["tls_mode"])),
			TLSCertPath:     stringValue(raw["tls_cert_path"]),
			TLSKeyPath:      stringValue(raw["tls_key_path"]),
			ExtraDirectives: stringSliceValue(raw["extra_directives"]),
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
	s, _ := v.(string)
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
