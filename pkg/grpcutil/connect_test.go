package grpcutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every client sets its connect timeout through WithConnectParams: a bare
// grpc.ConnectParams literal replaces gRPC's backoff with a zero one, and its
// clients redial a stopped peer thousands of times per second.
func TestNoBareConnectParamsOutsideGrpcutil(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	self, err := filepath.Abs("connect.go")
	if err != nil {
		t.Fatal(err)
	}
	offenders, err := bareConnectParamsSources(root, self)
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("grpc.ConnectParams literal without the reconnect backoff in %v; use grpcutil.WithConnectParams", offenders)
	}
}

// Hidden directories hold scratch state (.git, .stack, a TMPDIR such as .tmp
// whose build directories come and go during the walk), never sources.
func TestBareConnectParamsScanSkipsHiddenScratch(t *testing.T) {
	root := t.TempDir()
	literal := []byte("package x\n\nvar _ = grpc.ConnectParams{}\n")
	for _, dir := range []string{".tmp/go-build1/b579", "svc"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x.go"), literal, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	offenders, err := bareConnectParamsSources(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(offenders, ",") != filepath.Join("svc", "x.go") {
		t.Fatalf("offenders = %v, want only svc/x.go", offenders)
	}
}

func bareConnectParamsSources(root, self string) ([]string, error) {
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			switch name {
			case "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == self {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if strings.Contains(string(body), "grpc.ConnectParams{") {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	return offenders, err
}
