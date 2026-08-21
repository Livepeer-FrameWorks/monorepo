//go:build schema_verify

package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"frameworks/api_consultant/internal/skipper"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestConversationQueryPack_RealPG(t *testing.T) {
	db := startSkipperConversationRealPG(t)
	store := NewConversationStore(db)
	tenantA := "10000000-0000-0000-0000-000000000001"
	tenantB := "20000000-0000-0000-0000-000000000002"
	userA := "30000000-0000-0000-0000-000000000003"
	userB := "40000000-0000-0000-0000-000000000004"

	conversationID, err := store.CreateConversation(context.Background(), tenantA, userA)
	if err != nil {
		t.Fatal(err)
	}
	platformConversationID, err := store.CreateConversation(context.Background(), tenantA, "")
	if err != nil {
		t.Fatal(err)
	}
	ctxA := skipper.WithUserID(skipper.WithTenantID(context.Background(), tenantA), userA)
	ctxB := skipper.WithUserID(skipper.WithTenantID(context.Background(), tenantA), userB)
	ctxOtherTenant := skipper.WithUserID(skipper.WithTenantID(context.Background(), tenantB), userA)

	if err := store.AddMessage(ctxA, conversationID, "user", "hello", "", json.RawMessage(`[]`), nil, json.RawMessage(`{"verified":true}`), TokenCounts{Input: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(ctxA, conversationID, "assistant", "world", "verified", json.RawMessage(`[{"url":"https://example.test"}]`), json.RawMessage(`[]`), nil, TokenCounts{Input: 3, Output: 5}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(ctxOtherTenant, conversationID, "assistant", "leak", "", nil, nil, nil, TokenCounts{}); err == nil {
		t.Fatal("cross-tenant message insert succeeded")
	}

	conversation, err := store.GetConversation(ctxA, conversationID)
	if err != nil || len(conversation.Messages) != 2 || conversation.Messages[1].Content != "world" {
		t.Fatalf("conversation = %#v, err = %v", conversation, err)
	}
	if _, err := store.GetConversation(ctxB, conversationID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-user get error = %v", err)
	}
	if _, err := store.GetConversation(ctxOtherTenant, conversationID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-tenant get error = %v", err)
	}

	if rows, err := store.ListConversations(context.Background(), tenantA, userA, 10, 0); err != nil || len(rows) != 1 || !rows[0].LastMessageAt.Valid || rows[0].MessageCount != 2 {
		t.Fatalf("user conversations = %#v, err = %v", rows, err)
	}
	if rows, err := store.ListConversations(context.Background(), tenantA, "", 10, 0); err != nil || len(rows) != 2 {
		t.Fatalf("tenant conversations = %#v, err = %v", rows, err)
	}
	if rows, err := store.ListConversations(context.Background(), tenantA, "", 1, 1); err != nil || len(rows) != 1 {
		t.Fatalf("paged conversations = %#v, err = %v", rows, err)
	}
	if recent, err := store.GetRecentMessages(ctxA, conversationID, 1); err != nil || len(recent) != 1 || recent[0].Content != "world" {
		t.Fatalf("recent messages = %#v, err = %v", recent, err)
	}

	if err := store.UpdateSummary(ctxA, conversationID, "summary"); err != nil {
		t.Fatal(err)
	}
	if summary, err := store.GetSummary(ctxA, conversationID); err != nil || summary != "summary" {
		t.Fatalf("summary = %q, err = %v", summary, err)
	}
	if count, err := store.MessageCount(ctxA, conversationID); err != nil || count != 2 {
		t.Fatalf("message count = %d, err = %v", count, err)
	}
	if err := store.UpdateTitle(ctxB, conversationID, "forbidden"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-user title error = %v", err)
	}
	if err := store.UpdateTitle(ctxA, conversationID, "allowed"); err != nil {
		t.Fatal(err)
	}

	ctxTenantOnly := skipper.WithTenantID(context.Background(), tenantA)
	if err := store.DeleteConversation(ctxB, conversationID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-user delete error = %v", err)
	}
	if count, err := store.MessageCount(ctxA, conversationID); err != nil || count != 2 {
		t.Fatalf("failed delete changed messages: count = %d, err = %v", count, err)
	}
	if err := store.DeleteConversation(ctxA, conversationID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteConversation(ctxTenantOnly, platformConversationID); err != nil {
		t.Fatal(err)
	}
}

func startSkipperConversationRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-skipper-conversation-realpg-%d", time.Now().UnixNano())
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
