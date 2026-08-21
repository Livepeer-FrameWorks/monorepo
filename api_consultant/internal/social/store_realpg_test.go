//go:build schema_verify

package social

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestSocialPostRepository_RealPG(t *testing.T) {
	db := startSkipperSocialRealPG(t)
	ctx := context.Background()
	tenantA := "10000000-0000-0000-0000-000000000001"
	tenantB := "20000000-0000-0000-0000-000000000002"
	storeA := NewPostStore(db, tenantA)
	storeB := NewPostStore(db, tenantB)

	first, err := storeA.Save(ctx, PostRecord{
		ContentType: ContentPlatformStats, TweetText: "first", ContextSummary: "context",
		TriggerData: map[string]any{"viewers": float64(42)},
	})
	if err != nil || first.ID == "" || first.Status != "draft" || first.CreatedAt.IsZero() {
		t.Fatalf("first post = %#v, err = %v", first, err)
	}
	if _, err := storeA.Save(ctx, PostRecord{ContentType: ContentFederation, TweetText: "second", Status: "baseline"}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.Save(ctx, PostRecord{ContentType: ContentKnowledge, TweetText: "other"}); err != nil {
		t.Fatal(err)
	}
	if count, err := storeA.CountToday(ctx); err != nil || count != 1 {
		t.Fatalf("today count = %d, err = %v", count, err)
	}
	rows, err := storeA.ListRecent(ctx, 10)
	if err != nil || len(rows) != 2 || rows[0].TweetText == "other" {
		t.Fatalf("recent posts = %#v, err = %v", rows, err)
	}
	var found PostRecord
	for _, row := range rows {
		if row.ID == first.ID {
			found = row
		}
	}
	if found.ContextSummary != "context" || found.TriggerData["viewers"] != float64(42) || found.SentAt != nil {
		t.Fatalf("decoded post = %#v", found)
	}
	if err := storeB.MarkSent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := storeA.MarkSent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = storeA.ListRecent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == first.ID && (row.Status != "sent" || row.SentAt == nil) {
			t.Fatalf("sent post = %#v", row)
		}
	}
}

func startSkipperSocialRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-skipper-social-realpg-%d", time.Now().UnixNano())
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
