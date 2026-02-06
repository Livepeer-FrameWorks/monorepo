package attribution

import (
	"net/http"
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
	return &pb.SignupAttribution{
		SignupChannel: signupChannel,
		SignupMethod:  signupMethod,
		UtmSource:     query.Get("utm_source"),
		UtmMedium:     query.Get("utm_medium"),
		UtmCampaign:   query.Get("utm_campaign"),
		UtmContent:    query.Get("utm_content"),
		UtmTerm:       query.Get("utm_term"),
		HttpReferer:   r.Referer(),
		LandingPage:   r.URL.String(),
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
	attr.HttpReferer = r.Referer()
	if attr.LandingPage == "" {
		attr.LandingPage = r.URL.String()
	}
	attr.IsAgent = IsAgent(r)
	return attr
}
