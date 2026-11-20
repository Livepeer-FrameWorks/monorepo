package mediaauthority

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestManagedSourceListCancellationPreservesCallerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		err := sqlmock.QueryMatcherRegexp.Match(expected, actual)
		if err == nil && strings.Contains(expected, "ListLocalManagedStreamAuthorities") {
			cancel()
		}
		return err
	})
	store, mock, closeDB := newFixtureStoreWithMatcher(t, "cell-a", matcher)
	defer closeDB()
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_cancelled_managed_sets_total", Help: "test"}, []string{"outcome"})
	store.SetManagedStreamSetMetric(metric)
	mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnError(sqlmock.ErrCancelled)
	set, err := store.ManagedStreams(ctx, "cluster")
	if !errors.Is(err, context.Canceled) || set.Complete {
		t.Fatalf("cancelled list lost caller context or returned a complete set: complete=%v err=%v", set.Complete, err)
	}
	if !errors.Is(err, sqlmock.ErrCancelled) {
		t.Fatalf("cancelled driver failure lost original diagnostics: %v", err)
	}
	if got := testutil.ToFloat64(metric.WithLabelValues("cancelled")); got != 1 {
		t.Fatalf("cancelled sets=%v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
