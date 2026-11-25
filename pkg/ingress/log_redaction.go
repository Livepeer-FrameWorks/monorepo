package ingress

import (
	"fmt"
	"regexp"
	"strings"
)

// CredentialURIRegexp matches the stream-key segment of every publishing URI
// a FrameWorks HTTP ingress serves: WHIP (/webrtc/<key>, /whip/<key>), the
// Foghorn ingest front door (/ingest/<key>) and HTTP Matroska pushes
// (/<key>.mkv, /<key>.webm, under any prefix such as /view/). Playback IDs
// share the Matroska URL shape and are redacted with it.
//
// It is one RE2 expression with one replacement so it renders as a single
// Caddy `regexp` log filter, which every supported Caddy version has; the
// `multi_regexp` filter only exists from Caddy 2.11. Characters that would
// need escaping in a Caddyfile, Jinja or YAML string are avoided.
var CredentialURIRegexp = `(` + credentialSlash + `)(?:(` + credentialPathPrefix + `)` + credentialKey + `|` + credentialKey + `(` + matroskaSuffix + `))`

// The parts every rendered redaction is built from.
const (
	// credentialPathSegments are the path segments a stream key follows:
	// the Foghorn ingest front door and WHIP's two publish paths.
	credentialPathSegments = `ingest|webrtc|whip`
	credentialSlash        = `(?:/|%2[fF])`
	// credentialKey is the stream-key path segment itself.
	credentialKey = `[^/?]+`
)

// Mist decodes the path once before routing. Match the equivalent raw URI
// spellings because both proxies log the original, escaped request target.
var (
	credentialPathPrefix = `(?:` + encodedPathLiteral("ingest") + `|` + encodedPathLiteral("webrtc") + `|` + encodedPathLiteral("whip") + `)` + credentialSlash
	matroskaSuffix       = `(?:[.]|%2[eE])(?:` + encodedPathLiteral("mkv") + `|` + encodedPathLiteral("webm") + `)`
)

func encodedPathLiteral(path string) string {
	var pattern strings.Builder
	for _, c := range []byte(path) {
		encoded := fmt.Sprintf("%02x", c)
		pattern.WriteString("(?:" + string(c) + "|%")
		for _, digit := range encoded {
			if digit >= 'a' && digit <= 'f' {
				pattern.WriteString("[" + string(digit) + strings.ToUpper(string(digit)) + "]")
			} else {
				pattern.WriteRune(digit)
			}
		}
		pattern.WriteByte(')')
	}
	return pattern.String()
}

// CredentialURIReplacement replaces a CredentialURIRegexp match, keeping the
// path prefix and the Matroska extension.
const CredentialURIReplacement = `${1}${2}REDACTED${3}`

var credentialURI = regexp.MustCompile(CredentialURIRegexp)

// RedactCredentialURI applies the redaction exactly as Caddy's regexp log
// filter does (Regexp.ReplaceAllString over the logged request URI).
func RedactCredentialURI(uri string) string {
	return credentialURI.ReplaceAllString(uri, CredentialURIReplacement)
}

// CaddyCredentialURILogField is the Caddy `format filter` fields entry that
// redacts publishing credentials from logged request URIs.
func CaddyCredentialURILogField() string {
	return `request>uri regexp "` + CredentialURIRegexp + `" "` + CredentialURIReplacement + `"`
}

// NginxMapEntry is one regex key and value of an Nginx map block.
type NginxMapEntry struct {
	Regex string
	Value string
}

// NginxCredentialURIMap returns the regex entries of the Nginx
// `map $request_uri $frameworks_logged_uri` block, in order (Nginx uses the
// first matching regex). An Nginx map cannot substitute inside a value, so
// each alternative of CredentialURIRegexp becomes one whole-URI entry that
// rebuilds the URI around the redacted key; for any URI it logs what the
// Caddy filter logs for the first credential in it.
func NginxCredentialURIMap() []NginxMapEntry {
	const value = `${fw_prefix}REDACTED${fw_rest}`
	return []NginxMapEntry{
		{Regex: `^(?<fw_prefix>.*?` + credentialSlash + credentialPathPrefix + `)` + credentialKey + `(?<fw_rest>.*)$`, Value: value},
		{Regex: `^(?<fw_prefix>.*?` + credentialSlash + `)` + credentialKey + `(?<fw_rest>` + matroskaSuffix + `.*)$`, Value: value},
	}
}

// NginxCredentialURIMapBlock renders the Nginx map that sets
// $frameworks_logged_uri, indented for the http block.
func NginxCredentialURIMapBlock(indent string) string {
	var b strings.Builder
	b.WriteString(indent + "map $request_uri $frameworks_logged_uri {\n")
	for _, e := range NginxCredentialURIMap() {
		b.WriteString(indent + `    "~` + e.Regex + `" "` + e.Value + "\";\n")
	}
	b.WriteString(indent + "    default $request_uri;\n" + indent + "}\n")
	return b.String()
}

// CredentialPathPrefixes returns the request path prefixes whose next segment
// is a publishing credential, in the order renderers emit them.
func CredentialPathPrefixes() []string {
	segments := strings.Split(credentialPathSegments, "|")
	prefixes := make([]string, len(segments))
	for i, segment := range segments {
		prefixes[i] = "/" + segment + "/"
	}
	return prefixes
}
