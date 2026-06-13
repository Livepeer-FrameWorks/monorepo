package metabase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const managedMarkerPrefix = "frameworks:metabase-card"

type Spec struct {
	// Dashboard is the target dashboard for every card in this spec. The
	// --dashboard flag / DashboardID option override it.
	Dashboard string `yaml:"dashboard"`
	Cards     []Card `yaml:"cards"`
}

type Card struct {
	Slug     string `yaml:"slug"`
	Name     string `yaml:"name"`
	Display  string `yaml:"display"`
	Database string `yaml:"database"`
	Query    string `yaml:"query"`
}

type SyncOptions struct {
	BaseURL   string
	SessionID string
	APIKey    string
	// SpecPath reads the card spec from a file. SpecContent (e.g. a
	// CLI-embedded spec) takes precedence when both are set.
	SpecPath     string
	SpecContent  []byte
	Database     string
	DatabaseID   int
	Collection   string
	CollectionID int
	// Dashboard/DashboardID override the spec's own `dashboard:` field.
	Dashboard   string
	DashboardID int
	DryRun      bool
	Adopt       bool
	Force       bool
	HTTPClient  *http.Client
	Out         io.Writer
}

type SyncSummary struct {
	Created          int
	Updated          int
	Adopted          int
	Unchanged        int
	Conflicts        int
	AddedToDashboard int
}

type client struct {
	baseURL   string
	sessionID string
	apiKey    string
	http      *http.Client
}

type entity struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

type metabaseCard struct {
	ID           int          `json:"id"`
	Name         string       `json:"name"`
	Display      string       `json:"display"`
	Description  string       `json:"description"`
	CollectionID int          `json:"collection_id"`
	DatasetQuery datasetQuery `json:"dataset_query"`
}

type datasetQuery struct {
	Database int         `json:"database"`
	Type     string      `json:"type"`
	Native   nativeQuery `json:"native"`
}

type nativeQuery struct {
	Query        string         `json:"query"`
	TemplateTags map[string]any `json:"template-tags"`
}

type dashboard struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	Dashcards []dashboardCard `json:"dashcards"`
}

type dashboardCard struct {
	ID     int          `json:"id"`
	CardID int          `json:"card_id"`
	Card   metabaseCard `json:"card"`
}

func LoadSpec(path string) (Spec, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	return ParseSpec(content)
}

func ParseSpec(content []byte) (Spec, error) {
	var spec Spec
	if err := yaml.Unmarshal(content, &spec); err != nil {
		return Spec{}, err
	}
	for i, card := range spec.Cards {
		if strings.TrimSpace(card.Slug) == "" || strings.TrimSpace(card.Name) == "" || strings.TrimSpace(card.Query) == "" {
			return Spec{}, fmt.Errorf("card %d must set slug, name, and query", i+1)
		}
	}
	return spec, nil
}

