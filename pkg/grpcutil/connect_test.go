package grpcutil

import (
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
	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".build", ".stack":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == self {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "grpc.ConnectParams{") {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("grpc.ConnectParams literal without the reconnect backoff in %v; use grpcutil.WithConnectParams", offenders)
	}
}
