package internalpki

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMaterialFromBase64(t *testing.T) {
	t.Parallel()
	encode := func(value string) string {
		return base64.StdEncoding.EncodeToString([]byte(value))
	}
	material, err := LoadMaterial(map[string]string{
		rootCertB64Key:         encode("root"),
		intermediateCertB64Key: encode("intermediate"),
		intermediateKeyB64Key:  encode("key"),
	}, "")
	if err != nil {
		t.Fatalf("LoadMaterial: %v", err)
	}
	if material.CABundlePEM() != "root\nintermediate\n" {
		t.Fatalf("CABundlePEM = %q", material.CABundlePEM())
	}
	if material.IntermediateKeyPEM != "key" {
		t.Fatalf("IntermediateKeyPEM = %q", material.IntermediateKeyPEM)
	}
}

func TestLoadMaterialFromRelativeFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"root.crt":         "root",
		"intermediate.crt": "intermediate",
		"intermediate.key": "key",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	material, err := LoadMaterial(map[string]string{
		rootCertFileKey:         "root.crt",
		intermediateCertFileKey: "intermediate.crt",
		intermediateKeyFileKey:  "intermediate.key",
	}, dir)
	if err != nil {
		t.Fatalf("LoadMaterial: %v", err)
	}
	if material.RootCertPEM != "root" || material.IntermediateCertPEM != "intermediate" || material.IntermediateKeyPEM != "key" {
		t.Fatalf("material = %+v", material)
	}
}

func TestLoadMaterialRejectsPartialConfiguration(t *testing.T) {
	t.Parallel()
	_, err := LoadMaterial(map[string]string{rootCertB64Key: "cm9vdA=="}, "")
	if err == nil || !strings.Contains(err.Error(), "requires root cert, intermediate cert, and intermediate key") {
		t.Fatalf("error = %v", err)
	}
}
