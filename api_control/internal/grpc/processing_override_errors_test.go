package grpc

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
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

func TestOverrideOutagePreservesConnectedAudioAndThumbnails(t *testing.T) {
	for _, scope := range []string{"stream", "tenant-with-stream", "tenant"} {
		for _, strict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/strict=%v", scope, strict), func(t *testing.T) {
				server, mock, done := newMockServer(t)
				defer done()
				tier := ladderTier(true)
				tier.Features = &purserpb.BillingFeatures{ProcessingCustomizable: scope != "stream"}
				server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, tier))
				stream := "s1"
				if scope == "tenant" {
					stream = ""
				} else {
					lookup := mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs(stream, testTenantID)
					if scope == "stream" {
						lookup.WillReturnError(sql.ErrConnDone)
					} else {
						lookup.WillReturnError(sql.ErrNoRows)
					}
				}
				if scope != "stream" {
					mock.ExpectQuery("GetTenantProcessingOverrides").WithArgs(testTenantID).WillReturnError(sql.ErrConnDone)
				}
				if stream != "" {
					mock.ExpectQuery("GetStreamProcessingChoices").WithArgs(stream, testTenantID).WillReturnError(sql.ErrConnDone)
				}
				var policy string
				if strict {
					var err error
					policy, err = server.resolveProcessesJSONStrict(t.Context(), testTenantID, stream, "", "live")
					if err == nil {
						t.Fatal("signed authority concealed an override outage")
					}
				} else {
					policy = server.resolveProcessesJSON(t.Context(), testTenantID, stream, "", "live")
				}
				if !strings.Contains(policy, `"Thumbs"`) || !strings.Contains(policy, `"opus"`) || mist.HasTranscodeProcesses(policy) {
					t.Fatalf("override outage discarded entitled audio/thumbnails or enabled unknown ABR: %s", policy)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
