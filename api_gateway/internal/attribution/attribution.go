package attribution

import (
	"net/http"
	"net/url"
	"strings"

	pb "frameworks/pkg/proto"
)

// IsAgent checks whether the request comes from an AI agent based on the
// X-Frameworks-Agent header or known agent user-agent patterns.
func IsAgent(r *http.Request) bool {
	if r == nil {
		return false
	}
	agentHeader := r.Header.Get("X-Frameworks-Agent")
	if strings.EqualFold(agentHeader, "true") || strings.EqualFold(agentHeader, "1") {
		return true
	}
	ua := strings.ToLower(r.UserAgent())
	return strings.Contains(ua, "frameworks-agent") || strings.Contains(ua, "mcp")
}

// FromRequest builds a SignupAttribution from an HTTP request, extracting UTM
// parameters, referrer, landing page, referral code, and agent detection.
func FromRequest(r *http.Request, signupChannel, signupMethod string) *pb.SignupAttribution {
	if r == nil {
		return nil
	}
	query := r.URL.Query()
	referralCode := query.Get("referral_code")
	if referralCode == "" {
		referralCode = query.Get("ref")
	}
	landingPage := sanitizeURL(r.URL.String())
	return &pb.SignupAttribution{
		SignupChannel: signupChannel,
		SignupMethod:  signupMethod,
		UtmSource:     query.Get("utm_source"),
		UtmMedium:     query.Get("utm_medium"),
		UtmCampaign:   query.Get("utm_campaign"),
		UtmContent:    query.Get("utm_content"),
		UtmTerm:       query.Get("utm_term"),
		HttpReferer:   sanitizeURL(r.Referer()),
		LandingPage:   landingPage,
		ReferralCode:  referralCode,
		IsAgent:       IsAgent(r),
	}
}

// Enrich merges HTTP-level signals (referer, landing page, agent detection)
// into an existing attribution that was partially populated from the request body.
func Enrich(r *http.Request, attr *pb.SignupAttribution) *pb.SignupAttribution {
	if attr == nil {
		attr = &pb.SignupAttribution{}
	}
	if r == nil {
		return attr
	}
	attr.HttpReferer = sanitizeURL(r.Referer())
	if attr.LandingPage == "" {
		attr.LandingPage = sanitizeURL(r.URL.String())
	}
	attr.IsAgent = IsAgent(r)
	return attr
}

func sanitizeURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if parsed.Scheme == "" && parsed.Host == "" {
		// In net/http handlers, r.URL.String() is typically a relative path.
		// Preserve the path for attribution, but drop any explicit userinfo.
		parsed.User = nil
		return parsed.Path
	}
	parsed.User = nil
	return parsed.String()
}
