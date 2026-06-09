package ansiblerun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func renderHostEntry(t *testing.T, h Host) map[string]any {
	t.Helper()
	dir := t.TempDir()
	r := &InventoryRenderer{}
	invPath, err := r.Render(dir, Inventory{Hosts: []Host{h}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	raw, err := os.ReadFile(invPath)
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	all := parsed["all"].(map[string]any)
	hosts := all["hosts"].(map[string]any)
	return hosts[h.Name].(map[string]any)
}

func TestHostEntry_OptionalFieldsGatedByEmpty(t *testing.T) {
	t.Parallel()
	// All optionals set: every ansible_* key present.
	full := renderHostEntry(t, Host{
		Name: "h", Address: "10.0.0.1", User: "u", Port: 2222,
		PrivateKey: "/k.pem", Connection: "ssh",
	})
	wantKeys := map[string]any{
		"ansible_host":                 "10.0.0.1",
		"ansible_user":                 "u",
		"ansible_port":                 2222,
		"ansible_ssh_private_key_file": "/k.pem",
		"ansible_connection":           "ssh",
	}
	for k, v := range wantKeys {
		got, ok := full[k]
		if !ok {
			t.Fatalf("key %q missing from full host entry: %#v", k, full)
		}
		// YAML ints unmarshal as int; compare via fmt.
		if k == "ansible_port" {
			if got != v {
				t.Fatalf("ansible_port=%v want %v", got, v)
			}
			continue
		}
		if got != v {
			t.Fatalf("%s=%v want %v", k, got, v)
		}
	}

	// All optionals empty: only ansible_host present.
	bare := renderHostEntry(t, Host{Name: "h", Address: "10.0.0.1"})
	for _, k := range []string{"ansible_user", "ansible_port", "ansible_ssh_private_key_file", "ansible_connection"} {
		if _, ok := bare[k]; ok {
			t.Fatalf("empty field must omit %q; got %#v", k, bare)
		}
	}
	if bare["ansible_host"] != "10.0.0.1" {
		t.Fatalf("ansible_host must always be present; got %#v", bare)
	}
}

func TestHostEntry_PortZeroOmitted(t *testing.T) {
	t.Parallel()
	// Port==0 must be omitted; Port==22 must be present (only != 0 is gated).
	zero := renderHostEntry(t, Host{Name: "h", Address: "1.1.1.1", Port: 0})
	if _, ok := zero["ansible_port"]; ok {
		t.Fatalf("port 0 must be omitted; got %#v", zero)
	}
	nonzero := renderHostEntry(t, Host{Name: "h", Address: "1.1.1.1", Port: 22})
	if nonzero["ansible_port"] != 22 {
		t.Fatalf("port 22 must be present; got %#v", nonzero)
	}
}

func TestBuildInventoryTree_NoGroupsOmitsChildren(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := &InventoryRenderer{}
	invPath, err := r.Render(dir, Inventory{Hosts: []Host{{Name: "h", Address: "1.1.1.1"}}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	raw, _ := os.ReadFile(invPath)
	var parsed map[string]any
	if err = yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	all := parsed["all"].(map[string]any)
	if _, ok := all["children"]; ok {
		t.Fatalf("no groups must omit children key; got %#v", all)
	}

	// With a group, children present.
	invPath2, err := r.Render(t.TempDir(), Inventory{
		Hosts:  []Host{{Name: "h", Address: "1.1.1.1"}},
		Groups: []Group{{Name: "g", Hosts: []string{"h"}}},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	raw2, _ := os.ReadFile(invPath2)
	var parsed2 map[string]any
	_ = yaml.Unmarshal(raw2, &parsed2)
	all2 := parsed2["all"].(map[string]any)
	if _, ok := all2["children"]; !ok {
		t.Fatalf("one group must produce children key; got %#v", all2)
	}
}

func TestGroupVars_GlobalFactsOnlyStillWritesAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := &InventoryRenderer{}
	_, err := r.Render(dir, Inventory{
		Hosts:       []Host{{Name: "h", Address: "1.1.1.1"}},
		GlobalFacts: map[string]any{"fact_key": "fact_val"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "group_vars", "all.yml"))
	if err != nil {
		t.Fatalf("all.yml must exist when only GlobalFacts set: %v", err)
	}
	if !strings.Contains(string(data), "fact_key") {
		t.Fatalf("all.yml missing fact: %s", data)
	}

	// Neither set: all.yml must NOT be written.
	dir2 := t.TempDir()
	_, err = r.Render(dir2, Inventory{Hosts: []Host{{Name: "h", Address: "1.1.1.1"}}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir2, "group_vars", "all.yml")); !os.IsNotExist(statErr) {
		t.Fatalf("all.yml must not exist when no globals; stat err=%v", statErr)
	}
}

func TestRecapChanged_Boundary(t *testing.T) {
	t.Parallel()
	// changed=0 → Changed()==false (gate is > 0, not >= 0).
	zero := &RecapOutputer{Hosts: map[string]RecapHost{"h": {Changed: 0}}}
	if zero.Changed() {
		t.Fatal("changed=0 must report Changed()=false")
	}
	one := &RecapOutputer{Hosts: map[string]RecapHost{"h": {Changed: 1}}}
	if !one.Changed() {
		t.Fatal("changed=1 must report Changed()=true")
	}
}

func TestLineOutputer_PreservesLineOver64KiB(t *testing.T) {
	t.Parallel()
	// Buffer ceiling raised to 1 MiB; a 128 KiB line must pass through intact
	// (catches mutants shrinking the 64*1024 initial or 1024*1024 max).
	big := strings.Repeat("z", 128*1024)
	var buf bytes.Buffer
	out := &LineOutputer{W: &buf}
	if err := out.Print(context.Background(), strings.NewReader(big+"\n"), nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if buf.String() != big+"\n" {
		t.Fatalf("128KiB line truncated: got %d bytes, want %d", buf.Len(), len(big)+1)
	}
}

func TestRecapOutputer_PrefixGate(t *testing.T) {
	t.Parallel()
	// With a Prefix set, each line is prepended with it.
	var buf bytes.Buffer
	out := &RecapOutputer{W: &buf, Prefix: "[mb] "}
	if err := out.Print(context.Background(), strings.NewReader("line one\nline two\n"), nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if buf.String() != "[mb] line one\n[mb] line two\n" {
		t.Fatalf("prefix not applied per line; got %q", buf.String())
	}

	// Without a Prefix, lines pass through raw.
	var buf2 bytes.Buffer
	out2 := &RecapOutputer{W: &buf2}
	if err := out2.Print(context.Background(), strings.NewReader("raw\n"), nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if buf2.String() != "raw\n" {
		t.Fatalf("no-prefix must pass through raw; got %q", buf2.String())
	}
}

func TestRecapOutputer_PreservesLineOver64KiB(t *testing.T) {
	t.Parallel()
	// RecapOutputer raises the scanner ceiling to 1 MiB; a 128 KiB line must
	// pass through intact (catches mutants shrinking the buffer max).
	big := strings.Repeat("z", 128*1024)
	var buf bytes.Buffer
	out := &RecapOutputer{W: &buf}
	if err := out.Print(context.Background(), strings.NewReader(big+"\n"), nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if buf.String() != big+"\n" {
		t.Fatalf("128KiB recap line truncated: got %d bytes, want %d", buf.Len(), len(big)+1)
	}
}

func TestPreview_BinaryAndExtraVarsGate(t *testing.T) {
	t.Parallel()
	// Binary set → it leads the argv.
	withBin := &Executor{Binary: "/custom/ansible-playbook"}
	argv, err := withBin.Preview(ExecuteOptions{Playbook: "/p.yml", Inventory: "/i.yml"})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if argv[0] != "/custom/ansible-playbook" {
		t.Fatalf("custom binary must lead argv; got %v", argv)
	}

	// No ExtraVars → no extra-vars-file shape in argv.
	noVars := strings.Join(argv, " ")
	if strings.Contains(noVars, "extra-vars-file") {
		t.Fatalf("no ExtraVars must omit extra-vars-file; got %s", noVars)
	}

	// ExtraVars present → shape appears.
	withVars, err := (&Executor{}).Preview(ExecuteOptions{
		Playbook: "/p.yml", Inventory: "/i.yml",
		ExtraVars: map[string]any{"k": "v"},
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !strings.Contains(strings.Join(withVars, " "), "@<extra-vars-file>") {
		t.Fatalf("ExtraVars must show file shape; got %v", withVars)
	}
}
