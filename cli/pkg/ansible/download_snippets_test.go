package ansible

import (
	"strings"
	"testing"
)

func TestRobustDownloadSnippet_shape(t *testing.T) {
	t.Parallel()
	snippet := RobustDownloadSnippet("https://example.com/x.tar.gz", "sha256:abc123", "/tmp/x.tar.gz")
	want := []string{
		"curl --fail",
		"--location",
		"--retry 5",
		"--retry-delay 2",
		"--retry-connrefused",
		"sha256sum -c",
		"sha512sum -c",
		`"$__DST__"`,
		`"$__URL__"`,
	}
	for _, fragment := range want {
		if !strings.Contains(snippet, fragment) {
			t.Errorf("RobustDownloadSnippet missing %q", fragment)
		}
	}
	if strings.Contains(snippet, "curl -sSLO") {
		t.Error("RobustDownloadSnippet must not use bare curl -sSLO")
	}
}

func TestRobustDownloadSnippet_unknownAlgoExits(t *testing.T) {
	t.Parallel()
	snippet := RobustDownloadSnippet("https://example.com/x", "md5:abc", "/tmp/x")
	if !strings.Contains(snippet, "unsupported checksum algo") {
		t.Error("snippet must reject unknown algos at runtime")
	}
	if !strings.Contains(snippet, "exit 1") {
		t.Error("unknown-algo branch must exit 1")
	}
}

func TestRobustDownloadSnippet_emptyChecksumSkipsSilently(t *testing.T) {
	t.Parallel()
	snippet := RobustDownloadSnippet("https://example.com/x", "", "/tmp/x")
	if !strings.Contains(snippet, `"")       ;;`) {
		t.Error("empty checksum branch must be a silent no-op")
	}
	forbidden := []string{
		"WARNING: no checksum",
		"checksum required",
	}
	for _, f := range forbidden {
		if strings.Contains(snippet, f) {
			t.Errorf("empty-checksum branch must not emit %q — upstream may legitimately not publish one", f)
		}
	}
}

func TestRobustDownloadSnippet_shellInjectionIsNeutralized(t *testing.T) {
	t.Parallel()
	evilURL := "https://example.com/a'b;rm -rf /"
	evilSum := "sha256:$(whoami)"
	evilDst := "x'; echo pwned"
	snippet := RobustDownloadSnippet(evilURL, evilSum, evilDst)

	wantQuoted := []string{
		`__URL__='https://example.com/a'\''b;rm -rf /'`,
		`__SUM__='sha256:$(whoami)'`,
		`__DST__='x'\''; echo pwned'`,
	}
	for _, fragment := range wantQuoted {
		if !strings.Contains(snippet, fragment) {
			t.Errorf("expected shell-quoted form %q in snippet", fragment)
		}
	}

	forbidden := []string{"rm -rf /;", "$(whoami);", `echo pwned"`}
	for _, fragment := range forbidden {
		if strings.Contains(snippet, fragment) {
			t.Errorf("snippet leaks metacharacter run %q — shq() not quoting", fragment)
		}
	}
}

func TestShq_standaloneQuoting(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":             `''`,
		"abc":          `'abc'`,
		"a b":          `'a b'`,
		"it's":         `'it'\''s'`,
		"$(x)":         `'$(x)'`,
		"a'b'c":        `'a'\''b'\''c'`,
		"double\"quot": `'double"quot'`,
	}
	for in, want := range cases {
		if got := shq(in); got != want {
			t.Errorf("shq(%q) = %q, want %q", in, got, want)
		}
	}
}
