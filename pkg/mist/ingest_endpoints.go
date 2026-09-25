package mist

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type IngestURLs struct {
	WHIP string
	RTMP string
	SRT  string
}

// IngestPublicOrigin keeps the destination's advertised HTTP scheme and port.
// Publishing listener ports do not identify its public HTTP endpoint.
func IngestPublicOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "$\r\n\t") || strings.EqualFold(u.Hostname(), "HOST") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// ResolveIngestURLs consumes the online-listener templates in Mist's metrics
// report. Ports and public overrides belong to the reporting node. HLS or a
// public HTTP address alone is not evidence of an ingest listener.
func ResolveIngestURLs(outputs map[string]any, publicBase, streamKey string) IngestURLs {
	if !validIngestKey(streamKey) {
		return IngestURLs{}
	}
	return IngestURLs{
		WHIP: resolveIngestTemplate(whipIngestTemplate(outputs), publicBase, streamKey, "whip"),
		RTMP: resolveIngestTemplate(ingestTemplate(outputs, "RTMP"), publicBase, streamKey, "rtmp"),
		SRT:  resolveIngestTemplate(ingestTemplate(outputs, "TSSRT", "SRT"), publicBase, streamKey, "srt"),
	}
}

func validIngestKey(streamKey string) bool {
	return streamKey != "" && strings.TrimSpace(streamKey) == streamKey && strings.IndexFunc(streamKey, unicode.IsControl) < 0 && streamKey != "." && streamKey != ".."
}

// ResolveIngestEndpointTemplate confirms the node's reported publishing listener
// without a stream key. The single $ is bound by the credential-owning front door.
func ResolveIngestEndpointTemplate(outputs map[string]any, publicBase, protocol string) string {
	var template string
	switch protocol {
	case "whip":
		template = whipIngestTemplate(outputs)
	case "rtmp":
		template = ingestTemplate(outputs, "RTMP")
	case "srt":
		template = ingestTemplate(outputs, "TSSRT", "SRT")
	default:
		return ""
	}
	u := normalizedIngestTemplate(template, publicBase, protocol)
	if u == nil {
		return ""
	}
	return u.String()
}

// BindIngestEndpointTemplate accepts only an already resolved publishing
// template. It cannot substitute hosts, protocols, query fields or applications.
func BindIngestEndpointTemplate(template, protocol, streamKey string) string {
	if !validIngestKey(streamKey) {
		return ""
	}
	u := normalizeIngestTemplate(template, "", protocol, "live")
	if u == nil || u.Hostname() == "HOST" {
		return ""
	}
	return bindIngestKey(u, protocol, streamKey)
}

func ValidIngestEndpointTemplate(template, protocol string) bool {
	u := normalizeIngestTemplate(template, "", protocol, "live")
	return u != nil && u.Hostname() != "HOST"
}

func ingestTemplate(outputs map[string]any, aliases ...string) string {
	var result string
	for name, raw := range outputs {
		for _, alias := range aliases {
			if !strings.EqualFold(name, alias) {
				continue
			}
			var template string
			switch value := raw.(type) {
			case string:
				template = value
			case []any:
				if len(value) == 1 {
					if text, ok := value[0].(string); ok {
						template = text
					}
				}
			case []string:
				if len(value) == 1 {
					template = value[0]
				}
			}
			if template == "" || (result != "" && result != template) {
				return ""
			}
			result = template
		}
	}
	return result
}

func whipIngestTemplate(outputs map[string]any) string {
	raw := ingestTemplate(outputs, "WebRTC", "WebRTC with WebSocket signalling")
	listener := parseIngestTemplate(raw)
	if listener == nil {
		return ""
	}
	if listener.Scheme == "http" || listener.Scheme == "https" {
		return raw
	}
	if (listener.Scheme != "ws" && listener.Scheme != "wss") || listener.RawQuery != "" || !strings.HasSuffix(listener.Path, "/webrtc/$") {
		return ""
	}
	// Mist's WebRTC listener can advertise its UDP port with a ws URL.
	// WHIP signalling belongs to the separately advertised HTTP listener.
	for _, name := range []string{"HTTPS", "HTTP"} {
		httpListener := parseIngestTemplate(ingestTemplate(outputs, name))
		if httpListener == nil || (httpListener.Scheme != "http" && httpListener.Scheme != "https") || httpListener.RawQuery != "" || !strings.HasSuffix(httpListener.Path, "/$.html") {
			continue
		}
		rawPath := strings.TrimSuffix(httpListener.EscapedPath(), "/$.html") + "/webrtc/$"
		httpListener.Path = strings.TrimSuffix(httpListener.Path, "/$.html") + "/webrtc/$"
		httpListener.RawPath = rawPath
		return httpListener.String()
	}
	return ""
}

