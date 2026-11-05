//go:build stack_verify

package main

import "net/http"

// Only stack builds route public provider requests to the isolated provider
// simulator. Production builds contain neither this transport nor its target.
type stackProviderTransport struct{ next http.RoundTripper }

func (t stackProviderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Hostname() {
	case "api.stripe.com", "invoice.stripe.com", "files.stripe.com", "vies.stack.test", "facilitator.stack.test":
		copy := r.Clone(r.Context())
		u := *r.URL
		copy.URL = &u
		copy.Header.Set("X-Stack-Provider-Host", r.URL.Hostname())
		copy.URL.Scheme = "http"
		copy.URL.Host = "billing-provider:18778"
		copy.Host = "billing-provider:18778"
		return t.next.RoundTrip(copy)
	default:
		return t.next.RoundTrip(r)
	}
}

func init() {
	http.DefaultTransport = stackProviderTransport{next: http.DefaultTransport}
}
