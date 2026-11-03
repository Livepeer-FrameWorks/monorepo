package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeGrantNoticeClient struct {
	requests []*purserpb.AdminResendOperatorGrantNoticeRequest
}

func (f *fakeGrantNoticeClient) AdminResendOperatorGrantNotice(_ context.Context, r *purserpb.AdminResendOperatorGrantNoticeRequest) (*purserpb.AdminResendOperatorGrantNoticeResponse, error) {
	f.requests = append(f.requests, r)
	ends := timestamppb.New(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if r.GetConfirmed() {
		return &purserpb.AdminResendOperatorGrantNoticeResponse{Status: "queued", Recipient: "billing@example.test", Kind: r.GetKind(), EndsAt: r.GetEndsAt(), Detail: "queued for the worker"}, nil
	}
	return &purserpb.AdminResendOperatorGrantNoticeResponse{Status: "notice_abandoned", Recipient: "billing@example.test", Kind: "ended", EndsAt: ends, Detail: "550 mailbox unavailable"}, nil
}

// The confirmation names exactly the previewed notice; a declined prompt
// sends nothing past the preview.
func TestGrantNoticeRecoveryConfirmsThePreviewedNotice(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	run := func(stdin string, yes bool) (*fakeGrantNoticeClient, string, error) {
		client := &fakeGrantNoticeClient{}
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetIn(strings.NewReader(stdin))
		err := runGrantNoticeRecovery(cmd, client, "jwt", tenant, "billing email fixed", yes, false)
		return client, out.String(), err
	}

	client, out, err := run("no\n", false)
	if err == nil || !strings.Contains(err.Error(), "declined") || len(client.requests) != 1 || client.requests[0].GetConfirmed() {
		t.Fatalf("declined retry: err=%v requests=%v", err, client.requests)
	}
	if !strings.Contains(out, "Grant ended notice for 2026-12-01T00:00:00Z to billing@example.test: notice_abandoned. 550 mailbox unavailable") {
		t.Fatalf("preview output=%q", out)
	}

	client, out, err = run("yes\n", false)
	if err != nil {
		t.Fatal(err)
	}
	confirm := client.requests[len(client.requests)-1]
	if len(client.requests) != 2 || !confirm.GetConfirmed() || confirm.GetKind() != "ended" ||
		!confirm.GetEndsAt().AsTime().Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) || confirm.GetReason() != "billing email fixed" {
		t.Fatalf("confirmation = %+v", confirm)
	}
	if !strings.Contains(out, "Grant notice: queued (recipient billing@example.test)") {
		t.Fatalf("output=%q", out)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err = runGrantNoticeRecovery(cmd, &fakeGrantNoticeClient{}, "jwt", tenant, " ", true, false); err == nil || !strings.Contains(err.Error(), "--reason is required") {
		t.Fatalf("missing reason: %v", err)
	}
}
