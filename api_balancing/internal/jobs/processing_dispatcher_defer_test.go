package jobs

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestDeferUnroutableSpreadsTheDeferral proves the deferral of an unroutable
// job spans unroutableJobDeferral ±20%, so replicas that found the same jobs
// unroutable together do not re-claim them together.
func TestDeferUnroutableSpreadsTheDeferral(t *testing.T) {
	for _, tc := range []struct {
		random float64
		want   float64
	}{
		{random: 0, want: 12},
		{random: 0.5, want: 15},
		{random: 0.999999, want: 17.99999},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectExec(deferUnroutablePattern).
			WithArgs(approxSeconds(tc.want), "job-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		d := NewProcessingDispatcher(ProcessingDispatcherConfig{DB: db, Logger: logging.NewLogger()})
		d.random = func() float64 { return tc.random }
		d.deferUnroutable(context.Background(), "job-1")
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("random %v: %v", tc.random, err)
		}
		_ = db.Close()
	}
}

type approxSeconds float64

func (a approxSeconds) Match(v driver.Value) bool {
	seconds, ok := v.(float64)
	return ok && seconds > float64(a)-0.001 && seconds < float64(a)+0.001
}
