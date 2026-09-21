package social

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type Publisher interface {
	Publish(ctx context.Context, post PostRecord) error
}

type EmailPublisherConfig struct {
	Sender         *email.Sender
	SMTP           email.Config
	Branding       config.EmailBranding
	BrandingSource func() config.EmailBranding
	To             string
	Logger         logging.Logger
}

type EmailPublisher struct {
	sender         *email.Sender
	smtp           email.Config
	branding       config.EmailBranding
	brandingSource func() config.EmailBranding
	to             string
	logger         logging.Logger
}

func NewEmailPublisher(cfg EmailPublisherConfig) *EmailPublisher {
	return &EmailPublisher{
		sender:         cfg.Sender,
		smtp:           cfg.SMTP,
		branding:       cfg.Branding,
		brandingSource: cfg.BrandingSource,
		to:             cfg.To,
		logger:         cfg.Logger,
	}
}

func (p *EmailPublisher) Publish(ctx context.Context, post PostRecord) error {
	if p.smtp.Host == "" || p.smtp.From == "" {
		p.logger.Warn("Social publisher: SMTP not configured, skipping email")
		return nil
	}
	if p.to == "" {
		p.logger.Warn("Social publisher: no recipient configured, skipping email")
		return nil
	}

	subject := socialEmailSubject(post)
	branding := p.branding
	if p.brandingSource != nil {
		branding = p.brandingSource()
	}
	body, err := renderSocialEmail(post, branding)
	if err != nil {
		return fmt.Errorf("render social email: %w", err)
	}

	if err := p.sender.SendMail(ctx, p.to, subject, body); err != nil {
		return fmt.Errorf("send social email: %w", err)
	}

	p.logger.WithField("post_id", post.ID).Info("Social draft email sent")
	return nil
}

func socialEmailSubject(post PostRecord) string {
	preview := post.TweetText
	if len(preview) > 60 {
		preview = preview[:57] + "..."
	}
	return fmt.Sprintf("[FrameWorks] Social Draft: %s", preview)
}
