package cmd

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/health"
)

const (
	// doctorCheckWorkers bounds how many doctor checks run at once.
	doctorCheckWorkers = 8
	// doctorSSHPerHost bounds concurrent ssh/scp processes per host, well under OpenSSH's default MaxStartups of 10
	// unauthenticated connections, past which sshd drops new handshakes.
	doctorSSHPerHost = 4
	// doctorInterruptGrace is how long an interrupted doctor waits for running checks to observe the cancellation.
	doctorInterruptGrace = 2 * time.Second
)

// doctorOutcome is what one check reports. A check with a result or a miss counts toward the healthy total; a note is
// printed as a warning without counting; an empty outcome prints nothing.
type doctorOutcome struct {
	result *health.CheckResult
	miss   string
	note   string
	passed bool
	steps  []ux.NextStep
}

func (o doctorOutcome) counted() bool { return o.result != nil || o.miss != "" }

// doctorCheck is one independent probe. progress, when set, is printed if the check is still running when its section
// is reached, so a slow check is visible instead of silent.
type doctorCheck struct {
	name     string
	progress string
	run      func(ctx context.Context) doctorOutcome
}

type doctorSection struct {
	title  string
	checks []doctorCheck
}

type doctorJob struct {
	section, index int
	check          doctorCheck
}

type doctorDone struct {
	index   int
	outcome doctorOutcome
}

// runDoctorSections runs every check on at most workers goroutines, dispatching in section order, and renders each
// section once the sections before it are rendered: its finished checks in completion order, a progress line for
// every slow check still running, then the rest as they finish. When ctx ends it stops rendering, so checks cut short
// by the cancellation are never shown as failures, prints that it was interrupted, and reports interrupted. Outcomes are returned in check order;
// an unrendered check has an empty outcome.
func runDoctorSections(ctx context.Context, out io.Writer, sections []doctorSection, workers int, render func(name string, o doctorOutcome)) ([][]doctorOutcome, bool) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make([]chan doctorDone, len(sections))
	for s := range sections {
		done[s] = make(chan doctorDone, len(sections[s].checks))
	}
	jobs := make(chan doctorJob)
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Go(func() {
			for job := range jobs {
				if runCtx.Err() != nil {
					continue
				}
				done[job.section] <- doctorDone{index: job.index, outcome: job.check.run(runCtx)}
			}
		})
	}
	go func() {
		defer close(jobs)
		for s, section := range sections {
			for i, check := range section.checks {
				select {
				case jobs <- doctorJob{section: s, index: i, check: check}:
				case <-runCtx.Done():
					return
				}
			}
		}
	}()

	outcomes := make([][]doctorOutcome, len(sections))
	interrupted := false
render:
	for s, section := range sections {
		outcomes[s] = make([]doctorOutcome, len(section.checks))
		if ctx.Err() != nil {
			interrupted = true
			break
		}
		if section.title != "" {
			fmt.Fprintf(out, "%s:\n\n", section.title)
		}
		finished := make([]bool, len(section.checks))
		remaining := len(section.checks)
		accept := func(d doctorDone) bool {
			if ctx.Err() != nil {
				return false
			}
			finished[d.index] = true
			remaining--
			outcomes[s][d.index] = d.outcome
			render(section.checks[d.index].name, d.outcome)
			return true
		}
	drain:
		for remaining > 0 {
			select {
			case d := <-done[s]:
				if !accept(d) {
					interrupted = true
					break render
				}
			default:
				break drain
			}
		}
		for i, check := range section.checks {
			if !finished[i] && check.progress != "" {
				fmt.Fprintf(out, "  … %s: %s\n", check.name, check.progress)
			}
		}
		for remaining > 0 {
			select {
			case d := <-done[s]:
				if !accept(d) {
					interrupted = true
					break render
				}
			case <-ctx.Done():
				interrupted = true
				break render
			}
		}
		fmt.Fprintln(out)
	}

	if interrupted {
		ux.Warn(out, "interrupted; checks still running were abandoned and are not reported")
	}
	cancel()
	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()
	if interrupted {
		select {
		case <-stopped:
		case <-time.After(doctorInterruptGrace):
		}
	} else {
		<-stopped
	}
	return outcomes, interrupted
}
