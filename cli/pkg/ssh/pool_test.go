package ssh

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGetHealthyClientReconnectsAfterPingFailure(t *testing.T) {
	t.Parallel()

	pool := NewPool(2 * time.Second)

	var created []*Client
	pool.newClient = func(config *ConnectionConfig) (*Client, error) {
		client := &Client{}
		if len(created) == 0 {
			client.pingFunc = func(ctx context.Context) error {
				return errors.New("stale connection")
			}
		} else {
			client.pingFunc = func(ctx context.Context) error {
				return nil
			}
		}
		created = append(created, client)
		return client, nil
	}

	config := &ConnectionConfig{
		Address: "127.0.0.1",
		Port:    22,
		User:    "tester",
	}

	client, err := pool.getHealthyClient(context.Background(), config)
	if err != nil {
		t.Fatalf("expected healthy client, got error: %v", err)
	}

	if len(created) != 2 {
		t.Fatalf("expected 2 clients to be created, got %d", len(created))
	}

	if client != created[1] {
		t.Fatalf("expected second client to be returned after reconnect")
	}
}
