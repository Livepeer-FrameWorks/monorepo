package telemetryspool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurableReplayIdentityAndCapacity(t *testing.T) {
	directory := t.TempDir()
	q, err := Open(Config{Directory: directory, MaxRecords: 1, MaxBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Enqueue("stable-id", []byte("same-event")); err != nil {
		t.Fatal(err)
	}
	if err = q.Enqueue("stable-id", []byte("same-event")); err != nil {
		t.Fatal("exact retry rejected")
	}
	if err = q.Enqueue("stable-id", []byte("other-event")); err == nil {
		t.Fatal("ID reuse with changed payload accepted")
	}
	if err = q.Enqueue("second-id", []byte("other-event")); !errors.Is(err, ErrFull) {
		t.Fatalf("bound not enforced: %v", err)
	}
	count, _, dropped := q.Stats()
	if count != 1 || dropped != 1 {
		t.Fatalf("incorrect queue statistics: %d %d", count, dropped)
	}
	reopened, err := Open(Config{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.Pending(10)
	if err != nil || len(rows) != 1 || string(rows[0].Payload) != "same-event" {
		t.Fatalf("restart lost stable payload: %+v %v", rows, err)
	}
	if err := reopened.Ack(rows[0]); err != nil {
		t.Fatal(err)
	}
	count, _, _ = reopened.Stats()
	if count != 0 {
		t.Fatal("acknowledged record retained")
	}
}
func TestFailedDeliveryIsNeverAcknowledged(t *testing.T) {
	q, err := Open(Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue("event", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q.Run(ctx, func(context.Context, []byte) error { cancel(); return errors.New("not committed") }, nil)
	count, _, _ := q.Stats()
	if count != 1 {
		t.Fatal("failed delivery removed durable event")
	}
}
func TestExpiredSpoolRecordsAreCountedAsLost(t *testing.T) {
	directory := t.TempDir()
	q, err := Open(Config{Directory: directory, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Enqueue("old", []byte("expired")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err = os.Chtimes(filepath.Join(directory, filename("old")), old, old); err != nil {
		t.Fatal(err)
	}
	q, err = Open(Config{Directory: directory, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	count, _, dropped := q.Stats()
	if count != 0 || dropped != 1 {
		t.Fatalf("expired records remain or loss invisible: %d %d", count, dropped)
	}
}
