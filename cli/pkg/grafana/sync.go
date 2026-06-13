// Package grafana pushes the repo-owned dashboards and their datasources to a
// cluster's Grafana over the HTTP API. Managed objects are keyed by pinned
// uids (folder "frameworks", datasources "prometheus"/"clickhouse", dashboard
// uids) so the sync is idempotent and leaves operator-made content outside
// those managed identities alone.
package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	FolderUID   = "frameworks"
	FolderTitle = "FrameWorks"

	clickhousePluginID = "grafana-clickhouse-datasource"
	vmPluginID         = "victoriametrics-metrics-datasource"
)

type DashboardSource struct {
	Name    string
	Content []byte
}

type ClickHouseDatasource struct {
	Server   string
	Port     int
	Username string
	Password string
}

type SyncOptions struct {
	BaseURL string
	// APIKey takes precedence; otherwise Username/Password basic auth
	// (the GF_SECURITY_ADMIN_* credentials).
	APIKey   string
	Username string
	Password string
	// VictoriaMetricsURL ensures the uid=victoriametrics datasource when
	// non-empty (the VM base URL; the plugin handles API paths itself).
	VictoriaMetricsURL string
	// ClickHouse ensures the uid=clickhouse datasource when non-nil.
	ClickHouse *ClickHouseDatasource
	Dashboards []DashboardSource
	DryRun     bool
	HTTPClient *http.Client
	Out        io.Writer
}

type SyncSummary struct {
	FoldersCreated     int
	DatasourcesCreated int
	DatasourcesUpdated int
	DashboardsSynced   int
	Skipped            int
}

type client struct {
	baseURL  string
	apiKey   string
	username string
	password string
	http     *http.Client
}

func Sync(ctx context.Context, opts SyncOptions) (SyncSummary, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return SyncSummary{}, errors.New("grafana URL is required")
	}
	if strings.TrimSpace(opts.APIKey) == "" && strings.TrimSpace(opts.Username) == "" {
		return SyncSummary{}, errors.New("grafana API key or admin username/password is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	c := client{
		baseURL:  strings.TrimRight(opts.BaseURL, "/"),
		apiKey:   opts.APIKey,
		username: opts.Username,
		password: opts.Password,
		http:     opts.HTTPClient,
	}

	var summary SyncSummary
	created, err := c.ensureFolder(ctx, opts.DryRun, opts.Out)
	if err != nil {
		return summary, err
	}
	if created {
		summary.FoldersCreated++
	}

	if opts.VictoriaMetricsURL != "" {
		created, updated, err := c.ensureDatasource(ctx, "victoriametrics", map[string]any{
			"uid":       "victoriametrics",
			"name":      "VictoriaMetrics",
			"type":      vmPluginID,
			"access":    "proxy",
			"url":       opts.VictoriaMetricsURL,
			"isDefault": true,
		}, opts.DryRun, opts.Out)
		if err != nil {
			if strings.Contains(err.Error(), "plugin") {
				err = fmt.Errorf("%w — the VictoriaMetrics datasource plugin is not installed; provision sets GF_INSTALL_PLUGINS for grafana, re-run `frameworks cluster provision` (or restart the grafana container after adding %s to that env)", err, vmPluginID)
			}
			return summary, err
		}
		summary.DatasourcesCreated += created
		summary.DatasourcesUpdated += updated
	}
	if opts.ClickHouse != nil {
		created, updated, err := c.ensureDatasource(ctx, "clickhouse", map[string]any{
			"uid":    "clickhouse",
			"name":   "ClickHouse",
			"type":   clickhousePluginID,
			"access": "proxy",
			"jsonData": map[string]any{
				"host":            opts.ClickHouse.Server,
				"port":            opts.ClickHouse.Port,
				"protocol":        "http",
				"username":        opts.ClickHouse.Username,
				"defaultDatabase": "periscope",
			},
			"secureJsonData": map[string]any{
				"password": opts.ClickHouse.Password,
			},
		}, opts.DryRun, opts.Out)
		if err != nil {
			if strings.Contains(err.Error(), "plugin") {
				err = fmt.Errorf("%w — the ClickHouse datasource plugin is not installed; provision sets GF_INSTALL_PLUGINS=%s for grafana, re-run `frameworks cluster provision` (or restart the grafana container after adding that env)", err, clickhousePluginID)
			}
			return summary, err
		}
		summary.DatasourcesCreated += created
		summary.DatasourcesUpdated += updated
	}

	for _, src := range opts.Dashboards {
		var dash map[string]any
		if err := json.Unmarshal(src.Content, &dash); err != nil {
			return summary, fmt.Errorf("parse dashboard %s: %w", src.Name, err)
		}
		if _, classic := dash["panels"]; !classic {
			// Schema-V2 exports (elements/layout) flow UI → repo as backups;
			// they are not push targets.
			fmt.Fprintf(opts.Out, "skipping %s: not a classic dashboard (UI-owned V2 export)\n", src.Name)
			summary.Skipped++
			continue
		}
		if opts.DryRun {
			fmt.Fprintf(opts.Out, "sync dashboard %s (uid %v)\n", src.Name, dash["uid"])
			summary.DashboardsSynced++
			continue
		}
		dash["id"] = nil
		body := map[string]any{
			"dashboard": dash,
			"overwrite": true,
			"folderUid": FolderUID,
			"message":   "frameworks cluster grafana sync",
		}
		if err := c.do(ctx, http.MethodPost, "/api/dashboards/db", body, nil); err != nil {
			return summary, fmt.Errorf("sync dashboard %s: %w", src.Name, err)
		}
		fmt.Fprintf(opts.Out, "synced dashboard %s (uid %v)\n", src.Name, dash["uid"])
		summary.DashboardsSynced++
	}
	return summary, nil
}

