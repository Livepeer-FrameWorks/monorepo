package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"
	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
	"github.com/google/uuid"
)

const (
	activityRetryBase = time.Second
	activityRetryMax  = 30 * time.Second
)

// ActivityField is one redacted fact rendered in an operator activity message.
type ActivityField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ActivityPayload is the channel-neutral snapshot persisted for delivery.
type ActivityPayload struct {
	Headline  string          `json:"headline"`
	Summary   string          `json:"summary,omitempty"`
	Category  string          `json:"category"`
	Fields    []ActivityField `json:"fields,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

// OperatorActivity carries the source-event identity used for idempotent enqueue.
type OperatorActivity struct {
	SourceEventID string
	EventType     string
	TenantID      string
	Payload       ActivityPayload
}

// ActivitySink atomically enqueues every configured destination for one event.
type ActivitySink interface {
	EnqueueActivity(ctx context.Context, activity OperatorActivity, channels []string) error
}

type activityDefinition struct {
	source   string
	category string
	headline string
	platform bool
}

var activityDefinitions = map[string]activityDefinition{
	"tenant_created":                         {source: "quartermaster", category: "growth", headline: "New tenant signup"},
	"stream_created":                         {source: "commodore", category: "product", headline: "Stream created"},
	"subscription_created":                   {source: "purser", category: "revenue", headline: "Subscription created"},
	"subscription_canceled":                  {source: "purser", category: "revenue", headline: "Subscription canceled"},
	"payment_succeeded":                      {source: "purser", category: "revenue", headline: "Payment succeeded"},
	"payment_failed":                         {source: "purser", category: "revenue", headline: "Payment failed"},
	"invoice_payment_failed":                 {source: "purser", category: "revenue", headline: "Invoice payment failed"},
	"topup_credited":                         {source: "purser", category: "revenue", headline: "Top-up credited"},
	"topup_failed":                           {source: "purser", category: "revenue", headline: "Top-up failed"},
	"conversation_created":                   {source: "deckhand", category: "support", headline: "Support conversation created"},
	"cluster_subscription_requested":         {source: "quartermaster", category: "operator", headline: "Cluster subscription requested"},
	serviceevents.MarketingContactDelivered:  {source: "steward", category: "growth", headline: "Contact form delivered", platform: true},
	serviceevents.MarketingSubscriberCreated: {source: "steward", category: "growth", headline: "Newsletter subscriber added", platform: true},
}

// ActivityConsumer selects direct source-of-record events and snapshots a
// redacted operator message. It does not infer milestones or query other
// services.
type ActivityConsumer struct {
	Sink     ActivitySink
	Channels ChannelChecker
	Logger   logging.Logger
	Metrics  *incidents.Metrics
	Sleep    func(ctx context.Context, d time.Duration) error
}

func (c *ActivityConsumer) Handle(ctx context.Context, msg kafka.Message) error {
	var event kafka.ServiceEvent
	if err := json.Unmarshal(msg.Value, &event); err != nil {
		c.observe("unknown", "invalid")
		c.warn(err, msg, "Skipping undecodable operator activity event")
		return nil
	}
	fillServiceEventHeaders(&event, msg.Headers)
	definition, ok := activityDefinitions[event.EventType]
	if !ok || event.Source != definition.source {
		return nil
	}
	if event.EventID == "" {
		c.observe(event.EventType, "invalid")
		c.warn(errors.New("missing event_id"), msg, "Skipping operator activity event")
		return nil
	}
	if definition.platform {
		if event.TenantID != "" {
			c.observe(event.EventType, "invalid")
			c.warn(errors.New("platform activity has tenant_id"), msg, "Skipping operator activity event")
			return nil
		}
	} else if !validActivityTenant(event.TenantID) {
		c.observe(event.EventType, "invalid")
		c.warn(errors.New("tenant activity has invalid tenant_id"), msg, "Skipping operator activity event")
		return nil
	}

	channels := activityChannels(c.Channels)
	if len(channels) == 0 {
		c.observe(event.EventType, "disabled")
		return nil
	}
	activity := OperatorActivity{
		SourceEventID: event.EventID,
		EventType:     event.EventType,
		TenantID:      event.TenantID,
		Payload:       activityPayload(event, definition),
	}
	if c.Sink == nil {
		return errors.New("operator activity sink is not configured")
	}
	if err := c.Sink.EnqueueActivity(ctx, activity, channels); err != nil {
		c.observe(event.EventType, "error")
		return err
	}
	c.observe(event.EventType, "enqueued")
	return nil
}

// HandleUntilEnqueued prevents the Kafka partition from advancing past a
// selected event until its idempotent outbox rows exist.
func (c *ActivityConsumer) HandleUntilEnqueued(ctx context.Context, msg kafka.Message) error {
	for attempt := 1; ; attempt++ {
		err := c.Handle(ctx, msg)
		if err == nil {
			return nil
		}
		delay := activityRetryBase
		for i := 1; i < attempt && delay < activityRetryMax; i++ {
			delay *= 2
		}
		delay = min(delay, activityRetryMax)
		if c.Logger != nil {
			c.Logger.WithError(err).WithFields(logging.Fields{
				"topic": msg.Topic, "partition": msg.Partition, "offset": msg.Offset,
				"attempt": attempt, "retry_in": delay.String(),
			}).Warn("Operator activity event not enqueued; retrying")
		}
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

func activityChannels(checker ChannelChecker) []string {
	var channels []string
	for _, channel := range []string{incidents.ChannelSlack, incidents.ChannelDiscord} {
		if checker != nil && checker.Enabled(channel) {
			channels = append(channels, channel)
		}
	}
	return channels
}

func activityPayload(event kafka.ServiceEvent, definition activityDefinition) ActivityPayload {
	payload := ActivityPayload{
		Headline:  definition.headline,
		Category:  definition.category,
		Timestamp: event.Timestamp,
	}
	if event.TenantID != "" {
		payload.Fields = append(payload.Fields, ActivityField{Name: "Tenant", Value: event.TenantID})
	}
	if id := firstString(event.ResourceID, dataString(event.Data, "stream_id"), dataString(event.Data, "subscription_id"), dataString(event.Data, "payment_id"), dataString(event.Data, "invoice_id"), dataString(event.Data, "topup_id"), dataString(event.Data, "conversation_id"), dataString(event.Data, "cluster_id")); id != "" {
		payload.Fields = append(payload.Fields, ActivityField{Name: "Resource", Value: id})
	}
	if attribution, ok := event.Data["attribution"].(map[string]any); ok {
		appendActivityField(&payload, "Channel", dataString(attribution, "signup_channel"))
		appendActivityField(&payload, "Method", dataString(attribution, "signup_method"))
		appendActivityField(&payload, "Campaign", dataString(attribution, "utm_campaign"))
		appendActivityField(&payload, "Referral", dataString(attribution, "referral_code"))
	}
	if amount, ok := dataNumber(event.Data, "amount"); ok && amount != 0 {
		value := strconv.FormatFloat(amount, 'f', -1, 64)
		if currency := dataString(event.Data, "currency"); currency != "" {
			value += " " + strings.ToUpper(currency)
		}
		appendActivityField(&payload, "Amount", value)
	}
	appendActivityField(&payload, "Provider", dataString(event.Data, "provider"))
	return payload
}

func appendActivityField(payload *ActivityPayload, name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		payload.Fields = append(payload.Fields, ActivityField{Name: name, Value: value})
	}
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func dataString(data map[string]any, key string) string {
	value, ok := data[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func validActivityTenant(tenantID string) bool {
	_, err := uuid.Parse(tenantID)
	return err == nil
}

func dataNumber(data map[string]any, key string) (float64, bool) {
	value, ok := data[key].(float64)
	return value, ok
}

func fillServiceEventHeaders(event *kafka.ServiceEvent, headers map[string]string) {
	if event.EventID == "" {
		event.EventID = headers["event_id"]
	}
	if event.EventType == "" {
		event.EventType = headers["event_type"]
	}
	if event.Source == "" {
		event.Source = headers["source"]
	}
	if event.TenantID == "" {
		event.TenantID = headers["tenant_id"]
	}
}

func (c *ActivityConsumer) sleep(ctx context.Context, delay time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *ActivityConsumer) observe(eventType, result string) {
	if c.Metrics != nil {
		c.Metrics.ObserveOperatorActivity(eventType, result)
	}
}

func (c *ActivityConsumer) warn(err error, msg kafka.Message, text string) {
	if c.Logger != nil {
		c.Logger.WithError(err).WithFields(logging.Fields{
			"topic": msg.Topic, "partition": msg.Partition, "offset": msg.Offset,
		}).Warn(text)
	}
}

// ActivityDelivery is one claimed activity outbox row.
type ActivityDelivery struct {
	OutboxID      string
	TenantID      string
	SourceEventID string
	EventType     string
	Channel       string
	Payload       json.RawMessage
}

// ActivityStore is both the idempotent event sink and token-fenced worker
// store for lookout.operator_activity_outbox.
type ActivityStore struct {
	DB      *sql.DB
	Metrics *incidents.Metrics
	Logger  logging.Logger
}

var (
	_ ActivitySink                   = (*ActivityStore)(nil)
	_ outbox.Store[ActivityDelivery] = (*ActivityStore)(nil)
	_ outbox.TokenFencedStore        = (*ActivityStore)(nil)
)

func (s *ActivityStore) EnqueueActivity(ctx context.Context, activity OperatorActivity, channels []string) error {
	raw, err := json.Marshal(activity.Payload)
	if err != nil {
		return fmt.Errorf("marshal operator activity: %w", err)
	}
	return database.WithRetryablePostgresTx(ctx, s.DB, nil, func(tx *sql.Tx) error {
		q := lookoutdb.New(tx)
		for _, channel := range channels {
			if err := q.EnqueueOperatorActivity(ctx, lookoutdb.EnqueueOperatorActivityParams{
				SourceEventID: activity.SourceEventID,
				EventType:     activity.EventType,
				TenantID:      nullTenant(activity.TenantID),
				Channel:       channel,
				Payload:       raw,
			}); err != nil {
				return fmt.Errorf("enqueue operator activity: %w", err)
			}
		}
		return nil
	})
}

func (s *ActivityStore) ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) ([]outbox.Claim[ActivityDelivery], error) {
	var claims []outbox.Claim[ActivityDelivery]
	err := database.WithRetryablePostgresTx(ctx, s.DB, nil, func(tx *sql.Tx) error {
		q := lookoutdb.New(tx)
		rows, err := q.ClaimOperatorActivityCandidates(ctx, lookoutdb.ClaimOperatorActivityCandidatesParams{
			LeaseMilliseconds: lease.Milliseconds(), BatchSize: int32(batchSize),
		})
		if err != nil {
			return fmt.Errorf("select operator activity outbox: %w", err)
		}
		claims = make([]outbox.Claim[ActivityDelivery], 0, len(rows))
		for _, row := range rows {
			token := uuid.NewString()
			affected, err := q.LeaseOperatorActivity(ctx, lookoutdb.LeaseOperatorActivityParams{
				LeaseToken: token, ID: row.ID, TenantID: row.TenantID,
			})
			if err != nil {
				return fmt.Errorf("lease operator activity outbox: %w", err)
			}
			if affected != 1 {
				return errors.New("lease operator activity outbox: selected row disappeared")
			}
			claims = append(claims, outbox.Claim[ActivityDelivery]{
				ID: claimID(row.TenantID.String, row.ID), Attempts: int(row.Attempts), LeaseToken: token,
				Payload: ActivityDelivery{
					OutboxID: row.ID, TenantID: row.TenantID.String, SourceEventID: row.SourceEventID,
					EventType: row.EventType, Channel: row.Channel, Payload: row.Payload,
				},
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *ActivityStore) MarkCompleted(context.Context, string) error {
	return errLeaseTokenRequired
}

func (s *ActivityStore) RecordFailure(context.Context, string, int, []string, error, time.Duration) error {
	return errLeaseTokenRequired
}

func (s *ActivityStore) MarkCompletedToken(ctx context.Context, id, leaseToken string) error {
	tenantID, outboxID, err := parseClaimID(id)
	if err != nil {
		return err
	}
	_, err = lookoutdb.New(s.DB).CompleteOperatorActivity(ctx, lookoutdb.CompleteOperatorActivityParams{
		ID: outboxID, TenantID: nullTenant(tenantID), LeaseToken: leaseToken,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return errLeaseLost
	}
	if err != nil {
		return fmt.Errorf("complete operator activity: %w", err)
	}
	return nil
}

func (s *ActivityStore) RecordFailureToken(ctx context.Context, id string, _ int, _ []string, cause error, backoff time.Duration, leaseToken string) error {
	tenantID, outboxID, err := parseClaimID(id)
	if err != nil {
		return err
	}
	message := "delivery failed"
	if cause != nil {
		message = cause.Error()
	}
	row, err := lookoutdb.New(s.DB).FailOperatorActivity(ctx, lookoutdb.FailOperatorActivityParams{
		BackoffMilliseconds: backoff.Milliseconds(),
		LastError:           sql.NullString{String: message, Valid: true},
		MaxAttempts:         maxDeliveryAttempts,
		ID:                  outboxID,
		TenantID:            nullTenant(tenantID),
		LeaseToken:          leaseToken,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return errLeaseLost
	}
	if err != nil {
		return fmt.Errorf("record operator activity failure: %w", err)
	}
	if row.Failed {
		s.Metrics.ObserveDelivery(row.Channel, "failed")
		if s.Logger != nil {
			s.Logger.WithFields(logging.Fields{
				"channel": row.Channel, "event_type": row.EventType,
				"attempts": row.Attempts, "last_error": message,
			}).Error("Lookout operator activity delivery failed permanently")
		}
	}
	return nil
}

// ActivityDispatcher renders and sends one Slack or Discord activity row.
type ActivityDispatcher struct {
	Webhook *Dispatcher
}

var _ outbox.Dispatcher[ActivityDelivery] = (*ActivityDispatcher)(nil)

func (d *ActivityDispatcher) Dispatch(ctx context.Context, delivery ActivityDelivery) ([]string, error) {
	if d.Webhook == nil {
		return []string{delivery.Channel}, errors.New("activity webhook dispatcher is not configured")
	}
	if d.Webhook.Channels != nil && !d.Webhook.Channels.Enabled(delivery.Channel) {
		d.Webhook.Metrics.ObserveDelivery(delivery.Channel, "skipped")
		return nil, nil
	}
	var payload ActivityPayload
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		return []string{delivery.Channel}, fmt.Errorf("decode operator activity payload: %w", err)
	}
	message := buildActivityMessage(payload)
	var body []byte
	var err error
	switch delivery.Channel {
	case incidents.ChannelSlack:
		body, err = slackBody(message)
	case incidents.ChannelDiscord:
		body, err = discordBody(message)
	default:
		err = fmt.Errorf("unsupported operator activity channel %q", delivery.Channel)
	}
	if err == nil {
		err = d.Webhook.postJSON(ctx, delivery.Channel, d.Webhook.Settings.current().webhookURL(delivery.Channel), body)
	}
	if err != nil {
		d.Webhook.Metrics.ObserveDelivery(delivery.Channel, "error")
		return []string{delivery.Channel}, err
	}
	d.Webhook.Metrics.ObserveDelivery(delivery.Channel, "delivered")
	return nil, nil
}

func buildActivityMessage(payload ActivityPayload) message {
	color := 0x4A90E2
	if payload.Category == "growth" {
		color = 0x8B5CF6
	}
	if payload.Category == "revenue" {
		color = colorResolved
	}
	if strings.Contains(strings.ToLower(payload.Headline), "failed") || strings.Contains(strings.ToLower(payload.Headline), "canceled") {
		color = colorCritical
	}
	fields := []messageField{{Name: "Category", Value: strings.ToUpper(payload.Category)}}
	for _, field := range payload.Fields {
		fields = append(fields, messageField(field))
	}
	return message{
		Headline: payload.Headline, Summary: payload.Summary, Fields: fields,
		Color: color, Timestamp: payload.Timestamp,
	}
}
