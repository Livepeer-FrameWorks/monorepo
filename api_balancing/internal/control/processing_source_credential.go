package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProcessingSourceCredentialParam is the processing-job parameter that carries
// the credential Helmsman presents when it reads a job's node-local source
// (a Mist /view cut of a live buffer, rolling DVR or chapter) from Mist. The
// read is a Mist HTTP output, so Mist raises PLAY_REWRITE/USER_NEW for it like
// for any viewer; the credential is what lets Foghorn recognise it as the
// platform's own processing read and skip viewer placement admission and
// viewer accounting for it.
const ProcessingSourceCredentialParam = "source_credential"

// processingSourceCredentialPrefix marks a processing-source credential in a
// request URL so redaction and admission can tell it from a tenant playback
// token that happens to use the same query parameter.
const processingSourceCredentialPrefix = "fwproc."

// ProcessingSourceCredential mints the credential for one processing job's
// source read: bound to the tenant, the source stream, the node whose Mist
// serves the read, the artifact being produced and an expiry. It is signed with
// the cell's balancer capability secret, which every Foghorn of the cell shares,
// so the Foghorn that dispatches the job and the one that admits the read need
// no shared state. Empty when the cell has no secret or an identity is missing.
func ProcessingSourceCredential(tenantID, internalName, nodeID, artifactHash string, expires time.Time) string {
	tenantID, nodeID, artifactHash = strings.TrimSpace(tenantID), strings.TrimSpace(nodeID), strings.TrimSpace(artifactHash)
	if tenantID == "" || nodeID == "" || artifactHash == "" || strings.Contains(artifactHash, ".") || expires.IsZero() || sourceInternalKey(internalName) == "" {
		return ""
	}
	expiresUnix := strconv.FormatInt(expires.Unix(), 10)
	sig := processingSourceSignature(tenantID, internalName, nodeID, artifactHash, expiresUnix)
	if sig == "" {
		return ""
	}
	return processingSourceCredentialPrefix + artifactHash + "." + expiresUnix + "." + sig
}

// AcceptedProcessingSourceRead reports whether requestURL carries a valid,
// unexpired processing-source credential for exactly this tenant, source stream
// and serving node, returning the artifact hash it was minted for. A URL that
// carries the parameter more than once is refused, so a tenant token cannot
// ride along with a platform credential.
func AcceptedProcessingSourceRead(requestURL, tenantID, internalName, nodeID string, now time.Time) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(requestURL))
	if err != nil {
		return "", false
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values[SourcePullCredentialParameter]) != 1 {
		return "", false
	}
	token := values.Get(SourcePullCredentialParameter)
	if !strings.HasPrefix(token, processingSourceCredentialPrefix) || len(token) > 512 {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(token, processingSourceCredentialPrefix), ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	artifactHash, expiresUnix, sig := parts[0], parts[1], parts[2]
	expires, err := strconv.ParseInt(expiresUnix, 10, 64)
	if err != nil || now.IsZero() || !now.Before(time.Unix(expires, 0)) {
		return "", false
	}
	want := processingSourceSignature(strings.TrimSpace(tenantID), internalName, strings.TrimSpace(nodeID), artifactHash, expiresUnix)
	if want == "" || !hmac.Equal([]byte(want), []byte(sig)) {
		return "", false
	}
	return artifactHash, true
}

func processingSourceSignature(tenantID, internalName, nodeID, artifactHash, expiresUnix string) string {
	secret := balancerCapabilitySecret()
	internalKey := sourceInternalKey(internalName)
	if secret == "" || tenantID == "" || internalKey == "" || nodeID == "" || artifactHash == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join([]string{"foghorn-processing-source-v1", tenantID, internalKey, nodeID, artifactHash, expiresUnix}, "\x00")))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
