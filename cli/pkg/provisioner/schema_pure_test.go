package provisioner

import "testing"

// hasExecutableSchemaDDL gates whether an embedded baseline schema file is
// materialized and shipped to the node. A file that is only comments / GRANTs /
// no-op catalog DDL must be skipped so the role does not run an empty apply.
func TestHasExecutableSchemaDDL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{"create table", "CREATE TABLE foo (id int);", true},
		{"create schema", "create schema app;", true},
		{"alter table", "ALTER TABLE foo ADD COLUMN b int;", true},
		{"lowercase create table", "create table bar(id int);", true},
		{"only comments", "-- just a comment\n-- another", false},
		{"only whitespace", "   \n\t\n", false},
		{"grant only", "GRANT SELECT ON foo TO app;", false},
		{
			// The DDL keyword lives only inside a commented-out line, so after
			// stripping line comments there is no executable DDL left.
			name: "create table only in a comment",
			sql:  "-- CREATE TABLE foo (id int);\nGRANT SELECT ON foo TO app;",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasExecutableSchemaDDL(tt.sql); got != tt.want {
				t.Fatalf("hasExecutableSchemaDDL(%q) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}

func TestStripSQLLineComments(t *testing.T) {
	in := "-- header\nCREATE TABLE foo (id int);\n\n  -- trailing note\nGRANT SELECT ON foo TO app;\n"
	got := stripSQLLineComments(in)
	want := "CREATE TABLE foo (id int);\nGRANT SELECT ON foo TO app;\n"
	if got != want {
		t.Fatalf("stripSQLLineComments\n got = %q\nwant = %q", got, want)
	}
}

// safeSchemaFilePrefix sanitizes a database name into a temp-filename fragment:
// keep [A-Za-z0-9_.-], replace everything else with '_', and never return empty.
func TestSafeSchemaFilePrefix(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"analytics", "analytics"},
		{"my.db-1_v2", "my.db-1_v2"},
		{"a b/c:d", "a_b_c_d"},
		{"unicodé", "unicod_"},
		{"", "database"},
		{"///", "___"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := safeSchemaFilePrefix(tt.in); got != tt.want {
				t.Fatalf("safeSchemaFilePrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
