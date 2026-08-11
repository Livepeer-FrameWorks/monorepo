package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// emitClusterDescriptor backs the `cluster storage descriptor` command the destructive thumbnail cutover consumes.
// It must emit valid JSON, select the right cluster, fail closed on absent/backendless/incomplete clusters, round-trip
// PRINTABLE special characters (JSON, not a positional format), and REJECT control characters (a sane prefix grammar
// for a destructive path).
func TestEmitClusterDescriptor(t *testing.T) {
	p := func(s string) *string { return &s }
	m := &inventory.Manifest{Clusters: map[string]inventory.ClusterConfig{
		"media-eu-1": {S3Bucket: "frameworks", S3Endpoint: "https://eu.example.com", S3Prefix: p("prod")},
		"no-bucket":  {S3Endpoint: "https://x", S3Prefix: p("prod")},
		"no-prefix":  {S3Bucket: "b", S3Endpoint: "https://e"},                          // S3Prefix nil
		"pipe":       {S3Bucket: "b", S3Endpoint: "https://e", S3Prefix: p("pre|fix")},  // printable special: round-trips
		"ctrl":       {S3Bucket: "b", S3Endpoint: "https://e", S3Prefix: p("pre\nfix")}, // control char: refused
	}}

	type desc struct {
		Bucket   string `json:"bucket"`
		Prefix   string `json:"prefix"`
		Endpoint string `json:"endpoint"`
	}
	decode := func(t *testing.T, b []byte) desc {
		t.Helper()
		var d desc
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("output is not valid JSON: %v (%q)", err, string(b))
		}
		return d
	}

	var buf bytes.Buffer
	if err := emitClusterDescriptor(&buf, m, "media-eu-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d := decode(t, buf.Bytes()); d.Bucket != "frameworks" || d.Prefix != "prod" || d.Endpoint != "https://eu.example.com" {
		t.Fatalf("wrong descriptor: %+v", d)
	}

	// A prefix with a PRINTABLE special char ('|') round-trips exactly (a positional format would shift the endpoint).
	buf.Reset()
	if err := emitClusterDescriptor(&buf, m, "pipe"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d := decode(t, buf.Bytes()); d.Prefix != "pre|fix" || d.Endpoint != "https://e" {
		t.Fatalf("printable-special prefix corrupted fields: %+v", d)
	}

	// Output purity: exactly one clean JSON line, nothing else on the stream.
	buf.Reset()
	if err := emitClusterDescriptor(&buf, m, "media-eu-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out := buf.String(); strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") || !strings.HasPrefix(out, "{") {
		t.Fatalf("output is not a single clean JSON line: %q", out)
	}

	// Selection + validation failures.
	buf.Reset()
	if err := emitClusterDescriptor(&buf, m, "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
	buf.Reset()
	if err := emitClusterDescriptor(&buf, m, "no-bucket"); err == nil || !strings.Contains(err.Error(), "no s3_bucket") {
		t.Fatalf("want no-bucket error, got %v", err)
	}
	buf.Reset()
	if err := emitClusterDescriptor(&buf, m, "no-prefix"); err == nil || !strings.Contains(err.Error(), "no s3_prefix") {
		t.Fatalf("want no-prefix error, got %v", err)
	}
	buf.Reset()
	if err := emitClusterDescriptor(&buf, m, "ctrl"); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("want control-character rejection, got %v", err)
	}
}
