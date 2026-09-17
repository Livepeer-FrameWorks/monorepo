package handlers

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/listmonk"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/turnstile"
)

type EmailSender interface {
	SendMail(ctx context.Context, to, subject, htmlBody string) error
}

type TurnstileVerifier interface {
	Verify(ctx context.Context, token, remoteIP string) (*turnstile.VerifyResponse, error)
}

type ListmonkClient interface {
	Subscribe(ctx context.Context, email, name string, listID int, preconfirm bool) error
	GetSubscriber(ctx context.Context, email string) (*listmonk.SubscriberInfo, bool, error)
}

// ActivityEmitter publishes a redacted fact after Steward's primary action
// succeeds. Delivery is best-effort and never changes the HTTP result.
type ActivityEmitter interface {
	EmitActivity(ctx context.Context, eventType string) error
}
