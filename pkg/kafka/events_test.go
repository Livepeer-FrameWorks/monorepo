package kafka

import (
	dbpkg "frameworks/pkg/database"
	"testing"
	"time"
)

func TestAnalyticsEventHandler_HandlesEvent(t *testing.T) {
	handled := false
	handler := NewAnalyticsEventHandler(nil, func(_ dbpkg.PostgresConn, evt AnalyticsEvent) error {
		handled = true
		if evt.EventType != "stream-lifecycle" {
			t.Fatalf("wrong type")
		}
		if evt.InternalName == nil || *evt.InternalName != "foo" {
			t.Fatalf("missing internal_name")
		}
		return nil
	}, nil)

	name := "foo"
	e := AnalyticsEvent{
		EventID:      "1",
		EventType:    "stream-lifecycle",
		Source:       "test",
		Timestamp:    time.Now(),
		InternalName: &name,
	}
	if err := handler.HandleEvent(e); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !handled {
		t.Fatalf("handler not called")
	}
}
