package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testMap = map[string]target{
	"Stream":                       {importPath: oldPath + "/commodore", alias: "commodorepb"},
	"MistTrigger":                  {importPath: oldPath + "/ipc", alias: "ipcpb"},
	"NavigatorService_ServiceDesc": {importPath: oldPath + "/dns", alias: "dnspb"},
}

func write(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, src string) string {
	t.Helper()
	p := write(t, src)
	res, err := rewriteFile(p, testMap)
	if err != nil {
		t.Fatalf("rewriteFile: %v", err)
	}
	if res == nil {
		t.Fatalf("expected a rewrite, got nil")
	}
	return string(res.newSrc)
}

func TestAliasedPB(t *testing.T) {
	out := run(t, `package x
import pb "`+oldPath+`"
func f() { _ = &pb.Stream{}; _ = pb.MistTrigger{} }
`)
	for _, want := range []string{
		`commodorepb "` + oldPath + `/commodore"`,
		`ipcpb "` + oldPath + `/ipc"`,
		"commodorepb.Stream",
		"ipcpb.MistTrigger",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `pb "`+oldPath+`"`) {
		t.Errorf("old import not removed:\n%s", out)
	}
}

func TestAliasedProto(t *testing.T) {
	out := run(t, `package x
import proto "`+oldPath+`"
func f() { _ = &proto.Stream{} }
`)
	if !strings.Contains(out, "commodorepb.Stream") {
		t.Errorf("expected commodorepb.Stream:\n%s", out)
	}
	if strings.Contains(out, "proto.Stream") {
		t.Errorf("proto.Stream not rewritten:\n%s", out)
	}
}

func TestUnaliasedDefaultName(t *testing.T) {
	out := run(t, `package x
import "`+oldPath+`"
func f() { _ = proto.NavigatorService_ServiceDesc }
`)
	if !strings.Contains(out, "dnspb.NavigatorService_ServiceDesc") {
		t.Errorf("expected dnspb.NavigatorService_ServiceDesc:\n%s", out)
	}
	if !strings.Contains(out, `dnspb "`+oldPath+`/dns"`) {
		t.Errorf("expected dnspb import:\n%s", out)
	}
}

func TestBareUseFailsHard(t *testing.T) {
	p := write(t, `package x
import pb "`+oldPath+`"
var sink interface{}
func f() { sink = pb }
`)
	if _, err := rewriteFile(p, testMap); err == nil {
		t.Fatal("expected error on bare use of local name, got nil")
	}
}

func TestUnknownSelectorFailsHard(t *testing.T) {
	p := write(t, `package x
import pb "`+oldPath+`"
func f() { _ = pb.DoesNotExist{} }
`)
	if _, err := rewriteFile(p, testMap); err == nil {
		t.Fatal("expected error on unknown selector, got nil")
	}
}

func TestNoProtoImportIsSkipped(t *testing.T) {
	p := write(t, `package x
import "fmt"
func f() { fmt.Println("hi") }
`)
	res, err := rewriteFile(p, testMap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result for non-consumer file")
	}
}
