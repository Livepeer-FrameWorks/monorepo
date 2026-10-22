package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// portRegistryConsumers derive every port they use from pkg/servicedefs:
// provisioning and its scrape targets, the CLI's service port map,
// port-collision accounting, diagnose and the doctor's listener checks.
var portRegistryConsumers = []string{
	"cluster_provision.go",
	"cluster_diagnose.go",
	"cluster_diagnose_hosts.go",
	"cluster_doctor_listeners.go",
	"cluster_doctor_ephemeral.go",
	"edge_doctor_listeners.go",
	"../pkg/provisioner/registry.go",
	"../pkg/inventory/ports.go",
}

// portInText matches a port number after a colon or inside an ss port filter,
// as shell snippets and URLs write it.
var portInText = regexp.MustCompile(`[:|(]([0-9]{4,5})\b`)

func TestPortRegistryConsumersRepeatNoRegisteredPort(t *testing.T) {
	registered := servicedefs.RegisteredPorts()
	var violations []string
	for _, path := range portRegistryConsumers {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok {
				return true
			}
			switch lit.Kind {
			case token.INT:
				if port, err := strconv.Atoi(lit.Value); err == nil && registered[port] {
					violations = append(violations, fmt.Sprintf("%s: %s", fset.Position(lit.Pos()), lit.Value))
				}
			case token.STRING:
				for _, match := range portInText.FindAllStringSubmatch(lit.Value, -1) {
					if port, err := strconv.Atoi(match[1]); err == nil && registered[port] {
						violations = append(violations, fmt.Sprintf("%s: %q", fset.Position(lit.Pos()), match[0]))
					}
				}
			}
			return true
		})
	}
	if len(violations) > 0 {
		t.Fatalf("%d registered port(s) repeated instead of read from servicedefs:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// Every listener servicedefs registers for a service is planned on the host
// that runs it, so diagnose and the source-port doctor check see it.
func TestDiagnosePlannedPortsCoverRegisteredListeners(t *testing.T) {
	manifest := &inventory.Manifest{Hosts: map[string]inventory.Host{}, Services: map[string]inventory.ServiceConfig{}}
	for _, id := range []string{"decklog", "network-observer", "foghorn", "commodore", "quartermaster", "navigator"} {
		manifest.Hosts[id+"-host"] = inventory.Host{Name: id + "-host"}
		manifest.Services[id] = inventory.ServiceConfig{Enabled: true, Host: id + "-host"}
	}
	planned := diagnosePlannedPorts(manifest)
	for id := range manifest.Services {
		def, ok := servicedefs.Lookup(id)
		if !ok {
			t.Fatalf("%s is not a registered service", id)
		}
		want := []int{def.DefaultPort}
		if grpcPort, ok := servicedefs.DefaultGRPCPort(id); ok {
			want = append(want, grpcPort)
		}
		for _, aux := range servicedefs.AuxiliaryPorts(id) {
			want = append(want, aux.Port)
		}
		for _, port := range want {
			if planned[id+"-host"][port] == "" {
				t.Errorf("%s: registered listener %d is not planned on its host", id, port)
			}
		}
	}
}
