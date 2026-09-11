package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const SourcePullCredentialParameter = "token"

// sourcePullCredentialPrefix marks a token as one of ours, so redaction can tell
// a platform source credential from a tenant's own playback token.
const sourcePullCredentialPrefix = "fwsrc."

// SourcePullURL grants access to one accepted source attempt. Only the
// authenticated destination control plane receives this bearer credential.
func SourcePullURL(raw, internalName string, pull OutboundPull) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "dtsc" || u.Host == "" || u.User != nil || u.Fragment != "" || SourcePullBaseURL(raw) != pull.DTSCURL {
		return "", errors.New("invalid source pull URL")
	}
	token := sourcePullCredential(internalName, pull)
	if token == "" {
		return "", errors.New("source pull signing is unavailable")
	}
	query := u.Query()
	query.Set(SourcePullCredentialParameter, token)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// RedactSourcePullCredential removes a source-pull credential from a URL that is
// about to leave the platform, and returns the URL untouched when it carries
// none. Connection request URLs reach tenant-controlled endpoints, and a pull
// whose admission was refused is still evaluated as a viewer, so the credential
// must not ride along. The value is left byte-identical when there is nothing to
// strip, because customers see and may parse these URLs.
func RedactSourcePullCredential(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := u.Query()
	// Every value under the key is inspected, not just the first. A URL carrying
	// the parameter twice is refused admission by SourcePullCredential and is
	// therefore evaluated as a viewer, which is exactly the path that reaches a
	// tenant webhook; checking only the first value would let a credential in the
	// second one ride along.
	strip := false
	for _, value := range query[SourcePullCredentialParameter] {
		if strings.HasPrefix(value, sourcePullCredentialPrefix) {
			strip = true
			break
		}
	}
	if !strip {
		return raw
	}
	query.Del(SourcePullCredentialParameter)
	u.RawQuery = query.Encode()
	return u.String()
}

// SourcePullBaseURL is the credential-free media identity used by discovery
// and physical-source comparisons. Other query parameters remain significant.
func SourcePullBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	query := u.Query()
	query.Del(SourcePullCredentialParameter)
	u.RawQuery = query.Encode()
	return u.String()
}

func SourcePullCredential(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "dtsc" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return ""
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values[SourcePullCredentialParameter]) != 1 {
		return ""
	}
	return values.Get(SourcePullCredentialParameter)
}

func sourcePullCredential(internalName string, pull OutboundPull) string {
	secret := balancerCapabilitySecret()
	if secret == "" || internalName == "" || pull.AttemptID == "" || pull.TenantID == "" || pull.SourceNodeID == "" || pull.DestNodeID == "" || pull.DestClusterID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join([]string{"foghorn-source-pull-v1", sourceInternalKey(internalName), pull.TenantID,
		pull.AttemptID, pull.SourceMediaClusterID, pull.SourceNodeID, pull.SourceGeneration, strconv.FormatInt(pull.SourceRevision, 10),
		pull.DestClusterID, pull.DestNodeID, SourcePullBaseURL(pull.DTSCURL)}, "\x00")))
	return sourcePullCredentialPrefix + pull.AttemptID + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