func parseIngestTemplate(template string) *url.URL {
	if strings.Count(template, "$") != 1 || strings.IndexFunc(template, unicode.IsControl) >= 0 {
		return nil
	}
	u, err := url.Parse(template)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil
	}
	if port := u.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return nil
		}
	}
	return u
}

func resolveIngestTemplate(template, publicBase, streamKey, protocol string) string {
	u := normalizedIngestTemplate(template, publicBase, protocol)
	if u == nil {
		return ""
	}
	return bindIngestKey(u, protocol, streamKey)
}

func bindIngestKey(u *url.URL, protocol, streamKey string) string {
	if protocol == "srt" {
		query := u.Query()
		query.Set("streamid", streamKey)
		u.RawQuery = query.Encode()
		return u.String()
	}
	return strings.ReplaceAll(u.String(), "$", EncodeStreamNamePath(streamKey))
}

// SupportsIngestProtocol validates the actual listener without a publishing
// credential. Discovery does not need to distribute a stream key to candidate cells.
func SupportsIngestProtocol(outputs map[string]any, publicBase, protocol string) bool {
	return ResolveIngestEndpointTemplate(outputs, publicBase, protocol) != ""
}

func normalizedIngestTemplate(template, publicBase, protocol string) *url.URL {
	return normalizeIngestTemplate(template, publicBase, protocol, "play")
}

func normalizeIngestTemplate(template, publicBase, protocol, rtmpApplication string) *url.URL {
	u := parseIngestTemplate(template)
	if u == nil || u.ForceQuery {
		return nil
	}
	if (protocol == "whip" && u.Scheme != "http" && u.Scheme != "https") || (protocol == "rtmp" && u.Scheme != "rtmp" && u.Scheme != "rtmps") || (protocol == "srt" && u.Scheme != "srt") {
		return nil
	}
	if u.Hostname() == "HOST" {
		base, parseErr := url.Parse(publicBase)
		if parseErr != nil || base.Hostname() == "" || base.Hostname() == "HOST" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Opaque != "" || strings.Contains(base.Path, "$") || (base.Scheme != "http" && base.Scheme != "https") {
			return nil
		}
		if port := base.Port(); port != "" {
			value, portErr := strconv.Atoi(port)
			if portErr != nil || value < 1 || value > 65535 {
				return nil
			}
		}
		if protocol == "whip" {
			u.Scheme, u.Host = base.Scheme, base.Host
			if u.Path == "/webrtc/$" || u.Path == "/whip/$" {
				rawPath := strings.TrimRight(base.EscapedPath(), "/") + u.EscapedPath()
				u.Path = strings.TrimRight(base.Path, "/") + u.Path
				u.RawPath = rawPath
			}
		} else if u.Port() != "" {
			u.Host = net.JoinHostPort(base.Hostname(), u.Port())
		} else {
			u.Host = base.Hostname()
			if strings.Contains(u.Host, ":") {
				u.Host = "[" + u.Host + "]"
			}
		}
	}
	if strings.ContainsAny(u.Host, "$\\ \t\r\n") {
		return nil
	}
	if protocol != "srt" && strings.Count(u.Path, "$") != 1 {
		return nil
	}
	switch protocol {
	case "whip":
		if (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || (!strings.HasSuffix(u.Path, "/webrtc/$") && !strings.HasSuffix(u.Path, "/whip/$")) {
			return nil
		}
	case "rtmp":
		suffix := "/" + rtmpApplication + "/$"
		if (u.Scheme != "rtmp" && u.Scheme != "rtmps") || u.RawQuery != "" || !strings.HasSuffix(u.Path, suffix) {
			return nil
		}
		// Mist advertises its playback application; publishing uses the live
		// application on the same advertised listener.
		rawPath := strings.TrimSuffix(u.EscapedPath(), suffix) + "/live/$"
		u.Path = strings.TrimSuffix(u.Path, suffix) + "/live/$"
		u.RawPath = rawPath
	case "srt":
		query, parseErr := url.ParseQuery(u.RawQuery)
		if u.Scheme != "srt" || u.Port() == "" || parseErr != nil || len(query) != 1 || len(query["streamid"]) != 1 || query.Get("streamid") != "$" || (u.Path != "" && u.Path != "/") {
			return nil
		}
	default:
		return nil
	}
	return u
}
