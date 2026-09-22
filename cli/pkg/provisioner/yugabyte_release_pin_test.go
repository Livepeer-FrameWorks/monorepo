package provisioner

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/gitops"
	"gopkg.in/yaml.v3"
)

func TestYugabyteReleaseAndContractEnginePinsAgree(t *testing.T) {
	manifestPath := findInfrastructureYaml(t)
	if manifestPath == "" {
		t.Fatal("infrastructure release authority missing")
	}
	read := func(file string, out any) {
		t.Helper()
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	var release gitops.Manifest
	read(manifestPath, &release)
	engine := release.GetInfrastructure("yugabyte")
	if engine == nil {
		t.Fatal("Yugabyte release pin missing")
	}
	var contracts struct {
		Engines []struct {
			Name, Image, Digest string
		} `yaml:"contract_engines"`
	}
	read(filepath.Join(filepath.Dir(manifestPath), "schema-contract-engines.yaml"), &contracts)
	build := ""
	for _, entry := range contracts.Engines {
		if entry.Name == "yugabyte" {
			var ok bool
			build, ok = strings.CutPrefix(entry.Image, "yugabytedb/yugabyte:")
			if !ok || !strings.HasPrefix(entry.Digest, "sha256:") {
				t.Fatalf("invalid contract image pin: %+v", entry)
			}
		}
	}
	if engine.Version == "" || !strings.HasPrefix(build, engine.Version+"-b") {
		t.Fatalf("contract build %q does not match release version %q", build, engine.Version)
	}
	for arch, suffix := range map[string]string{"linux-amd64": "linux-x86_64.tar.gz", "linux-arm64": "el8-aarch64.tar.gz"} {
		found := false
		for _, artifact := range engine.Artifacts {
			if artifact.Arch != arch {
				continue
			}
			found = true
			u, err := url.Parse(artifact.URL)
			if err != nil || u.Scheme != "https" || path.Base(u.Path) != "yugabyte-"+build+"-"+suffix || !strings.HasPrefix(artifact.Checksum, "sha256:") {
				t.Errorf("%s release archive does not match contract build %s: %+v", arch, build, artifact)
			}
		}
		if !found {
			t.Errorf("missing release artifact for %s", arch)
		}
	}
}
