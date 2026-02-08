package handlers

import (
	"context"
	"frameworks/pkg/clients/listmonk"
	"frameworks/pkg/turnstile"
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
