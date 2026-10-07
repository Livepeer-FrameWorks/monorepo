//go:build schema_verify

package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_dns/internal/provider/bunny"
	"frameworks/api_dns/internal/store"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

// A Navigator running with public DNS records disabled still receives
// Quartermaster's remove and retire hand-offs. The started worker must drain
// them from local state without calling the DNS provider.
func TestTenantAliasTeardownCompletesWithDNSRecordsDisabled_RealPG(t *testing.T) {
	db := startTenantAliasWorkerRealPG(t)
	enc, err := fieldcrypt.DeriveFieldEncryptor([]byte("navigator-worker-real-postgres-secret"), "navigator-worker")
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewStore(db, enc)
	ctx := context.Background()
	removedTenant := "20000000-0000-0000-0000-000000000001"
	renamedTenant := "20000000-0000-0000-0000-000000000002"

	issued := func(tenantID, subdomain string) {
		t.Helper()
		alias, ensureErr := st.EnsureTenantAlias(ctx, tenantID, subdomain)
		if ensureErr != nil {
			t.Fatal(ensureErr)
		}
		if ok, setErr := st.SetTenantAliasStatus(ctx, tenantID, subdomain, alias.AuthorityVersion, "cert_issued", ""); setErr != nil || !ok {
			t.Fatalf("issue %s ok=%v err=%v", tenantID, ok, setErr)
		}
	}
	issued(removedTenant, "removed")
	if _, err := st.SetTenantAliasStatus(ctx, removedTenant, "", 0, "tearing_down", ""); err != nil {
		t.Fatal(err)
	}
	issued(renamedTenant, "newlabel")
	if err := st.InsertTenantAliasRetirement(ctx, renamedTenant, "oldlabel"); err != nil {
		t.Fatal(err)
	}

	provider := &countingTenantAliasDNS{}
	logger := logging.NewLogger()
	logger.SetOutput(io.Discard)
	w := &AliasApplyStateWorker{
		store:              st,
		bunny:              provider,
		edges:              &fakeTenantEdgeResolver{},
		logger:             logger,
		interval:           50 * time.Millisecond,
		rootDomain:         "frameworks.network",
		tenantZoneLabel:    "cdn",
		healthStaleSeconds: 300,
	}
	w.SetDNSRecordsEnabled(false)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Start(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, getErr := st.GetTenantAlias(ctx, removedTenant)
		labels, listErr := st.ListTenantAliasRetirementLabels(ctx, renamedTenant)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if errors.Is(getErr, store.ErrNotFound) && len(labels) == 0 {
			break
		}
		if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
			t.Fatal(getErr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not drain with DNS records disabled: removed alias lookup err=%v, pending retirements=%v", getErr, labels)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("DNS provider received %d calls with public DNS records disabled", calls)
	}
	active, err := st.GetTenantAlias(ctx, renamedTenant)
	if err != nil || active.Status != "cert_issued" || active.Subdomain != "newlabel" {
		t.Fatalf("active alias = %#v, err = %v; want cert_issued newlabel untouched", active, err)
	}
}

type countingTenantAliasDNS struct {
	calls atomic.Int64
}

func (d *countingTenantAliasDNS) FindZone(context.Context, string) (*bunny.Zone, bool, error) {
	d.calls.Add(1)
	return &bunny.Zone{ID: 1, Domain: "cdn.frameworks.network"}, true, nil
}

func (d *countingTenantAliasDNS) ReconcileRecordSet(context.Context, int64, string, int, []bunny.Record) error {
	d.calls.Add(1)
	return nil
}

func startTenantAliasWorkerRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-navigator-worker-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/navigator.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
