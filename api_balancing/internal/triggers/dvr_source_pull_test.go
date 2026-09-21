package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/appconfig"
	"frameworks/api_balancing/internal/control"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestLocalDVRSourcePullBindsOriginAndDestination(t *testing.T) {
	useFoghornConfig(t, &appconfig.Foghorn{BalancerCapabilitySecret: "dvr-source-test-secret"})
	sm := resetStateTrigHandlers(t)
	sm.SetNodeConnectionInfo(t.Context(), "origin", "http://origin:8080", "", "media", nil)
	sm.SetNodeConnectionInfo(t.Context(), "viewer-edge", "http://viewer:8080", "", "media", nil)
	sm.SetNodeInfo("origin", "http://origin:8080", true, nil, nil, "", "", map[string]any{"DTSC": "dtsc://HOST:4200/$"})
	previousDB, previousRegistry := control.GetDB(), control.StreamRegistryInstance
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	control.SetDB(database)
	registry := control.NewStreamRegistry(nil, "cell", time.Minute)
	control.SetStreamRegistry(registry)
	t.Cleanup(func() {
		control.SetDB(previousDB)
		control.SetStreamRegistry(previousRegistry)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		database.Close()
	})
	expectOwner := func(owner bool) {
		mock.ExpectQuery(`SELECT EXISTS`).WithArgs("tenant", "recording", "origin").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(owner))
	}
	p := newTestProcessor(t)
	recording := &control.DVRArtifactDispatch{TenantID: "tenant", InternalName: "recording", RecordingNode: "origin"}
	expectOwner(true)
	url := p.localDVRSourcePull(t.Context(), "dvr+recording", recording, "viewer-edge")
	credential := control.SourcePullCredential(url)
	if credential == "" {
		t.Fatal("same-cell DVR source has no scoped credential")
	}
	expectOwner(true)
	if _, ok := registry.AcceptedOutboundPull(t.Context(), "dvr+recording", "origin", credential, time.Now()); !ok {
		t.Fatal("same-cell credential not admitted at recording origin")
	}
	if _, ok := registry.AcceptedOutboundPull(t.Context(), "dvr+recording", "viewer-edge", credential, time.Now()); ok {
		t.Fatal("source credential admitted on wrong edge")
	}
	expectOwner(false)
	if got := p.localDVRSourcePull(t.Context(), "dvr+recording", recording, "viewer-edge"); got != "" {
		t.Fatal("stopped recording returned a source URL")
	}
}
