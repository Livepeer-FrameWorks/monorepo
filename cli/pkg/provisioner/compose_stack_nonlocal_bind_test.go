package provisioner

import (
	"os"
	"strings"
	"testing"
)

// A compose service published on the host's WireGuard address must start even
// when the mesh interface is not up yet: Docker binds the published address on
// the host, and a refused bind leaves a restart:always container exited after
// boot. The role enables non-local binds before it applies the stack.
func TestComposeStackAllowsNonLocalBindBeforePublishingMeshAddresses(t *testing.T) {
	raw, err := os.ReadFile(composeStackRolePath(t, "tasks", "install.yml"))
	if err != nil {
		t.Fatal(err)
	}
	install := string(raw)
	sysctlAt := strings.Index(install, "net.ipv4.ip_nonlocal_bind = 1")
	if sysctlAt < 0 {
		t.Fatal("install.yml does not enable net.ipv4.ip_nonlocal_bind for published mesh addresses")
	}
	if !strings.Contains(install, "net.ipv6.ip_nonlocal_bind = 1") {
		t.Fatal("install.yml does not enable net.ipv6.ip_nonlocal_bind for published mesh addresses")
	}
	applyAt := strings.Index(install, "- name: Apply compose stack")
	if applyAt < 0 || sysctlAt > applyAt {
		t.Fatalf("non-local bind sysctl at %d must precede the compose apply at %d", sysctlAt, applyAt)
	}
	task := install[strings.LastIndex(install[:sysctlAt], "- name:"):]
	if end := strings.Index(task[1:], "\n- name:"); end >= 0 {
		task = task[:end+1]
	}
	if !strings.Contains(task, "compose_stack_service.ports") || !strings.Contains(task, "attribute='addrs'") {
		t.Fatalf("the non-local bind task must apply only to stacks publishing ports on given addresses:\n%s", task)
	}
}
