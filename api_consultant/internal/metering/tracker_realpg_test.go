//go:build schema_verify

package metering

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

type contractUsagePublisher struct {
	events []*ipcpb.ServiceEvent
	err    error
}

func (p *contractUsagePublisher) SendServiceEvent(event *ipcpb.ServiceEvent) error {
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, event)
	return nil
}

func TestUsagePublicationRepository_RealPG(t *testing.T) {
	db := startSkipperMeteringRealPG(t)
	tenantID := "10000000-0000-0000-0000-000000000001"
	failing := &contractUsagePublisher{err: errors.New("publisher unavailable")}
	tracker := NewUsageTracker(UsageTrackerConfig{DB: db, Publisher: failing})
	ctx := context.Background()

	if err := tracker.insertUsageRow(ctx, tenantID, "llm_call", 2, 30, 40, "gpt-contract", "openai"); err != nil {
		t.Fatal(err)
	}
	if err := tracker.insertUsageRow(ctx, tenantID, "search_query", 1, 0, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := tracker.insertUsageRow(ctx, tenantID, "ignored", 0, 0, 0, "", ""); err != nil {
		t.Fatal(err)
	}

	claimed, err := tracker.claimPending(ctx)
	if err != nil || len(claimed) != 2 || claimed[0].eventType != "llm_call" || claimed[0].model != "gpt-contract" || claimed[1].model != "" {
		t.Fatalf("claimed = %#v, err = %v", claimed, err)
	}
	if second, err := tracker.claimPending(ctx); err != nil || len(second) != 0 {
		t.Fatalf("second claim = %#v, err = %v", second, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE skipper.skipper_usage SET claimed_at = NULL"); err != nil {
		t.Fatal(err)
	}

	if err := tracker.publishPending(ctx); err == nil {
		t.Fatal("publication failure was not returned")
	}
	var attempts int
	var claimedAt sql.NullTime
	var lastError sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT attempts, claimed_at, last_error FROM skipper.skipper_usage WHERE event_type = 'llm_call'`).Scan(&attempts, &claimedAt, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || claimedAt.Valid || lastError.String != "publisher unavailable" {
		t.Fatalf("failed publication state: attempts=%d claimed=%v error=%q", attempts, claimedAt.Valid, lastError.String)
	}

	success := &contractUsagePublisher{}
	tracker.publisher = success
	if err := tracker.publishPending(ctx); err != nil {
		t.Fatal(err)
	}
	if len(success.events) != 2 || success.events[0].GetTenantId() != tenantID || success.events[0].GetEventId() == "" {
		t.Fatalf("published events = %#v", success.events)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM skipper.skipper_usage WHERE published_at IS NULL OR claimed_at IS NOT NULL OR last_error IS NOT NULL").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("unsettled usage rows = %d", remaining)
	}
}

func startSkipperMeteringRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-skipper-metering-realpg-%d", time.Now().UnixNano())
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
	schema, err := dbsql.Content.ReadFile("schema/skipper.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