func Sync(ctx context.Context, opts SyncOptions) (SyncSummary, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return SyncSummary{}, errors.New("metabase URL is required")
	}
	if strings.TrimSpace(opts.SessionID) == "" && strings.TrimSpace(opts.APIKey) == "" {
		return SyncSummary{}, errors.New("metabase session id or API key is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	var spec Spec
	var err error
	if len(opts.SpecContent) > 0 {
		spec, err = ParseSpec(opts.SpecContent)
	} else {
		spec, err = LoadSpec(opts.SpecPath)
	}
	if err != nil {
		return SyncSummary{}, err
	}
	if opts.Dashboard == "" && opts.DashboardID == 0 {
		opts.Dashboard = spec.Dashboard
	}

	c := client{
		baseURL:   strings.TrimRight(opts.BaseURL, "/"),
		sessionID: opts.SessionID,
		apiKey:    opts.APIKey,
		http:      opts.HTTPClient,
	}
	// The database connection is the only container that cannot be created
	// here: it carries engine credentials. Collections and dashboards are
	// plain named containers, so a missing one is created instead of failing.
	databaseID, err := c.resolveEntityID(ctx, "/api/database", opts.DatabaseID, opts.Database)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return SyncSummary{}, fmt.Errorf("resolve Metabase database: %w — create the connection in Metabase admin (Databases → Add database) or pass --database/--database-id", err)
		}
		return SyncSummary{}, fmt.Errorf("resolve Metabase database: %w", err)
	}
	collectionID, err := c.resolveEntityID(ctx, "/api/collection", opts.CollectionID, opts.Collection)
	if errors.Is(err, errNotFound) {
		if opts.DryRun {
			fmt.Fprintf(opts.Out, "create collection %q\n", opts.Collection)
			collectionID, err = 0, nil
		} else if collectionID, err = c.createCollection(ctx, opts.Collection); err == nil {
			fmt.Fprintf(opts.Out, "created collection %q\n", opts.Collection)
		}
	}
	if err != nil {
		return SyncSummary{}, fmt.Errorf("resolve Metabase collection: %w", err)
	}
	dashboardID, err := c.resolveEntityID(ctx, "/api/dashboard", opts.DashboardID, opts.Dashboard)
	if errors.Is(err, errNotFound) {
		if opts.DryRun {
			fmt.Fprintf(opts.Out, "create dashboard %q\n", opts.Dashboard)
			dashboardID, err = 0, nil
		} else if dashboardID, err = c.createDashboard(ctx, opts.Dashboard, collectionID); err == nil {
			fmt.Fprintf(opts.Out, "created dashboard %q\n", opts.Dashboard)
		}
	}
	if err != nil {
		return SyncSummary{}, fmt.Errorf("resolve Metabase dashboard: %w", err)
	}

	var summary SyncSummary
	var conflicts []string
	for _, card := range spec.Cards {
		remote, found, err := c.findCardByName(ctx, card.Name)
		if err != nil {
			return summary, err
		}
		hash := cardHash(card)
		if !found {
			if opts.DryRun {
				fmt.Fprintf(opts.Out, "create card %q\n", card.Name)
				summary.Created++
				continue
			}
			created, err := c.createCard(ctx, card, databaseID, collectionID, hash)
			if err != nil {
				return summary, err
			}
			remote = created
			summary.Created++
			fmt.Fprintf(opts.Out, "created card %q\n", card.Name)
		} else {
			remoteSlug, remoteHash, managed := managedMetadata(remote.Description)
			switch {
			case managed && remoteSlug == card.Slug && remoteHash == hash:
				summary.Unchanged++
			case managed && remoteSlug == card.Slug:
				if opts.DryRun {
					fmt.Fprintf(opts.Out, "update managed card %q\n", card.Name)
					summary.Updated++
				} else {
					if err := c.updateCard(ctx, remote, card, databaseID, collectionID, hash); err != nil {
						return summary, err
					}
					summary.Updated++
					fmt.Fprintf(opts.Out, "updated card %q\n", card.Name)
				}
			case opts.Adopt && equivalentSQL(remote.DatasetQuery.Native.Query, card.Query):
				if opts.DryRun {
					fmt.Fprintf(opts.Out, "adopt card %q\n", card.Name)
					summary.Adopted++
				} else {
					if err := c.updateCardDescription(ctx, remote, card.Slug, hash); err != nil {
						return summary, err
					}
					summary.Adopted++
					fmt.Fprintf(opts.Out, "adopted card %q\n", card.Name)
				}
			case opts.Force:
				if opts.DryRun {
					fmt.Fprintf(opts.Out, "force update unmanaged card %q\n", card.Name)
					summary.Updated++
				} else {
					if err := c.updateCard(ctx, remote, card, databaseID, collectionID, hash); err != nil {
						return summary, err
					}
					summary.Updated++
					fmt.Fprintf(opts.Out, "force-updated card %q\n", card.Name)
				}
			default:
				summary.Conflicts++
				conflicts = append(conflicts, fmt.Sprintf("%q exists but is not managed; use --adopt if the SQL matches or --force to replace it", card.Name))
				continue
			}
		}

		if dashboardID > 0 && remote.ID > 0 {
			added, err := c.ensureDashboardCard(ctx, dashboardID, remote.ID, opts.DryRun)
			if err != nil {
				return summary, err
			}
			if added {
				summary.AddedToDashboard++
				fmt.Fprintf(opts.Out, "added card %q to dashboard\n", card.Name)
			}
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return summary, fmt.Errorf("metabase managed-card conflicts:\n%s", strings.Join(conflicts, "\n"))
	}
	return summary, nil
}

