package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/fakesession"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Every sweep asks the election before it runs. A lost lock warns once as
// such, and a check that keeps failing warns once for the run, so a database
// outage does not log on every sweep tick.
func TestMediaAuthoritySweepElectionWarnsOncePerFailedRun(t *testing.T) {
	db, sessions := fakesession.New(func(session int, query string) (bool, error) {
		if session == 1 {
			if strings.Contains(query, "pg_try_advisory_lock") {
				return true, nil
			}
			return false, fakesession.ExpiredSessionError()
		}
		return false, errors.New("db down")
	})
	defer db.Close()
	logger, hook := logtest.NewNullLogger()
	s := &CommodoreServer{db: db, logger: logger}
	ctx := context.Background()

	if !s.leadsMediaAuthoritySweeps(ctx) {
		t.Fatal("the first sweep did not take the free election")
	}
	for range 4 {
		if s.leadsMediaAuthoritySweeps(ctx) {
			t.Fatal("a replica whose lock expired still runs the sweeps")
		}
	}
	if !sessions.Closed(1) {
		t.Fatal("the expired session went back to the pool")
	}
	var warnings []string
	for _, entry := range hook.AllEntries() {
		if entry.Level <= logrus.WarnLevel {
			warnings = append(warnings, entry.Message)
		}
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "lost") {
		t.Fatalf("warnings = %q; want the lost lock, then one for the run of failed checks", warnings)
	}
}
