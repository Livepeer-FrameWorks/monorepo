package provisioner

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const kafkaRoleDir = "ansible/collections/ansible_collections/frameworks/infra/roles/kafka/"

// renderKafkaServerProperties evaluates the kafka_role branches of
// server.properties.j2 and substitutes the quorum timeout vars from the role's
// vars/main.yml. Other expressions stay verbatim; the test only reads the
// controller.quorum.* lines.
func renderKafkaServerProperties(t *testing.T, role string) map[string]string {
	t.Helper()
	tmpl := readRepoFile(t, kafkaRoleDir+"templates/server.properties.j2")
	var vars map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, kafkaRoleDir+"vars/main.yml")), &vars); err != nil {
		t.Fatalf("parse kafka vars: %v", err)
	}

	cond := regexp.MustCompile(`^\{%\s*(if|elif)\s+kafka_role\s*(==|!=)\s*'([a-z]+)'\s*%\}$`)
	expr := regexp.MustCompile(`\{\{\s*(kafka_quorum_[a-z_]+)\s*\}\}`)
	type frame struct{ taken, active bool }
	var stack []frame
	emitting := func() bool {
		for _, f := range stack {
			if !f.active {
				return false
			}
		}
		return true
	}
	props := map[string]string{}
	for _, line := range strings.Split(tmpl, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := cond.FindStringSubmatch(trimmed); m != nil {
			match := (role == m[3]) == (m[2] == "==")
			if m[1] == "if" {
				stack = append(stack, frame{taken: match, active: match})
			} else {
				top := &stack[len(stack)-1]
				top.active = !top.taken && match
				top.taken = top.taken || match
			}
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "{% else"):
			top := &stack[len(stack)-1]
			top.active = !top.taken
			top.taken = true
			continue
		case strings.HasPrefix(trimmed, "{% endif"):
			stack = stack[:len(stack)-1]
			continue
		case strings.HasPrefix(trimmed, "{%"):
			t.Fatalf("unhandled template statement %q; extend the test renderer", trimmed)
		}
		if !emitting() || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		line = expr.ReplaceAllStringFunc(line, func(s string) string {
			name := expr.FindStringSubmatch(s)[1]
			v, ok := vars[name]
			if !ok {
				t.Fatalf("%s is not defined in kafka vars/main.yml", name)
			}
			return strconv.Itoa(v.(int))
		})
		key, value, _ := strings.Cut(line, "=")
		props[key] = value
	}
	if len(stack) != 0 {
		t.Fatalf("unbalanced if/endif in server.properties.j2")
	}
	return props
}

// Staging lost its KRaft leader whenever the shared hypervisor storage stalled
// fsync for 2-12 s: every voter's raft thread froze, hit the stock 2 s fetch
// timeout on resume and abandoned a healthy leader, and the elections that
// followed timed out at the stock 1 s while their own quorum-state fsyncs were
// stalled. Voters must ride out such a stall; brokers keep the stock observer
// timeouts so they rediscover a new leader within broker.session.timeout.ms.
func TestKafkaVotersToleratePausedStorageWithoutLosingTheLeader(t *testing.T) {
	for _, role := range []string{"controller", "combined"} {
		props := renderKafkaServerProperties(t, role)
		fetch, err := strconv.Atoi(props["controller.quorum.fetch.timeout.ms"])
		if err != nil {
			t.Fatalf("%s: controller.quorum.fetch.timeout.ms not rendered: %v (props=%v)", role, err, props)
		}
		election, err := strconv.Atoi(props["controller.quorum.election.timeout.ms"])
		if err != nil {
			t.Fatalf("%s: controller.quorum.election.timeout.ms not rendered: %v", role, err)
		}
		// Staging stalls ran 2-4 s typically and up to ~12 s with compounding
		// elections; 10 s covers ~96% of observed bursts, and the leader's
		// check-quorum window is 1.5x that.
		if fetch < 10000 {
			t.Fatalf("%s: fetch timeout %d ms abandons the leader on a multi-second fsync stall", role, fetch)
		}
		if election < 5000 || election >= fetch {
			t.Fatalf("%s: election timeout %d ms must outlast a stalled vote fsync and stay below the fetch timeout %d ms", role, election, fetch)
		}
		if props["process.roles"] == "" {
			t.Fatalf("%s: renderer lost process.roles: %v", role, props)
		}
	}
	broker := renderKafkaServerProperties(t, "broker")
	if broker["process.roles"] != "broker" {
		t.Fatalf("broker render: process.roles=%q", broker["process.roles"])
	}
	for _, key := range []string{"controller.quorum.fetch.timeout.ms", "controller.quorum.election.timeout.ms"} {
		if v, ok := broker[key]; ok {
			t.Fatalf("broker must keep the stock observer %s, got %s", key, v)
		}
	}
}
