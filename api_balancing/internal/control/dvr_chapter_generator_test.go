package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestCurrentChapterBounds pins the chapter-windowing invariant the DVR
// finalization sweep depends on: window_sized_chapters anchors its buckets at
// the artifact's started_at, while fixed_interval anchors at unix epoch 0 (UTC).
// The returned range is half-open [startMs, endMs). Inputs that cannot produce a
// sensible bounded chapter return ok=false so the caller skips the boundary.
func TestCurrentChapterBounds(t *testing.T) {
	cases := []struct {
		name         string
		mode         string
		intervalSecs int32
		startedAtMs  int64
		nowMs        int64
		wantStart    int64
		wantEnd      int64
		wantOK       bool
	}{
		{
			name:         "window_sized exact boundary",
			mode:         ChapterModeWindowSized,
			intervalSecs: 60,
			startedAtMs:  1_000_000,
			nowMs:        1_000_000, // offset 0 → bucket 0
			wantStart:    1_000_000,
			wantEnd:      1_060_000,
			wantOK:       true,
		},
		{
			name:         "window_sized mid bucket anchors at startedAt",
			mode:         ChapterModeWindowSized,
			intervalSecs: 60,
			startedAtMs:  1_000_000,
			nowMs:        1_000_000 + 90_000, // 1.5 intervals in → bucket 1
			wantStart:    1_000_000 + 60_000,
			wantEnd:      1_000_000 + 120_000,
			wantOK:       true,
		},
		{
			name:         "window_sized end is exclusive (next interval start belongs to next bucket)",
			mode:         ChapterModeWindowSized,
			intervalSecs: 60,
			startedAtMs:  1_000_000,
			nowMs:        1_060_000, // exactly one interval → bucket 1, not still in bucket 0
			wantStart:    1_060_000,
			wantEnd:      1_120_000,
			wantOK:       true,
		},
		{
			name:         "window_sized now before start is invalid",
			mode:         ChapterModeWindowSized,
			intervalSecs: 60,
			startedAtMs:  1_000_000,
			nowMs:        999_999,
			wantOK:       false,
		},
		{
			name:         "window_sized zero interval is invalid",
			mode:         ChapterModeWindowSized,
			intervalSecs: 0,
			startedAtMs:  1_000_000,
			nowMs:        1_000_000,
			wantOK:       false,
		},
		{
			name:         "window_sized zero startedAt is invalid",
			mode:         ChapterModeWindowSized,
			intervalSecs: 60,
			startedAtMs:  0,
			nowMs:        1_000_000,
			wantOK:       false,
		},
		{
			name:         "fixed_interval anchors at epoch 0",
			mode:         ChapterModeFixedInterval,
			intervalSecs: 60,
			startedAtMs:  1_234_567, // ignored for fixed_interval
			nowMs:        130_000,   // bucket 2 (120000..180000)
			wantStart:    120_000,
			wantEnd:      180_000,
			wantOK:       true,
		},
		{
			name:         "fixed_interval exact boundary",
			mode:         ChapterModeFixedInterval,
			intervalSecs: 60,
			nowMs:        120_000,
			wantStart:    120_000,
			wantEnd:      180_000,
			wantOK:       true,
		},
		{
			name:         "fixed_interval zero interval is invalid",
			mode:         ChapterModeFixedInterval,
			intervalSecs: 0,
			nowMs:        120_000,
			wantOK:       false,
		},
		{
			name:         "unknown mode is invalid",
			mode:         "rolling",
			intervalSecs: 60,
			startedAtMs:  1_000_000,
			nowMs:        1_000_000,
			wantOK:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok := CurrentChapterBounds(tc.mode, tc.intervalSecs, tc.startedAtMs, tc.nowMs)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				if start != 0 || end != 0 {
					t.Fatalf("invalid input should return zero bounds, got [%d,%d)", start, end)
				}
				return
			}
			if start != tc.wantStart || end != tc.wantEnd {
				t.Fatalf("bounds = [%d,%d), want [%d,%d)", start, end, tc.wantStart, tc.wantEnd)
			}
			if end <= start {
				t.Fatalf("range must be non-empty half-open, got [%d,%d)", start, end)
			}
			// nowMs must fall inside the returned half-open range.
			if tc.nowMs < start || tc.nowMs >= end {
				t.Fatalf("nowMs %d not contained in [%d,%d)", tc.nowMs, start, end)
			}
		})
	}
}

