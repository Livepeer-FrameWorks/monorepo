package cmd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/provisioner"
)

type fakeYugabyteCompactor struct {
	targets   []provisioner.YugabyteCompactionTarget
	sizes     map[string]int64
	after     map[string]int64
	compacted []string
	timeouts  []time.Duration
	fail      map[string]error
	onCompact func(name string)
}

func (f *fakeYugabyteCompactor) Resolve(context.Context, []string) ([]provisioner.YugabyteCompactionTarget, error) {
	return slices.Clone(f.targets), nil
}

func (f *fakeYugabyteCompactor) Measure(_ context.Context, targets []provisioner.YugabyteCompactionTarget) (provisioner.YugabyteCompactionMeasurement, error) {
	for i := range targets {
		targets[i].SSTBytes = f.sizes[targets[i].Name]
		if slices.Contains(f.compacted, targets[i].Name) {
			targets[i].SSTBytes = f.after[targets[i].Name]
		}
	}
	return provisioner.YugabyteCompactionMeasurement{Unreachable: []string{"yb-3: ssh: connection refused"}}, nil
}

func (f *fakeYugabyteCompactor) Compact(_ context.Context, target provisioner.YugabyteCompactionTarget, timeout time.Duration) (string, error) {
	if f.onCompact != nil {
		f.onCompact(target.Name)
	}
	f.compacted = append(f.compacted, target.Name)
	f.timeouts = append(f.timeouts, timeout)
	return "Compacted tables.", f.fail[target.Name]
}

func newFakeCompactor() *fakeYugabyteCompactor {
	return &fakeYugabyteCompactor{
		targets: []provisioner.YugabyteCompactionTarget{{Name: "public.small"}, {Name: "public.big"}, {Name: "public.mid"}},
		sizes:   map[string]int64{"public.small": 1 << 10, "public.big": 3 << 30, "public.mid": 5 << 20},
		after:   map[string]int64{"public.small": 512, "public.big": 1 << 30, "public.mid": 1 << 20},
		fail:    map[string]error{},
	}
}

func TestCompactYugabyteDatabaseDryRunListsWithoutCompacting(t *testing.T) {
	compactor := newFakeCompactor()
	var out bytes.Buffer
	err := compactYugabyteDatabase(context.Background(), &out, compactor, yugabyteCompactOptions{Database: "app", Timeout: time.Hour, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(compactor.compacted) != 0 {
		t.Fatalf("dry run compacted %v", compactor.compacted)
	}
	text := out.String()
	big, mid := strings.Index(text, "public.big"), strings.Index(text, "public.mid")
	if big < 0 || mid < 0 || big > mid || !strings.Contains(text, "3.0 GiB") || !strings.Contains(text, "not measured: yb-3") || !strings.Contains(text, "nothing was compacted") {
		t.Fatalf("dry run output:\n%s", text)
	}
}

func TestCompactYugabyteDatabaseRunsOneTableAtATimeLargestFirst(t *testing.T) {
	compactor := newFakeCompactor()
	var out bytes.Buffer
	if err := compactYugabyteDatabase(context.Background(), &out, compactor, yugabyteCompactOptions{Database: "app", Timeout: 2 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"public.big", "public.mid", "public.small"}; !slices.Equal(compactor.compacted, want) {
		t.Fatalf("compaction order = %v, want %v", compactor.compacted, want)
	}
	if !slices.Equal(compactor.timeouts, []time.Duration{2 * time.Hour, 2 * time.Hour, 2 * time.Hour}) {
		t.Fatalf("timeouts = %v", compactor.timeouts)
	}
	text := out.String()
	for _, want := range []string{
		"expect raised disk IO",
		"[1/3] public.big: compacting",
		"[1/3] public.big: done in 0s, SST files 3.0 GiB -> 1.0 GiB",
		"[3/3] public.small: done in",
		"compacted 3 DocDB table(s) in app",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestCompactYugabyteDatabaseStopsAtTheFirstFailure(t *testing.T) {
	compactor := newFakeCompactor()
	compactor.fail["public.mid"] = errors.New("yb-1: exit 1: Timed out waiting for FlushTables")
	var out bytes.Buffer
	err := compactYugabyteDatabase(context.Background(), &out, compactor, yugabyteCompactOptions{Database: "app", Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "public.mid") {
		t.Fatalf("err = %v, want the failed table named", err)
	}
	if !slices.Equal(compactor.compacted, []string{"public.big", "public.mid"}) {
		t.Fatalf("compacted = %v, want no table after the failure", compactor.compacted)
	}
	if text := out.String(); !strings.Contains(text, "keeps running on the tservers") || !strings.Contains(text, "Not started: public.small") {
		t.Fatalf("output:\n%s", text)
	}
}

func TestCompactYugabyteDatabaseReportsInterruption(t *testing.T) {
	compactor := newFakeCompactor()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	compactor.onCompact = func(name string) {
		if name == "public.big" {
			cancel()
			compactor.fail[name] = context.Canceled
		}
	}
	var out bytes.Buffer
	err := compactYugabyteDatabase(ctx, &out, compactor, yugabyteCompactOptions{Database: "app", Timeout: time.Minute})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	text := out.String()
	if !strings.Contains(text, "[1/3] public.big: interrupted after") || !strings.Contains(text, "Not started: public.mid, public.small") {
		t.Fatalf("output:\n%s", text)
	}
}

func TestYugabyteCompactFlagsValidate(t *testing.T) {
	for name, args := range map[string][]string{
		"missing database": {},
		"empty table":      {"--database", "app", "--table", " "},
		"zero timeout":     {"--database", "app", "--timeout", "0s"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newYugabyteCompactCmd()
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			if _, err := yugabyteCompactOptionsFromFlags(cmd); err == nil {
				t.Fatalf("flags %v accepted", args)
			}
		})
	}
	cmd := newYugabyteCompactCmd()
	if err := cmd.ParseFlags([]string{"--database", "app", "--table", "public.a", "--table", "b", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	opts, err := yugabyteCompactOptionsFromFlags(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Database != "app" || !slices.Equal(opts.Tables, []string{"public.a", "b"}) || !opts.DryRun || opts.Timeout != time.Hour {
		t.Fatalf("opts = %+v", opts)
	}
}