func (c client) ensureFolder(ctx context.Context, dryRun bool, out io.Writer) (bool, error) {
	found, err := c.get(ctx, "/api/folders/"+FolderUID, nil)
	if err != nil {
		return false, fmt.Errorf("resolve folder %q: %w", FolderUID, err)
	}
	if found {
		return false, nil
	}
	if dryRun {
		fmt.Fprintf(out, "create folder %q\n", FolderTitle)
		return true, nil
	}
	body := map[string]any{"uid": FolderUID, "title": FolderTitle}
	if err := c.do(ctx, http.MethodPost, "/api/folders", body, nil); err != nil {
		return false, fmt.Errorf("create folder %q: %w", FolderTitle, err)
	}
	fmt.Fprintf(out, "created folder %q\n", FolderTitle)
	return true, nil
}

// ensureDatasource converges the datasource with the given pinned uid:
// missing → create; present → update in place (URL/credentials drift heals on
// every sync). Datasources with other uids — operator-made — are untouched.
func (c client) ensureDatasource(ctx context.Context, uid string, payload map[string]any, dryRun bool, out io.Writer) (created, updated int, err error) {
	found, err := c.get(ctx, "/api/datasources/uid/"+uid, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve datasource %q: %w", uid, err)
	}
	if dryRun {
		if found {
			fmt.Fprintf(out, "update datasource %q\n", uid)
			return 0, 1, nil
		}
		fmt.Fprintf(out, "create datasource %q\n", uid)
		return 1, 0, nil
	}
	if found {
		// update-by-id was removed in Grafana 13; updates go by uid.
		if err := c.do(ctx, http.MethodPut, "/api/datasources/uid/"+uid, payload, nil); err != nil {
			return 0, 0, fmt.Errorf("update datasource %q: %w", uid, err)
		}
		fmt.Fprintf(out, "updated datasource %q\n", uid)
		return 0, 1, nil
	}
	if err := c.do(ctx, http.MethodPost, "/api/datasources", payload, nil); err != nil {
		return 0, 0, fmt.Errorf("create datasource %q: %w", uid, err)
	}
	fmt.Fprintf(out, "created datasource %q\n", uid)
	return 1, 0, nil
}

// get returns found=false on 404 instead of an error.
func (c client) get(ctx context.Context, path string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return false, err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		content, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return false, fmt.Errorf("GET %s returned %s and reading response body failed: %w", path, resp.Status, readErr)
		}
		return false, fmt.Errorf("GET %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(content)))
	}
	if out == nil {
		return true, nil
	}
	return true, json.NewDecoder(resp.Body).Decode(out)
}

func (c client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		content, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return fmt.Errorf("%s %s returned %s and reading response body failed: %w", method, path, resp.Status, readErr)
		}
		return fmt.Errorf("%s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(content)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c client) authorize(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		return
	}
	req.SetBasicAuth(c.username, c.password)
}
