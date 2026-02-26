package social

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
)

const (
	maxTweetLength    = 280
	composeTimeout    = 30 * time.Second
	maxComposeRetries = 1
)

const composerSystemPrompt = `You are the social media voice for FrameWorks, a live streaming platform.
Draft a single tweet (max 280 characters) about the following event.
Be informative and conversational, not hyperbolic. No hashtags unless they genuinely add value.
Do not repeat themes from recent posts listed below.
Respond with ONLY the tweet text, nothing else.`

type ComposerConfig struct {
	LLM    llm.Provider
	Store  PostStore
	Logger logging.Logger
}

type Composer struct {
	llm    llm.Provider
	store  PostStore
	logger logging.Logger
}

func NewComposer(cfg ComposerConfig) *Composer {
	return &Composer{
		llm:    cfg.LLM,
		store:  cfg.Store,
		logger: cfg.Logger,
	}
}

func (c *Composer) Compose(ctx context.Context, signal EventSignal) (*PostRecord, error) {
	if c.llm == nil {
		return nil, errors.New("LLM provider not configured")
	}

	recent, err := c.store.ListRecent(ctx, 20)
	if err != nil {
		c.logger.WithError(err).Warn("Social composer: failed to load recent posts")
	}

	userPrompt := buildComposePrompt(signal, recent)
	tweetText, err := c.generate(ctx, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("compose tweet: %w", err)
	}

	// Retry once if too long
	if len(tweetText) > maxTweetLength {
		c.logger.WithField("length", len(tweetText)).Debug("Social composer: tweet too long, retrying")
		tweetText, err = c.generate(ctx, userPrompt+"\n\nIMPORTANT: Your previous response was too long. Keep it under 280 characters.")
		if err != nil {
			return nil, fmt.Errorf("compose tweet retry: %w", err)
		}
		// Hard truncate at last space before limit
		if len(tweetText) > maxTweetLength {
			tweetText = truncateAtWord(tweetText, maxTweetLength)
		}
	}

	return &PostRecord{
		ContentType:    signal.ContentType,
		TweetText:      tweetText,
		ContextSummary: signal.Headline,
		TriggerData:    signal.Data,
	}, nil
}

func (c *Composer) generate(ctx context.Context, userPrompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, composeTimeout)
	defer cancel()

	stream, err := c.llm.Complete(ctx, []llm.Message{
		{Role: "system", Content: composerSystemPrompt},
		{Role: "user", Content: userPrompt},
	}, nil)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var content strings.Builder
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return "", recvErr
		}
		content.WriteString(chunk.Content)
	}

	return strings.TrimSpace(content.String()), nil
}

func buildComposePrompt(signal EventSignal, recentPosts []PostRecord) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Event type: %s\n", signal.ContentType)
	fmt.Fprintf(&b, "Event: %s\n\n", signal.Headline)

	if len(signal.Data) > 0 {
		b.WriteString("Data:\n")
		for k, v := range signal.Data {
			fmt.Fprintf(&b, "- %s: %v\n", k, v)
		}
		b.WriteString("\n")
	}

	if len(recentPosts) > 0 {
		b.WriteString("Recent tweets (avoid repeating these themes):\n")
		for i, post := range recentPosts {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&b, "- %s\n", post.TweetText)
		}
	}

	return b.String()
}

func truncateAtWord(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	truncated := s[:maxLen]
	lastSpace := strings.LastIndex(truncated, " ")
	if lastSpace > maxLen/2 {
		return truncated[:lastSpace]
	}
	return truncated
}