var errNotFound = errors.New("not found")

func (c client) resolveEntityID(ctx context.Context, endpoint string, explicitID int, name string) (int, error) {
	if explicitID > 0 {
		return explicitID, nil
	}
	if strings.TrimSpace(name) == "" {
		return 0, nil
	}
	var raw any
	if err := c.do(ctx, http.MethodGet, endpoint, nil, &raw); err != nil {
		return 0, err
	}
	for _, item := range extractEntities(raw) {
		if item.Name == name {
			return item.ID, nil
		}
	}
	return 0, fmt.Errorf("%q %w", name, errNotFound)
}

func (c client) createCollection(ctx context.Context, name string) (int, error) {
	var created struct {
		ID int `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/collection", map[string]any{"name": name}, &created); err != nil {
		return 0, fmt.Errorf("create collection %q: %w", name, err)
	}
	return created.ID, nil
}

func (c client) createDashboard(ctx context.Context, name string, collectionID int) (int, error) {
	body := map[string]any{"name": name}
	if collectionID > 0 {
		body["collection_id"] = collectionID
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/dashboard", body, &created); err != nil {
		return 0, fmt.Errorf("create dashboard %q: %w", name, err)
	}
	return created.ID, nil
}

func (c client) findCardByName(ctx context.Context, name string) (metabaseCard, bool, error) {
	var raw any
	path := "/api/search?models=card&q=" + url.QueryEscape(name)
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return metabaseCard{}, false, err
	}
	for _, item := range extractEntities(raw) {
		if item.Name != name || (item.Model != "" && item.Model != "card") {
			continue
		}
		card, err := c.getCard(ctx, item.ID)
		return card, err == nil, err
	}
	return metabaseCard{}, false, nil
}

func (c client) getCard(ctx context.Context, id int) (metabaseCard, error) {
	var card metabaseCard
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/card/%d", id), nil, &card); err != nil {
		return metabaseCard{}, err
	}
	return card, nil
}

func (c client) createCard(ctx context.Context, card Card, databaseID, collectionID int, hash string) (metabaseCard, error) {
	var created metabaseCard
	if err := c.do(ctx, http.MethodPost, "/api/card", cardPayload(card, databaseID, collectionID, "", hash), &created); err != nil {
		return metabaseCard{}, err
	}
	return created, nil
}

func (c client) updateCard(ctx context.Context, remote metabaseCard, card Card, databaseID, collectionID int, hash string) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/card/%d", remote.ID), cardPayload(card, databaseID, collectionID, remote.Description, hash), nil)
}

func (c client) updateCardDescription(ctx context.Context, remote metabaseCard, slug, hash string) error {
	body := map[string]any{
		"description": managedDescription(remote.Description, slug, hash),
	}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/card/%d", remote.ID), body, nil)
}

func (c client) ensureDashboardCard(ctx context.Context, dashboardID, cardID int, dryRun bool) (bool, error) {
	// Dashcards are read and written as raw maps: PUT /api/dashboard/:id
	// replaces the whole dashcards array (POST /api/dashboard/:id/cards was
	// removed in Metabase 49), so existing entries are sent back exactly as
	// fetched to preserve their layout.
	var dash map[string]any
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/dashboard/%d", dashboardID), nil, &dash); err != nil {
		return false, err
	}
	var dashcards []any
	if list, ok := dash["dashcards"].([]any); ok {
		dashcards = list
	}
	nextRow := 0.0
	for _, raw := range dashcards {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if existing, ok := entry["card_id"].(float64); ok && int(existing) == cardID {
			return false, nil
		}
		var row, sizeY float64
		if v, ok := entry["row"].(float64); ok {
			row = v
		}
		if v, ok := entry["size_y"].(float64); ok {
			sizeY = v
		}
		if row+sizeY > nextRow {
			nextRow = row + sizeY
		}
	}
	if dryRun {
		return true, nil
	}
	// Negative id = "create this dashcard"; appended below the existing grid
	// at half width (24-column grid).
	body := map[string]any{"dashcards": append(dashcards, map[string]any{
		"id":      -1,
		"card_id": cardID,
		"row":     int(nextRow),
		"col":     0,
		"size_x":  12,
		"size_y":  6,
	})}
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/dashboard/%d", dashboardID), body, nil); err != nil {
		return false, err
	}
	return true, nil
}

func (c client) do(ctx context.Context, method, path string, body any, out any) error {
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
	if c.sessionID != "" {
		req.Header.Set("X-Metabase-Session", c.sessionID)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
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

func cardPayload(card Card, databaseID, collectionID int, existingDescription, hash string) map[string]any {
	return map[string]any{
		"name":          card.Name,
		"display":       card.Display,
		"description":   managedDescription(existingDescription, card.Slug, hash),
		"collection_id": collectionID,
		"dataset_query": datasetQuery{
			Database: databaseID,
			Type:     "native",
			Native: nativeQuery{
				Query:        strings.TrimSpace(card.Query),
				TemplateTags: map[string]any{},
			},
		},
		"visualization_settings": map[string]any{},
	}
}

func cardHash(card Card) string {
	payload := struct {
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		Display string `json:"display"`
		Query   string `json:"query"`
	}{
		Slug:    card.Slug,
		Name:    card.Name,
		Display: card.Display,
		Query:   strings.TrimSpace(card.Query),
	}
	content, err := json.Marshal(payload)
	if err != nil {
		fallback := sha256.Sum256([]byte(card.Slug + "\x00" + card.Name + "\x00" + card.Display + "\x00" + strings.TrimSpace(card.Query)))
		return "sha256:" + hex.EncodeToString(fallback[:])
	}
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func managedDescription(existing, slug, hash string) string {
	existing = managedMarkerRe.ReplaceAllString(existing, "")
	existing = strings.TrimSpace(existing)
	marker := fmt.Sprintf("<!-- %s {\"slug\":%q,\"hash\":%q} -->", managedMarkerPrefix, slug, hash)
	if existing == "" {
		return marker
	}
	return existing + "\n\n" + marker
}

func managedMetadata(description string) (slug, hash string, ok bool) {
	match := managedMarkerRe.FindStringSubmatch(description)
	if match == nil {
		return "", "", false
	}
	var payload struct {
		Slug string `json:"slug"`
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal([]byte(match[1]), &payload); err != nil {
		return "", "", false
	}
	return payload.Slug, payload.Hash, payload.Slug != "" && payload.Hash != ""
}

var managedMarkerRe = regexp.MustCompile(`(?s)<!--\s*` + regexp.QuoteMeta(managedMarkerPrefix) + `\s+({.*?})\s*-->`)

func equivalentSQL(left, right string) bool {
	return strings.Join(strings.Fields(left), " ") == strings.Join(strings.Fields(right), " ")
}

func extractEntities(raw any) []entity {
	var entities []entity
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			id, idOK := typed["id"].(float64)
			name, nameOK := typed["name"].(string)
			if idOK && nameOK {
				model := ""
				if rawModel, ok := typed["model"].(string); ok {
					model = rawModel
				}
				entities = append(entities, entity{ID: int(id), Name: name, Model: model})
				return
			}
			for key, child := range typed {
				switch key {
				case "data", "results", "collections", "dashboards":
					walk(child)
				}
			}
		}
	}
	walk(raw)
	return entities
}
