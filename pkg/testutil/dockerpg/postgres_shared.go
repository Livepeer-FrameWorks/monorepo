package dockerpg

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// PostgresDSNEnv opts integration tests into a suite-owned native PostgreSQL
// server. Each test still creates a fresh database and applies the complete baseline.
const PostgresDSNEnv = "FRAMEWORKS_POSTGRES_TEST_DSN"

var nativePostgresSequence atomic.Uint64

func OpenNativePostgresBaseline(t testing.TB, prefix string, baseline []byte) (*sql.DB, string, bool) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(PostgresDSNEnv))
	if raw == "" {
		return nil, "", false
	}
	address, err := url.Parse(raw)
	if err != nil || address.Scheme != "postgres" || (address.Hostname() != "127.0.0.1" && address.Hostname() != "localhost" && address.Hostname() != "::1") {
		t.Fatalf("%s must name a loopback suite-owned PostgreSQL server", PostgresDSNEnv)
	}
	admin, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	prefix = regexp.MustCompile(`[^a-z0-9_]`).ReplaceAllString(strings.ToLower(prefix), "_")
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	name := fmt.Sprintf("fw_%s_%d_%d", prefix, os.Getpid(), nativePostgresSequence.Add(1))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, createErr := admin.ExecContext(ctx, "CREATE DATABASE "+name); createErr != nil {
		t.Fatal(createErr)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, dropErr := admin.ExecContext(cleanupCtx, "DROP DATABASE "+name+" WITH (FORCE)"); dropErr != nil {
			t.Errorf("drop native fixture %s: %v", name, dropErr)
		}
	})
	address.Path = "/" + name
	conn, err := sql.Open("postgres", address.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, string(baseline)); err != nil {
		t.Fatalf("apply complete baseline to native PostgreSQL: %v", err)
	}
	return conn, address.String(), true
}
