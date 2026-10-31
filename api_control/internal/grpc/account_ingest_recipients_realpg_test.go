//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"frameworks/api_control/internal/database/commodoredb"

	"github.com/google/uuid"
)

func testAccountIngestRecipientIsolation(t *testing.T, db *sql.DB) {
	t.Helper()
	t.Run("ingest recipients retain historical cells and tenant isolation", func(t *testing.T) {
		ctx := context.Background()
		tenant, otherTenant := uuid.NewString(), uuid.NewString()
		selected, bystander, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString()
		for _, item := range []struct{ tenant, stream, cell string }{
			{tenant, selected, "selected"}, {tenant, bystander, "bystander"}, {otherTenant, foreign, "foreign"},
		} {
			owner := uuid.NewString()
			activeCell := item.cell + "-active"
			if item.stream == bystander {
				activeCell = item.cell + "-previous"
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,$3,true)`, owner, item.tenant, owner+"@recipient.test"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `
				INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title,active_ingest_cluster_id)
				VALUES($1::text::uuid,$2,$3,$1::text,$1::text,$1::text,'Recipient isolation',$4)`,
				item.stream, item.tenant, owner, activeCell); err != nil {
				t.Fatal(err)
			}
			if item.stream == bystander {
				if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL WHERE tenant_id=$1 AND id=$2`, item.tenant, item.stream); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, `
				INSERT INTO commodore.media_authority_targets(authority_kind,authority_id,cell_id,highest_targeted_version)
				VALUES('media_object',$1,$2,1)`, "live_stream:"+item.stream, item.cell+"-historical"); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{tenant, otherTenant} {
			if _, err := db.ExecContext(ctx, `
				INSERT INTO commodore.media_authority_targets(authority_kind,authority_id,cell_id,highest_targeted_version)
				VALUES('tenant',$1,$2,1)`, id, "tenant-"+id); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			name, stream string
			want         []string
		}{
			{"selected stream", selected, []string{"selected-active", "selected-historical"}},
			{"disconnected stream", bystander, []string{"bystander-historical", "bystander-previous"}},
			{"closed tenant", "", []string{"bystander-historical", "bystander-previous", "selected-active", "selected-historical", "tenant-" + tenant}},
			{"another tenant's stream", foreign, []string{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := commodoredb.New(db).ListAccountIngestCells(ctx, commodoredb.ListAccountIngestCellsParams{TenantID: tenant, StreamID: tc.stream})
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("recipients=%v, want %v", got, tc.want)
				}
			})
		}
	})
}
