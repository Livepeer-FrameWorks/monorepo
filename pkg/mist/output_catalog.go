package mist

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"
)

// FetchPlaybackOutputsContext reads enabled connectors and their method URLs.
// Metrics alone omit connectors such as MP4 that have only method-level URLs.
// The caller serializes this with other authenticated calls on the client.
func (c *Client) FetchPlaybackOutputsContext(ctx context.Context, reported map[string]any) (map[string]any, error) {
	inventory, err := c.makeAPIRequestContext(ctx, map[string]any{"capabilities": true, "config_backup": true})
	if err != nil {
		return nil, err
	}
	return PlaybackOutputCatalog(reported, inventory)
}

// PlaybackOutputCatalog retains observed listeners and adds declared methods
// only for enabled HTTP-dependent connectors with an observed HTTP listener.
func PlaybackOutputCatalog(reported, inventory map[string]any) (map[string]any, error) {
	backup, backupOK := inventory["config_backup"].(map[string]any)
	config, configOK := backup["config"].(map[string]any)
	protocols, ok := config["protocols"].([]any)
	capabilities, capabilitiesOK := inventory["capabilities"].(map[string]any)
	connectors, capsOK := capabilities["connectors"].(map[string]any)
	if !backupOK || !configOK || !ok || !capabilitiesOK || !capsOK {
		return nil, errors.New("mist playback inventory lacks configured protocols or connector capabilities")
	}
	enabled := make(map[string]bool, len(protocols))
	for _, raw := range protocols {
		p, valid := raw.(map[string]any)
		if !valid {
			continue
		}
		name, named := p["connector"].(string)
		if named && name != "" && p["enabled"] != false && p["disabled"] != true {
			enabled[strings.ToLower(name)] = true
		}
	}
	owners := make(map[string]string, len(connectors))
	for name, raw := range connectors {
		owners[strings.ToLower(name)] = strings.ToLower(name)
		connector, valid := raw.(map[string]any)
		if !valid {
			continue
		}
		methods, declared := connector["methods"].([]any)
		if !declared {
			continue
		}
		for _, rawMethod := range methods {
			method, valid := rawMethod.(map[string]any)
			if !valid {
				continue
			}
			if label, named := method["hrn"].(string); named && label != "" {
				owners[strings.ToLower(label)] = strings.ToLower(name)
			}
		}
	}
	catalog := make(map[string]any, len(reported))
	for name, value := range reported {
		if strings.EqualFold(name, nativeMatroskaOutput) {
			continue
		}
		if owner, known := owners[strings.ToLower(name)]; known && !enabled[owner] {
			continue
		}
		catalog[name] = value
	}

	var httpBase *url.URL
	for _, name := range []string{"HTTPS", "HTTP"} {
		if !enabled[strings.ToLower(name)] {
			continue
		}
		raw := ingestTemplate(catalog, name)
		u := parseIngestTemplate(raw)
		if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.ForceQuery || !strings.HasSuffix(u.EscapedPath(), "/$.html") {
			continue
		}
		httpBase = u
		break
	}
	if httpBase == nil {
		return catalog, nil
	}

	for name, raw := range connectors {
		if !enabled[strings.ToLower(name)] {
			continue
		}
		connector, valid := raw.(map[string]any)
		if !valid {
			continue
		}
		dependency, dependent := connector["deps"].(string)
		if !dependent || (!strings.EqualFold(dependency, "HTTP") && !strings.EqualFold(dependency, "HTTPS")) {
			continue
		}
		if strings.EqualFold(name, "EBML") {
			matches, _ := connector["url_match"].([]any)
			for _, match := range matches {
				if match == "/$.mkv" {
					catalog[nativeMatroskaOutput] = playbackMethodURL(httpBase, "/$.mkv", "http")
					break
				}
			}
		}
		if hasReportedListener(catalog, name) {
			continue
		}
		methods, declared := connector["methods"].([]any)
		if !declared {
			continue
		}
		for _, rawMethod := range methods {
			method, valid := rawMethod.(map[string]any)
			if !valid || method["enabled"] == false || method["disabled"] == true {
				continue
			}
			label, named := method["hrn"].(string)
			relative, located := method["url_rel"].(string)
			handler, handled := method["handler"].(string)
			if !named || !located || !handled || strings.EqualFold(label, nativeMatroskaOutput) {
				continue
			}
			endpoint := playbackMethodURL(httpBase, relative, handler)
			if strings.TrimSpace(label) == "" || endpoint == "" {
				continue
			}
			if previous, exists := catalog[label]; exists && previous != endpoint {
				return nil, errors.New("mist playback inventory contains conflicting method URLs")
			}
			catalog[label] = endpoint
		}
	}
	return catalog, nil
}

func playbackMethodURL(base *url.URL, relative, handler string) string {
	if handler != "http" && handler != "ws" {
		return ""
	}
	u, err := url.Parse(relative)
	if err != nil || !strings.HasPrefix(relative, "/") || u.IsAbs() || u.Host != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.Count(u.Path, "$") != 1 || path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") {
		return ""
	}
	out := *base
	rawPath := strings.TrimSuffix(base.EscapedPath(), "/$.html") + u.EscapedPath()
	out.Path, err = url.PathUnescape(rawPath)
	if err != nil {
		return ""
	}
	out.RawPath = rawPath
	if handler == "ws" {
		if out.Scheme == "https" {
			out.Scheme = "wss"
		} else {
			out.Scheme = "ws"
		}
	}
	return out.String()
}
