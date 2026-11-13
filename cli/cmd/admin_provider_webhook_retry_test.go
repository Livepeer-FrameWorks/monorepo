package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type webhookRetryFake struct {
	calls   []*purserpb.AdminRetryProviderWebhookRequest
	preview *purserpb.AdminRetryProviderWebhookResponse
}

func (f *webhookRetryFake) AdminRetryProviderWebhook(_ context.Context, r *purserpb.AdminRetryProviderWebhookRequest) (*purserpb.AdminRetryProviderWebhookResponse, error) {
	f.calls = append(f.calls, proto.Clone(r).(*purserpb.AdminRetryProviderWebhookRequest))
	if r.GetConfirmed() {
		return &purserpb.AdminRetryProviderWebhookResponse{RetryRequested: true, AuditId: "audit"}, nil
	}
	if f.preview != nil {
		return f.preview, nil
	}
	return &purserpb.AdminRetryProviderWebhookResponse{Eligible: true, ExpectedState: "previewed-state", Provider: "stripe", Status: "blocked", ErrorCode: "evidence_conflict"}, nil
}

func TestProviderWebhookRetryExplainsMollieConflictWithoutConfirming(t *testing.T) {
	fake := &webhookRetryFake{preview: &purserpb.AdminRetryProviderWebhookResponse{Provider: "mollie", Status: "blocked", ErrorCode: "evidence_conflict"}}
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetErr(&bytes.Buffer{})
	req := &purserpb.AdminRetryProviderWebhookRequest{TenantId: "00000000-0000-4000-8000-000000000001", WebhookId: "00000000-0000-4000-8000-000000000002", Reason: "review"}
	err := runProviderWebhookRetry(cmd, fake, "jwt", req, true, false)
	if err == nil || !strings.Contains(err.Error(), "cannot recheck the settlement") || len(fake.calls) != 1 {
		t.Fatalf("conflict silently confirmed or unexplained: %v %+v", err, fake.calls)
	}
}

func TestProviderWebhookRetryRequiresConfirmationOfPreview(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		fake := &webhookRetryFake{}
		cmd := &cobra.Command{}
		cmd.SetContext(t.Context())
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetIn(bytes.NewBufferString("no\n"))
		req := &purserpb.AdminRetryProviderWebhookRequest{TenantId: "00000000-0000-4000-8000-000000000001", WebhookId: "00000000-0000-4000-8000-000000000002", Reason: "precision repair deployed"}
		err := runProviderWebhookRetry(cmd, fake, "jwt", req, confirmed, false)
		if confirmed {
			if err != nil || len(fake.calls) != 2 || !fake.calls[1].Confirmed || fake.calls[1].ExpectedState != "previewed-state" {
				t.Fatalf("confirmation did not bind preview: %+v %v", fake.calls, err)
			}
		} else if err == nil || len(fake.calls) != 1 {
			t.Fatalf("declined request still confirmed: %+v %v", fake.calls, err)
		}
		if fake.calls[0].Confirmed || fake.calls[0].ExpectedState != "" {
			t.Fatal("preview inherited confirmation state")
		}
	}
}
