package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRelayUpstreamClientKeepsTLSAndHTTPProtocolsAligned(t *testing.T) {
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("upstream protocol = %s, want HTTP/1.1", r.Proto)
		}
		_, _ = io.WriteString(w, "artifact block")
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	t.Cleanup(upstream.Close)

	original := http.DefaultTransport
	base := original.(*http.Transport).Clone()
	base.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	base.TLSClientConfig.NextProtos = []string{"h2", "http/1.1"}
	http.DefaultTransport = base
	t.Cleanup(func() { http.DefaultTransport = original })

	client := newRelayUpstreamClient(testUpstreamBounds)
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("artifact fetch after cloning an HTTP/2-capable default transport: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "artifact block" {
		t.Fatalf("artifact body = %q, error = %v", body, err)
	}
	if response.TLS.NegotiatedProtocol == "h2" {
		t.Fatal("TLS advertised HTTP/2 to an HTTP/1-only relay transport")
	}
}
