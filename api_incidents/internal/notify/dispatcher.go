package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"frameworks/api_incidents/internal/config"
	"frameworks/api_incidents/internal/incidents"

	pkgconfig "github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

const webhookTimeout = 10 * time.Second

// MailSender sends one HTML email.
type MailSender interface {
	SendMail(ctx context.Context, to, subject, htmlBody string) error
}

// Producer publishes one Kafka record.
type Producer interface {
	ProduceMessage(topic string, key, value []byte, headers map[string]string) error
}

// Dispatcher delivers claimed outbox rows.
type Dispatcher struct {
	Channels ChannelChecker
	HTTP     *http.Client
	// Mailer builds a sender per delivery so SMTP settings follow env reloads.
	Mailer   func() (MailSender, error)
	Producer Producer
	Topic    string
	Logger   logging.Logger
	Metrics  *incidents.Metrics
}

var _ outbox.Dispatcher[Delivery] = (*Dispatcher)(nil)

// Dispatch delivers one row. An operator channel whose destination was removed
// after the row was queued is skipped and settled without retrying.
func (d *Dispatcher) Dispatch(ctx context.Context, delivery Delivery) ([]string, error) {
	var err error
	switch delivery.Channel {
	case incidents.ChannelKafka:
		err = d.publishIncident(delivery)
	case incidents.ChannelEmail, incidents.ChannelSlack, incidents.ChannelDiscord:
		if d.Channels != nil && !d.Channels.Enabled(delivery.Channel) {
			d.Metrics.ObserveDelivery(delivery.Channel, "skipped")
			if d.Logger != nil {
				d.Logger.WithField("channel", delivery.Channel).WithField("incident_id", delivery.IncidentID).
					Info("Skipping notification for a channel that is no longer configured")
			}
			return nil, nil
		}
		err = d.notify(ctx, delivery)
	default:
		err = fmt.Errorf("unknown delivery channel %q", delivery.Channel)
	}
	if err != nil {
		d.Metrics.ObserveDelivery(delivery.Channel, "error")
		return []string{delivery.Channel}, err
	}
	d.Metrics.ObserveDelivery(delivery.Channel, "delivered")
	return nil, nil
}

func (d *Dispatcher) notify(ctx context.Context, delivery Delivery) error {
	var payload incidents.DeliveryPayload
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		return fmt.Errorf("decode notification payload: %w", err)
	}
	m := buildMessage(payload, config.WebappPublicURL())
	switch delivery.Channel {
	case incidents.ChannelSlack:
		body, err := slackBody(m)
		if err != nil {
			return err
		}
		return d.postJSON(ctx, "slack", config.SlackWebhookURL(), body)
	case incidents.ChannelDiscord:
		body, err := discordBody(m)
		if err != nil {
			return err
		}
		return d.postJSON(ctx, "discord", config.DiscordWebhookURL(), body)
	default:
		return d.sendEmail(ctx, m)
	}
}

func (d *Dispatcher) sendEmail(ctx context.Context, m message) error {
	if d.Mailer == nil {
		return errors.New("email sender is not configured")
	}
	sender, err := d.Mailer()
	if err != nil {
		return err
	}
	subject, body := emailContent(m)
	var errs []error
	for _, recipient := range config.NotifyEmailRecipients() {
		if sendErr := sender.SendMail(ctx, recipient, subject, body); sendErr != nil {
			errs = append(errs, fmt.Errorf("send email to %s: %w", recipient, sendErr))
		}
	}
	return errors.Join(errs...)
}

// postJSON posts a webhook body. Transport errors are unwrapped from
// *url.Error because the webhook URL is a credential and must not reach logs
// or the outbox last_error column.
func (d *Dispatcher) postJSON(ctx context.Context, channel, target string, body []byte) error {
	if target == "" {
		return fmt.Errorf("%s webhook URL is not configured", channel)
	}
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s webhook request: invalid webhook URL", channel)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("post %s webhook: %w", channel, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && d.Logger != nil {
			d.Logger.WithError(closeErr).WithField("channel", channel).Debug("Closing webhook response body failed")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("post %s webhook: unexpected status %d", channel, resp.StatusCode)
	}
	// The delivery succeeded once the status is 2xx; failing to drain the body
	// only costs connection reuse and must not trigger a duplicate post.
	if _, drainErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)); drainErr != nil && d.Logger != nil {
		d.Logger.WithError(drainErr).WithField("channel", channel).Debug("Draining webhook response body failed")
	}
	return nil
}

func (d *Dispatcher) publishIncident(delivery Delivery) error {
	if d.Producer == nil {
		return errors.New("kafka producer is not configured")
	}
	headers := map[string]string{"event_type": "incident_opened", "source": "lookout"}
	if delivery.TenantID != "" {
		headers["tenant_id"] = delivery.TenantID
	}
	if err := d.Producer.ProduceMessage(d.Topic, []byte(delivery.IncidentID), delivery.Payload, headers); err != nil {
		return fmt.Errorf("publish incident: %w", err)
	}
	return nil
}

// SMTPMailer builds a pkg/email sender from the shared SMTP environment.
func SMTPMailer() (MailSender, error) {
	host := pkgconfig.GetEnv("SMTP_HOST", "")
	if host == "" {
		return nil, errors.New("SMTP_HOST is not configured")
	}
	return email.NewSender(email.Config{
		Host:     host,
		Port:     pkgconfig.GetEnv("SMTP_PORT", "587"),
		User:     pkgconfig.GetEnv("SMTP_USER", ""),
		Password: pkgconfig.GetEnv("SMTP_PASSWORD", ""),
		From:     pkgconfig.GetEnv("FROM_EMAIL", "noreply@frameworks.network"),
		FromName: pkgconfig.GetEnv("FROM_NAME", ""),
	}), nil
}
