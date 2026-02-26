package social

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"
)

const defaultSocialInterval = 2 * time.Hour

type AgentConfig struct {
	Interval  time.Duration
	MaxPerDay int
	Detector  *Detector
	Composer  *Composer
	Publisher Publisher
	Store     PostStore
	Logger    logging.Logger
}

type Agent struct {
	interval  time.Duration
	maxPerDay int
	detector  *Detector
	composer  *Composer
	publisher Publisher
	store     PostStore
	logger    logging.Logger
}

func NewAgent(cfg AgentConfig) *Agent {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultSocialInterval
	}
	return &Agent{
		interval:  interval,
		maxPerDay: cfg.MaxPerDay,
		detector:  cfg.Detector,
		composer:  cfg.Composer,
		publisher: cfg.Publisher,
		store:     cfg.Store,
		logger:    cfg.Logger,
	}
}

func (a *Agent) Start(ctx context.Context) {
	if a == nil {
		return
	}
	a.runCycle(ctx)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runCycle(ctx)
		}
	}
}

func (a *Agent) runCycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			if a.logger != nil {
				a.logger.WithField("panic", fmt.Sprint(r)).Error("Social posting cycle panic")
			}
		}
	}()
	if a.logger == nil {
		return
	}

	// Enforce daily limit (0 = unlimited)
	if a.maxPerDay > 0 {
		count, err := a.store.CountToday(ctx)
		if err != nil {
			a.logger.WithError(err).Warn("Social agent: failed to count today's posts")
			return
		}
		if count >= a.maxPerDay {
			a.logger.WithField("count", count).Debug("Social agent: daily post limit reached, skipping")
			return
		}
	}

	signals := a.detector.DetectAll(ctx)
	if len(signals) == 0 {
		a.logger.Debug("Social agent: no noteworthy events detected")
		return
	}

	// Take the top-scoring signal
	signal := signals[0]
	a.logger.WithFields(logging.Fields{
		"content_type": string(signal.ContentType),
		"headline":     signal.Headline,
		"score":        signal.Score,
	}).Info("Social agent: noteworthy event detected")

	post, err := a.composer.Compose(ctx, signal)
	if err != nil {
		a.logger.WithError(err).Warn("Social agent: failed to compose tweet")
		return
	}

	saved, err := a.store.Save(ctx, *post)
	if err != nil {
		a.logger.WithError(err).Warn("Social agent: failed to save draft")
		return
	}

	if err := a.publisher.Publish(ctx, saved); err != nil {
		a.logger.WithError(err).Warn("Social agent: failed to publish draft")
		return
	}

	if err := a.store.MarkSent(ctx, saved.ID); err != nil {
		a.logger.WithError(err).Warn("Social agent: failed to mark draft as sent")
	}

	a.logger.WithFields(logging.Fields{
		"post_id":      saved.ID,
		"content_type": string(saved.ContentType),
		"tweet_length": len(saved.TweetText),
	}).Info("Social agent: draft created and sent")
}
