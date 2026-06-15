package database

import (
	"net/url"
	"strings"
	"testing"
)

func TestWithPgxExecMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		// for URL inputs we assert via parsed query; for keyword/value we assert substring
		wantExecParam bool
		keywordForm   bool
	}{
		{name: "url adds param", in: "postgres://u:p@h1:5433,h2:5433/db?sslmode=disable&load_balance=true", wantExecParam: true},
		{name: "url already set keeps", in: "postgres://u@h/db?default_query_exec_mode=simple_protocol", wantExecParam: true},
		{name: "postgresql scheme", in: "postgresql://u@h/db?sslmode=disable", wantExecParam: true},
		{name: "keyword/value appended", in: "host=db port=5432 user=u dbname=d sslmode=disable", wantExecParam: true, keywordForm: true},
		{name: "keyword/value already set", in: "host=db dbname=d default_query_exec_mode=exec", wantExecParam: true, keywordForm: true},
		{name: "empty unchanged", in: "", wantExecParam: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := withPgxExecMode(tt.in)
			if err != nil {
				t.Fatalf("withPgxExecMode(%q): %v", tt.in, err)
			}
			if tt.in == "" {
				if got != "" {
					t.Fatalf("empty DSN must be unchanged, got %q", got)
				}
				return
			}
			if tt.keywordForm {
				// Must remain keyword/value form (not url-escaped) and contain the param.
				if strings.Contains(got, "%") {
					t.Fatalf("keyword/value DSN was URL-corrupted: %q", got)
				}
				if !strings.Contains(got, "default_query_exec_mode=") {
					t.Fatalf("exec mode not present in keyword DSN: %q", got)
				}
				if !strings.HasPrefix(got, "host=") {
					t.Fatalf("keyword DSN shape changed: %q", got)
				}
				return
			}
			u, perr := url.Parse(got)
			if perr != nil {
				t.Fatalf("result must parse as URL: %v (got %q)", perr, got)
			}
			if u.Query().Get("default_query_exec_mode") == "" {
				t.Fatalf("exec mode not set on URL DSN: %q", got)
			}
			// Idempotent: an already-set value must be preserved, not overwritten.
			if strings.Contains(tt.in, "simple_protocol") && u.Query().Get("default_query_exec_mode") != "simple_protocol" {
				t.Fatalf("existing exec mode overwritten: %q", got)
			}
		})
	}
}
