package mist

import (
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// ViewerSessionParam carries a FrameWorks playback session on media URLs. It is Mist's own
// session token: Mist bundles a viewer's connections under it and copies it into every manifest
// and segment URL it generates, so each request of one playback carries the same session.
const ViewerSessionParam = "tkn"

// ViewerJWTParam carries a viewer's playback JWT on media URLs. Mist reads a parameter named
// `jwt` as its session token in place of tkn, which would bundle viewers by JWT instead of by
// playback session; Mist does not read this one.
const ViewerJWTParam = "fwjwt"

// NewViewerSessionID issues a FrameWorks playback session ID.
func NewViewerSessionID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// IsViewerSessionID reports whether a Mist session token is an issued FrameWorks playback
// session. A token Mist generated for a request that brought none is not one.
func IsViewerSessionID(token string) bool {
	id, err := uuid.Parse(token)
	return err == nil && id.Version() == 7 && id.String() == token
}

// ViewerSessionID returns the FrameWorks playback session a request URL carries, or "".
func ViewerSessionID(requestURL string) string {
	token := queryValue(requestURL, ViewerSessionParam)
	if !IsViewerSessionID(token) {
		return ""
	}
	return token
}

// ViewerJWT returns the playback credential a viewer presented: the fwjwt parameter of its
// request URL, else the token Mist reported for the session, which is a direct request's own
// jwt. When the request carried no jwt, Mist reports the request's tkn, which is the playback
// session and not a credential.
func ViewerJWT(requestURL, reportedToken string) string {
	if token := queryValue(requestURL, ViewerJWTParam); token != "" {
		return token
	}
	reportedToken = strings.TrimSpace(reportedToken)
	if reportedToken != "" && reportedToken == ViewerSessionID(requestURL) {
		return ""
	}
	return reportedToken
}

func queryValue(requestURL, name string) string {
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" {
		return ""
	}
	u, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Query().Get(name))
}
