package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/internal/templates"
)

type memEdgeStack struct {
	files  map[string]string
	writes []string
}

func (m *memEdgeStack) Read(name string) (string, bool, error) {
	content, ok := m.files[name]
	return content, ok, nil
}

func (m *memEdgeStack) Write(name, content string) error {
	m.files[name] = content
	m.writes = append(m.writes, name)
	return nil
}

// `edge update` on an operator-local container stack rewrites the compose
// file and .edge.env from this CLI's templates, and leaves the write-once
// secrets and enrollment files alone.
func TestRerenderEdgeStackFilesUpdatesStaleConfig(t *testing.T) {
	files, err := templates.RenderEdgeTemplates(templates.EdgeVars{
		NodeID:          "edge-self-1",
		EdgeDomain:      "edge-self-1.example.com",
		FoghornGRPCAddr: "foghorn.example.com:18019",
		Mode:            "container",
		EdgeOS:          "linux",
		EnrollmentToken: "tok",
		MistAPIPassword: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	stack := &memEdgeStack{files: map[string]string{}}
	for _, f := range files {
		stack.files[f.Path] = string(f.Content)
	}
	fresh := stack.files[edgeOperatorComposeFile]
	stack.files[edgeOperatorComposeFile] = strings.Replace(fresh, "stop_grace_period: 1m", "stop_grace_period: 10s", 1)
	secrets, enroll := stack.files[".edge-secrets.env"], stack.files[".edge-enroll.env"]

	changed, err := rerenderEdgeStackFiles(stack)
	if err != nil {
		t.Fatalf("rerenderEdgeStackFiles: %v", err)
	}
	if strings.Join(changed, ",") != edgeOperatorComposeFile {
		t.Fatalf("changed = %v, want only the stale compose file", changed)
	}
	if stack.files[edgeOperatorComposeFile] != fresh {
		t.Fatalf("compose not re-rendered:\n%s", stack.files[edgeOperatorComposeFile])
	}
	if stack.files[".edge-secrets.env"] != secrets || stack.files[".edge-enroll.env"] != enroll {
		t.Fatal("write-once secrets or enrollment file changed")
	}

	again, err := rerenderEdgeStackFiles(stack)
	if err != nil || len(again) != 0 {
		t.Fatalf("second re-render changed %v (err %v); an up-to-date stack must be left alone", again, err)
	}
}
