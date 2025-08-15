package kafka

import (
	dbpkg "frameworks/pkg/database"
	"testing"
	"time"
)

func TestAnalyticsEventHandler_ConvertsEvent(t *testing.T) {
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

	e := Event{ID: "1", Type: "stream-lifecycle", Source: "test", Timestamp: time.Now(), Data: map[string]interface{}{"internal_name": "foo"}}
	if err := handler.HandleEvent(e); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !handled {
		t.Fatalf("handler not called")
	}
}
