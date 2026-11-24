package ingress

import "regexp"

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
const CredentialURIRegexp = `/(?:((?:webrtc|whip|ingest)/)[^/?]+|[^/?]+([.](?:mkv|webm)))`

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
