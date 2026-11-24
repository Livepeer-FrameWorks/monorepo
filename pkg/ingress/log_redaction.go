package ingress

import (
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
const CredentialURIRegexp = `/(?:(` + credentialPathPrefix + `)` + credentialKey + `|` + credentialKey + `(` + matroskaSuffix + `))`

// The parts every rendered redaction is built from.
const (
	// credentialPathPrefix is the path segment a stream key follows.
	credentialPathPrefix = `(?:webrtc|whip|ingest)/`
	// credentialKey is the stream-key path segment itself.
	credentialKey = `[^/?]+`
	// matroskaSuffix is the extension a Matroska push appends to the key.
	matroskaSuffix = `[.](?:mkv|webm)`
)

// CredentialURIReplacement replaces a CredentialURIRegexp match, keeping the
// path prefix and the Matroska extension.
const CredentialURIReplacement = `/${1}REDACTED${2}`

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
		{Regex: `^(?<fw_prefix>.*?/` + credentialPathPrefix + `)` + credentialKey + `(?<fw_rest>.*)$`, Value: value},
		{Regex: `^(?<fw_prefix>.*?/)` + credentialKey + `(?<fw_rest>` + matroskaSuffix + `.*)$`, Value: value},
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
