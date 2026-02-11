package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	goredis "github.com/redis/go-redis/v9"
)

type TypedPubSub[T any] struct {
	client goredis.UniversalClient
}

func NewTypedPubSub[T any](client goredis.UniversalClient) *TypedPubSub[T] {
	return &TypedPubSub[T]{client: client}
}

func (p *TypedPubSub[T]) Publish(ctx context.Context, channel string, msg T) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal pubsub payload: %w", err)
	}

	if err := p.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("publish to redis: %w", err)
	}

	return nil
}

func (p *TypedPubSub[T]) Subscribe(ctx context.Context, channel string, handler func(T)) error {
	sub := p.client.Subscribe(ctx, channel)
	defer sub.Close()

	if _, err := sub.Receive(ctx); err != nil {
		return fmt.Errorf("subscribe to redis: %w", err)
	}

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}

			var payload T
			if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
				log.Printf("[redis-pubsub] unmarshal error on channel %s: %v", channel, err)
				continue
			}
			handler(payload)
		}
	}
}
