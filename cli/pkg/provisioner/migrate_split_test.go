package provisioner

import (
	"reflect"
	"testing"
)

// splitSQLStatements is a hand-rolled lexer that decides which DDL statements
// get executed against a database during migration. A bug here silently changes
// what runs in production, so these tests pin the boundary rules: a ';' only ends
// a statement when it is NOT inside a single/double-quoted string, a -- line
// comment, a /* */ block comment, or a $tag$...$tag$ dollar-quoted body.
func TestSplitSQLStatements(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "two simple statements keep their semicolons",
			in:   "SELECT 1; SELECT 2;",
			want: []string{"SELECT 1;", "SELECT 2;"},
		},
		{
			name: "trailing statement without a semicolon is still emitted",
			in:   "SELECT 1",
			want: []string{"SELECT 1"},
		},
		{
			name: "semicolon inside a single-quoted literal does not split",
			in:   "SELECT ';';",
			want: []string{"SELECT ';';"},
		},
		{
			name: "escaped single quote stays inside the literal",
			in:   "SELECT 'it''s; fine';",
			want: []string{"SELECT 'it''s; fine';"},
		},
		{
			name: "semicolon inside a double-quoted identifier does not split",
			in:   `SELECT "a;b";`,
			want: []string{`SELECT "a;b";`},
		},
		{
			name: "semicolon inside a line comment does not split",
			in:   "SELECT 1 -- end; here\n;",
			want: []string{"SELECT 1 -- end; here\n;"},
		},
		{
			name: "semicolon inside a block comment does not split",
			in:   "SELECT 1 /* a; b */;",
			want: []string{"SELECT 1 /* a; b */;"},
		},
		{
			name: "semicolons inside a dollar-quoted function body do not split",
			in:   "CREATE FUNCTION f() RETURNS void AS $$ BEGIN; PERFORM 1; END; $$ LANGUAGE plpgsql;",
			want: []string{"CREATE FUNCTION f() RETURNS void AS $$ BEGIN; PERFORM 1; END; $$ LANGUAGE plpgsql;"},
		},
		{
			name: "named dollar tag with semicolons in body does not split",
			in:   "DO $body$ BEGIN; END; $body$;",
			want: []string{"DO $body$ BEGIN; END; $body$;"},
		},
		{
			name: "blank input yields no statements",
			in:   "   \n\t  ",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSQLStatements(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitSQLStatements(%q)\n got = %#v\nwant = %#v", tt.in, got, tt.want)
			}
		})
	}
}

// A bare "$" that is not a valid dollar-quote opener (e.g. a literal dollar in
// a numeric default) must not start a dollar-quoted region, otherwise the rest
// of the file would be swallowed into one statement.
func TestSplitSQLStatementsLoneDollarIsNotADollarQuote(t *testing.T) {
	got := splitSQLStatements("SELECT 1 $ 2; SELECT 3;")
	want := []string{"SELECT 1 $ 2;", "SELECT 3;"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

// Characterization test: two consecutive semicolons currently emit a bare ";"
// statement (the empty run between them is trimmed to ";", which is non-empty).
// This documents existing behavior; if statement-emptiness filtering is ever
// added, update this expectation deliberately.
func TestSplitSQLStatementsConsecutiveSemicolonsEmitBareSemicolon(t *testing.T) {
	got := splitSQLStatements("SELECT 1;;SELECT 2;")
	want := []string{"SELECT 1;", ";", "SELECT 2;"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReadDollarTag(t *testing.T) {
	tests := []struct {
		in      string
		wantTag string
		wantOK  bool
	}{
		{"$$", "$$", true},
		{"$body$ rest", "$body$", true},
		{"$tag_1$x", "$tag_1$", true},
		{"$1$", "", false},     // leading digit is not a valid tag char
		{"$ x", "", false},     // space terminates without a closing $
		{"$tag", "", false},    // never closed
		{"$", "", false},       // too short
		{"nope", "", false},    // does not start with $
		{"$a$b$", "$a$", true}, // stops at the first closing $
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			tag, ok := readDollarTag(tt.in)
			if tag != tt.wantTag || ok != tt.wantOK {
				t.Fatalf("readDollarTag(%q) = (%q, %v), want (%q, %v)", tt.in, tag, ok, tt.wantTag, tt.wantOK)
			}
		})
	}
}

func TestParseSequence(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"001_init.sql", 1},
		{"042_add_index.sql", 42},
		{"100_big.sql", 100},
		{"_leading_underscore.sql", 0}, // underscore at index 0 -> 0
		{"nounderscore.sql", 0},        // no underscore -> 0
		{"abc_def.sql", 0},             // non-numeric prefix -> 0
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parseSequence(tt.in); got != tt.want {
				t.Fatalf("parseSequence(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
