package ansible

import (
	"fmt"
	"strings"
)

// RobustDownloadSnippet returns a bash snippet that fetches url to destPath
// and fails on any non-2xx response. If checksum is "<algo>:<hex>" with algo
// in {sha256, sha512} the snippet also verifies it; an empty checksum skips
// verification (used when upstream does not publish one). Every argument is
// single-quote-escaped before being spliced into the snippet so callers
// never need to pre-quote.
func RobustDownloadSnippet(url, checksum, destPath string) string {
	return fmt.Sprintf(`__URL__=%s
__SUM__=%s
__DST__=%s
curl --fail --location --show-error --retry 5 --retry-delay 2 --retry-connrefused -o "$__DST__" "$__URL__"
case "$__SUM__" in
  sha256:*) printf '%%s  %%s\n' "${__SUM__#sha256:}" "$__DST__" | sha256sum -c - ;;
  sha512:*) printf '%%s  %%s\n' "${__SUM__#sha512:}" "$__DST__" | sha512sum -c - ;;
  "")       ;;
  *)        echo "unsupported checksum algo: ${__SUM__%%%%:*}" >&2 ; exit 1 ;;
esac
`, shq(url), shq(checksum), shq(destPath))
}

// shq wraps s in single quotes so bash treats it as one literal token,
// escaping any embedded single quote via the standard close-open pattern.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
