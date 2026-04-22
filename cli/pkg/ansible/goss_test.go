package ansible

import (
	"strings"
	"testing"
)

func TestRenderGossYAML_servicePortFile(t *testing.T) {
	t.Parallel()
	spec := GossSpec{
		Services: map[string]GossService{
			"frameworks-kafka": {Running: true, Enabled: true},
		},
		Ports: map[string]GossPort{
			"tcp:9092": {Listening: true},
		},
		Files: map[string]GossFile{
			"/opt/kafka/bin/kafka-server-start.sh": {Exists: true, Mode: "0755"},
		},
	}
	out := RenderGossYAML(spec)
	for _, frag := range []string{
		"service:",
		"  frameworks-kafka:",
		"    running: true",
		"    enabled: true",
		"port:",
		"  tcp:9092:",
		"    listening: true",
		"file:",
		"  /opt/kafka/bin/kafka-server-start.sh:",
		"    exists: true",
		`    mode: "0755"`,
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("rendered goss YAML missing %q; got:\n%s", frag, out)
		}
	}
}

func TestRenderGossYAML_sortedDeterministic(t *testing.T) {
	t.Parallel()
	spec := GossSpec{
		Services: map[string]GossService{
			"zzz": {Running: true},
			"aaa": {Running: true},
		},
	}
	a := RenderGossYAML(spec)
	b := RenderGossYAML(spec)
	if a != b {
		t.Errorf("RenderGossYAML must be stable for identical input:\n--a--\n%s\n--b--\n%s", a, b)
	}
	if strings.Index(a, "aaa:") > strings.Index(a, "zzz:") {
		t.Errorf("service keys must be sorted; got:\n%s", a)
	}
}

func TestRenderGossYAML_emptyOmitsSections(t *testing.T) {
	t.Parallel()
	out := RenderGossYAML(GossSpec{})
	if strings.Contains(out, "service:") || strings.Contains(out, "port:") ||
		strings.Contains(out, "file:") || strings.Contains(out, "process:") {
		t.Errorf("empty spec must emit empty output; got:\n%s", out)
	}
}

func TestGossValidateTasks_shapeAndChangedWhenFalse(t *testing.T) {
	t.Parallel()
	tasks := GossValidateTasks("kafka", "service:\n  x:\n    running: true\n")
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks (mkdir, copy spec, validate), got %d", len(tasks))
	}
	if tasks[0].Module != "ansible.builtin.file" || tasks[0].Args["state"] != "directory" {
		t.Errorf("first task must be the goss spec dir mkdir; got %+v", tasks[0])
	}
	if tasks[1].Module != "ansible.builtin.copy" {
		t.Errorf("second task must copy the spec; got module=%s", tasks[1].Module)
	}
	if tasks[2].Module != "ansible.builtin.shell" {
		t.Errorf("third task must run goss; got module=%s", tasks[2].Module)
	}
	// validate is read-only — must not report changed on rerun.
	if tasks[2].ChangedWhen != "false" {
		t.Errorf("goss validate task must have ChangedWhen=false; got %q", tasks[2].ChangedWhen)
	}
	if !strings.Contains(tasks[2].Args["cmd"].(string), "validate") {
		t.Errorf("goss shell cmd must call validate; got %v", tasks[2].Args["cmd"])
	}
}

func TestGossInstallTasks_checksumThreadsThrough(t *testing.T) {
	t.Parallel()
	tasks := GossInstallTasks("https://example.com/goss-linux-amd64", "sha256:deadbeef")
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (get_url, chmod), got %d", len(tasks))
	}
	if tasks[0].Module != "ansible.builtin.get_url" {
		t.Errorf("first task must be get_url; got %s", tasks[0].Module)
	}
	if tasks[0].Args["checksum"] != "sha256:deadbeef" {
		t.Errorf("checksum must propagate into get_url; got %v", tasks[0].Args["checksum"])
	}
	if tasks[1].Args["mode"] != "0755" {
		t.Errorf("chmod task must set 0755; got %v", tasks[1].Args["mode"])
	}
}
