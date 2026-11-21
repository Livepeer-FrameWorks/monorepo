package grpc

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestProcessingOverrideReadFailuresAreVisible(t *testing.T) {
	for _, scope := range []string{"stream", "tenant"} {
		for _, missing := range []bool{false, true} {
			t.Run(scope+"/missing="+fmt.Sprint(missing), func(t *testing.T) {
				server, mock, done := newMockServer(t)
				defer done()
				hook := logtest.NewLocal(server.logger)
				lookupErr := sql.ErrConnDone
				if missing {
					lookupErr = sql.ErrNoRows
				}
				var value string
				var readErr error
				if scope == "stream" {
					mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs("stream-one", "tenant-one").WillReturnError(lookupErr)
					value, readErr = server.getStreamProcessingOverride(t.Context(), "tenant-one", "stream-one", "live")
				} else {
					mock.ExpectQuery("GetTenantProcessingOverrides").WithArgs("tenant-one").WillReturnError(lookupErr)
					value, readErr = server.getTenantProcessingOverride(t.Context(), "tenant-one", "live")
				}
				if !missing && readErr == nil {
					t.Fatal("database error was not propagated")
				}
				if missing && readErr != nil {
					t.Fatal(readErr)
				}
				if value != "" {
					t.Fatalf("failed lookup returned an override: %s", value)
				}
				if missing {
					if len(hook.AllEntries()) != 0 {
						t.Fatal("missing optional override logged as an error")
					}
				} else {
					entry := hook.LastEntry()
					if entry == nil || entry.Level != logrus.WarnLevel || entry.Data["tenant_id"] != "tenant-one" || entry.Data["error"] == nil {
						t.Fatalf("override database failure was silent: %+v", entry)
					}
				}
			})
		}
	}
}
