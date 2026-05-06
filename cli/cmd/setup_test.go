package cmd

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

func TestPromptOwnerControlPlaneStoresTLSTransportByDefault(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("qm.example.test:19002\nfh.example.test:18019\n\n"))
	var out bytes.Buffer
	var ctx fwcfg.Context
	if err := promptOwnerControlPlane(reader, &out, &ctx); err != nil {
		t.Fatalf("promptOwnerControlPlane: %v", err)
	}
	if !ctx.Endpoints.UseTLS {
		t.Fatal("UseTLS = false, want true by default")
	}
	if ctx.Endpoints.AllowInsecure {
		t.Fatal("AllowInsecure = true, want false by default")
	}
}

func TestPromptOwnerControlPlaneStoresPlaintextWhenExplicit(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("qm.internal:19002\nfh.internal:18019\nplaintext\n"))
	var out bytes.Buffer
	var ctx fwcfg.Context
	if err := promptOwnerControlPlane(reader, &out, &ctx); err != nil {
		t.Fatalf("promptOwnerControlPlane: %v", err)
	}
	if ctx.Endpoints.UseTLS {
		t.Fatal("UseTLS = true, want false for explicit plaintext")
	}
	if !ctx.Endpoints.AllowInsecure {
		t.Fatal("AllowInsecure = false, want true for explicit plaintext")
	}
}
