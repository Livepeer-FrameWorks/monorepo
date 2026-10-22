package provisioner

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"gopkg.in/yaml.v3"
)

func servicePort(id string) int { return servicedefs.Services[id].DefaultPort }

// rolePortDefaults pins every role default or var that carries a port
// servicedefs registers, as <role>/<key path>, to the registry's value. Ansible
// cannot read the Go registry, so the YAML keeps the number and this table is
// where it is tied to its single declaration.
var rolePortDefaults = map[string]int{
	"caddy/caddy_admin_listen":                        servicedefs.CaddyAdminPort,
	"chatwoot/chatwoot_port":                          servicePort("chatwoot"),
	"clickhouse/clickhouse_port_http":                 servicedefs.ClickHouseHTTPPort,
	"clickhouse/clickhouse_port_tcp":                  servicedefs.ClickHouseNativePort,
	"clickhouse/clickhouse_port_interserver":          servicedefs.ClickHouseInterserverPort,
	"edge/edge_image_env.MISTSERVER_URL":              servicedefs.MistAPIPort,
	"edge/edge_image_env.MISTSERVER_HTTP_URL":         servicePort("mistserver"),
	"edge/edge_image_env.HELMSMAN_WEBHOOK_URL":        servicePort("helmsman"),
	"edge/edge_node_exporter_port":                    servicedefs.NodeExporterPort,
	"helmsman/helmsman_port":                          servicePort("helmsman"),
	"helmsman/helmsman_mist_api_upstream":             servicedefs.MistAPIPort,
	"helmsman/helmsman_mist_http_upstream":            servicePort("mistserver"),
	"helmsman/helmsman_webhook_upstream":              servicePort("helmsman"),
	"helmsman/helmsman_caddy_admin_url":               servicedefs.CaddyAdminPort,
	"kafka/kafka_broker_port":                         servicedefs.KafkaBrokerPort,
	"kafka/kafka_controller_port":                     servicedefs.KafkaControllerPort,
	"kafka/kafka_jmx_port":                            servicedefs.KafkaBrokerJMXPort,
	"kafka_mirrormaker/kafka_mm_rest_port":            servicedefs.KafkaMirrorMakerRESTPort,
	"kafka_mirrormaker/kafka_mm_jmx_port":             servicedefs.KafkaMirrorMakerJMXPort,
	"listmonk/listmonk_port":                          servicePort("listmonk"),
	"listmonk/listmonk_db_port":                       servicedefs.PostgresPort,
	"mistserver/mistserver_controller_port":           servicedefs.MistAPIPort,
	"nginx/nginx_http_port":                           servicePort("nginx"),
	"nginx/nginx_tenant_alias_playback.upstream_port": servicePort("foghorn"),
	"postgres/postgres_port":                          servicedefs.PostgresPort,
	"privateer/privateer_port":                        servicePort("privateer"),
	"privateer/privateer_wireguard_port":              servicedefs.WireGuardPort,
	"prometheus_stack/prometheus_port":                servicePort("prometheus"),
	"prometheus_stack/node_exporter_port":             servicedefs.NodeExporterPort,
	"prometheus_stack/victoriametrics_port":           servicePort("victoriametrics"),
	"prometheus_stack/vmagent_port":                   servicePort("vmagent"),
	"prometheus_stack/vmauth_port":                    servicePort("vmauth"),
	"prometheus_stack/vmalert_port":                   servicePort("vmalert"),
	"prometheus_stack/alertmanager_port":              servicePort("alertmanager"),
	"redis/redis_port":                                servicedefs.RedisPort,
	"redis/redis_primary_port":                        servicedefs.RedisPort,
	"redis/redis_sentinel_port":                       servicedefs.RedisSentinelPort,
	"yugabyte/yugabyte_master_rpc_port":               servicedefs.YugabyteMasterRPCPort,
	"yugabyte/yugabyte_master_web_port":               servicedefs.YugabyteMasterWebPort,
	"yugabyte/yugabyte_tserver_rpc_port":              servicedefs.YugabyteTServerRPCPort,
	"yugabyte/yugabyte_tserver_web_port":              servicedefs.YugabyteTServerWebPort,
	"yugabyte/yugabyte_ysql_port":                     servicedefs.YugabyteYSQLPort,
	"yugabyte/yugabyte_ycql_port":                     servicedefs.YugabyteYCQLPort,
}

// rolePortNumber finds the port of a host:port or URL value.
var rolePortNumber = regexp.MustCompile(`:(\d{2,5})(?:$|/)`)

// TestRolePortDefaultsMatchTheRegistry fails when a role default or var holds
// a port servicedefs registers without an entry in rolePortDefaults, or when
// an entry's value differs from the registry.
func TestRolePortDefaultsMatchTheRegistry(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	roles := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles")
	files, err := filepath.Glob(filepath.Join(roles, "*", "*", "main.yml"))
	if err != nil {
		t.Fatal(err)
	}
	registered := servicedefs.RegisteredPorts()
	seen := map[string]bool{}
	var problems []string
	for _, file := range files {
		kind := filepath.Base(filepath.Dir(file))
		if kind != "defaults" && kind != "vars" {
			continue
		}
		role := filepath.Base(filepath.Dir(filepath.Dir(file)))
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		if err := yaml.Unmarshal(data, &values); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		var visit func(path string, value any)
		visit = func(path string, value any) {
			switch v := value.(type) {
			case map[string]any:
				for key, child := range v {
					visit(path+"."+key, child)
				}
				return
			case int:
				if !strings.Contains(strings.ToLower(path), "port") || !registered[v] {
					return
				}
				check(role, strings.TrimPrefix(path, "."), v, seen, &problems)
			case string:
				for _, match := range rolePortNumber.FindAllStringSubmatch(v, -1) {
					if port, err := strconv.Atoi(match[1]); err == nil && registered[port] {
						check(role, strings.TrimPrefix(path, "."), port, seen, &problems)
					}
				}
			}
		}
		visit("", values)
	}
	for key := range rolePortDefaults {
		if !seen[key] {
			problems = append(problems, key+": in rolePortDefaults but no role holds it")
		}
	}
	sort.Strings(problems)
	for _, problem := range problems {
		t.Error(problem)
	}
}

func check(role, path string, port int, seen map[string]bool, problems *[]string) {
	key := role + "/" + path
	seen[key] = true
	want, ok := rolePortDefaults[key]
	switch {
	case !ok:
		*problems = append(*problems, fmt.Sprintf("%s = %d: a registered port; add it to rolePortDefaults with its servicedefs value", key, port))
	case want != port:
		*problems = append(*problems, fmt.Sprintf("%s = %d, servicedefs registers %d", key, port, want))
	}
}