// TestEffectiveChapterInterval pins the interval-resolution precedence: an
// explicit positive interval always wins; otherwise only window_sized_chapters
// falls back to the DVR window length, and every other mode resolves to 0
// (meaning "no chaptering"). EffectiveIntervalSeconds is the policy-struct
// delegate and must agree.
func TestEffectiveChapterInterval(t *testing.T) {
	cases := []struct {
		name         string
		mode         string
		intervalSecs int32
		windowSecs   int32
		want         int32
	}{
		{"explicit interval wins over window", ChapterModeWindowSized, 30, 600, 30},
		{"explicit interval wins for fixed", ChapterModeFixedInterval, 30, 600, 30},
		{"window_sized falls back to window", ChapterModeWindowSized, 0, 600, 600},
		{"fixed with no interval is zero", ChapterModeFixedInterval, 0, 600, 0},
		{"unknown mode with no interval is zero", "rolling", 0, 600, 0},
		{"window_sized with no window is zero", ChapterModeWindowSized, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveChapterInterval(tc.mode, tc.intervalSecs, tc.windowSecs); got != tc.want {
				t.Fatalf("EffectiveChapterInterval = %d, want %d", got, tc.want)
			}
			p := DVRChapterPolicy{Mode: tc.mode, IntervalSeconds: tc.intervalSecs, WindowSeconds: tc.windowSecs}
			if got := p.EffectiveIntervalSeconds(); got != tc.want {
				t.Fatalf("EffectiveIntervalSeconds = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDVRChapterMaxRangeMs pins the max-range lookup contract: the tenant filter
// is only appended when a tenant is supplied; a valid positive window converts
// seconds→milliseconds; a NULL/zero window falls back to a 1h cap; and a nil
// querier short-circuits to ErrConnDone.
func TestDVRChapterMaxRangeMs(t *testing.T) {
	const oneHourMs = int64(time.Hour / time.Millisecond)

	t.Run("nil querier returns ErrConnDone", func(t *testing.T) {
		got, err := DVRChapterMaxRangeMs(context.Background(), nil, "art", "tenant")
		if !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("err = %v, want sql.ErrConnDone", err)
		}
		if got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})

	t.Run("valid window converts seconds to ms with tenant filter", func(t *testing.T) {
		dbm, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer dbm.Close()
		mock.ExpectQuery(`SELECT dvr_window_seconds\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1\s+AND artifact_type = 'dvr'\s+AND tenant_id = \$2`).
			WithArgs("art", "tenant").
			WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(int64(120)))

		got, err := DVRChapterMaxRangeMs(context.Background(), dbm, "art", "tenant")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 120_000 {
			t.Fatalf("got %d, want 120000", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty tenant omits tenant filter", func(t *testing.T) {
		dbm, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer dbm.Close()
		// Only artifact_hash is bound; a second arg here would fail expectations.
		mock.ExpectQuery(`SELECT dvr_window_seconds\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1\s+AND artifact_type = 'dvr'`).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(int64(60)))

		got, err := DVRChapterMaxRangeMs(context.Background(), dbm, "art", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 60_000 {
			t.Fatalf("got %d, want 60000", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("null window falls back to one hour", func(t *testing.T) {
		dbm, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer dbm.Close()
		mock.ExpectQuery(`SELECT dvr_window_seconds`).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(nil))

		got, err := DVRChapterMaxRangeMs(context.Background(), dbm, "art", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != oneHourMs {
			t.Fatalf("got %d, want %d (1h)", got, oneHourMs)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("zero window falls back to one hour", func(t *testing.T) {
		dbm, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer dbm.Close()
		mock.ExpectQuery(`SELECT dvr_window_seconds`).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"dvr_window_seconds"}).AddRow(int64(0)))

		got, err := DVRChapterMaxRangeMs(context.Background(), dbm, "art", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != oneHourMs {
			t.Fatalf("got %d, want %d (1h)", got, oneHourMs)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestReadDVRChapterPolicy pins the policy-read gate that decides whether an
// artifact is chapter-eligible. A missing row is a clean (false, nil) — not an
// error. A row that decodes but is structurally incomplete (no mode, no valid
// start, or a non-positive effective interval) returns ok=false so the sweep
// leaves it alone. Only a fully-formed policy returns ok=true.
func TestReadDVRChapterPolicy(t *testing.T) {
	const query = `SELECT COALESCE\(dvr_chapter_mode`

	t.Run("missing artifact is not an error", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(query).
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)

		p, ok, err := ReadDVRChapterPolicy(context.Background(), "missing")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatalf("missing artifact should be ineligible, got ok=true (%+v)", p)
		}
	})

	t.Run("empty mode is ineligible", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(query).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"mode", "interval", "started", "ended", "window"}).
				AddRow("", int32(60), int64(5000), int64(0), int32(600)))

		_, ok, err := ReadDVRChapterPolicy(context.Background(), "art")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatal("empty mode should be ineligible")
		}
	})

	t.Run("non-positive start is ineligible", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(query).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"mode", "interval", "started", "ended", "window"}).
				AddRow(ChapterModeFixedInterval, int32(60), int64(0), int64(0), int32(0)))

		_, ok, err := ReadDVRChapterPolicy(context.Background(), "art")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatal("zero started_at should be ineligible")
		}
	})

	t.Run("zero effective interval is ineligible", func(t *testing.T) {
		mock := setupChapterTest(t)
		// fixed_interval with no interval → EffectiveIntervalSeconds()==0.
		mock.ExpectQuery(query).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"mode", "interval", "started", "ended", "window"}).
				AddRow(ChapterModeFixedInterval, int32(0), int64(5000), int64(0), int32(600)))

		_, ok, err := ReadDVRChapterPolicy(context.Background(), "art")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatal("zero effective interval should be ineligible")
		}
	})

	t.Run("complete policy is eligible", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(query).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"mode", "interval", "started", "ended", "window"}).
				AddRow(ChapterModeFixedInterval, int32(30), int64(5000), int64(65000), int32(600)))

		p, ok, err := ReadDVRChapterPolicy(context.Background(), "art")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("complete policy should be eligible")
		}
		if p.Mode != ChapterModeFixedInterval || p.IntervalSeconds != 30 || p.StartedAtMs != 5000 {
			t.Fatalf("policy decoded incorrectly: %+v", p)
		}
	})
}
