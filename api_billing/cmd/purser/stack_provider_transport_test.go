//go:build stack_verify

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type stackTransportRecorder struct{ request *http.Request }

func (r *stackTransportRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.request = req
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("fixture")), Header: make(http.Header)}, nil
}

func TestStackProviderTransportRestrictsHostRouting(t *testing.T) {
	for _, host := range []string{"api.stripe.com", "invoice.stripe.com", "vies.stack.test", "facilitator.stack.test", "api.stripe.com.attacker.test", "example.test"} {
		t.Run(host, func(t *testing.T) {
			recorder := &stackTransportRecorder{}
			transport := stackProviderTransport{next: recorder}
			req, err := http.NewRequest(http.MethodGet, "https://"+host+"/facts?q=one", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			known := host == "api.stripe.com" || host == "invoice.stripe.com" || host == "vies.stack.test" || host == "facilitator.stack.test"
			if known && (recorder.request.URL.Host != "billing-provider:18778" || recorder.request.URL.Scheme != "http" || recorder.request.Header.Get("X-Stack-Provider-Host") != host) {
				t.Fatalf("fixture request: %+v", recorder.request)
			}
			if !known && recorder.request != req {
				t.Fatal("unrelated URL rewritten")
			}
			if req.URL.Scheme != "https" || req.URL.Host != host || req.Header.Get("X-Stack-Provider-Host") != "" {
				t.Fatal("original validated request was mutated")
			}
			if recorder.request.URL.RawQuery != "q=one" || recorder.request.URL.Path != "/facts" {
				t.Fatal("provider path/query changed")
			}
		})
	}
}
