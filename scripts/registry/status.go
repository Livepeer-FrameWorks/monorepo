package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Showcase is the marketing showcase copy for one registry row. Rows without
// it still appear in status.json for status lookups, but not on the showcase.
type Showcase struct {
	// Order sorts showcase items within their tier; unique across the registry.
	Order   int           `yaml:"order"`
	Title   string        `yaml:"title,omitempty"`
	Summary string        `yaml:"summary"`
	Bullets []string      `yaml:"bullets,omitempty"`
	Figure  string        `yaml:"figure,omitempty"`
	Link    *ShowcaseLink `yaml:"link,omitempty"`
}

// ShowcaseLink points at a marketing route (To) or a docs page (Docs), never both.
type ShowcaseLink struct {
	Label string `yaml:"label"`
	To    string `yaml:"to,omitempty"`
	Docs  string `yaml:"docs,omitempty"`
}

const (
	marketingStatusPath = "website_marketing/src/data/status.json"
	roadmapDocPath      = "website_docs/src/content/docs/roadmap.mdx"
	maxShowcaseBullets  = 4
)

// statusTier is one public horizon. The roadmap badge, the showcase group
// label, and the inline status tag are the same tier under three names.
type statusTier struct {
	Horizon      string `json:"horizon"`
	Label        string `json:"label"`
	Tag          string `json:"tag"`
	RoadmapBadge string `json:"roadmap_badge"`
}

var statusTiers = []statusTier{
	{Horizon: "now", Label: "Shipping today", Tag: "Shipping", RoadmapBadge: "Now"},
	{Horizon: "next", Label: "Up next", Tag: "Up next", RoadmapBadge: "Next"},
	{Horizon: "later", Label: "Further out", Tag: "Further out", RoadmapBadge: "Later"},
}

func tierFor(horizon string) (statusTier, bool) {
	for _, t := range statusTiers {
		if t.Horizon == horizon {
			return t, true
		}
	}
	return statusTier{}, false
}

// roadmapSection is a badged `###` heading in roadmap.mdx.
type roadmapSection struct {
	Title   string
	Horizon string
}

var roadmapHeadingRe = regexp.MustCompile(`(?m)^###\s+(.+?)\s*<Badge\s+text="(Now|Next|Later)"`)

// parseRoadmapSections maps each badged heading's anchor to its title and
// horizon. Anchors follow anchorSlug, which matches Starlight's heading ids
// (the Badge component does not contribute to the id).
func parseRoadmapSections(mdx string) map[string]roadmapSection {
	out := map[string]roadmapSection{}
	for _, m := range roadmapHeadingRe.FindAllStringSubmatch(mdx, -1) {
		out[anchorSlug(m[1])] = roadmapSection{Title: m[1], Horizon: strings.ToLower(m[2])}
	}
	return out
}

func loadRoadmapSections(repoRoot string) (map[string]roadmapSection, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(roadmapDocPath)))
	if err != nil {
		return nil, err
	}
	return parseRoadmapSections(string(data)), nil
}

// loadMarketingRoutes returns the marketing site paths that have a route
// module under website_marketing/app/routes ("/" is home.jsx).
func loadMarketingRoutes(repoRoot string) map[string]bool {
	routes := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(repoRoot, "website_marketing", "app", "routes"))
	if err != nil {
		return routes
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jsx")
		if e.IsDir() || !ok {
			continue
		}
		if name == "home" {
			routes["/"] = true
			continue
		}
		routes["/"+name] = true
	}
	return routes
}

// loadMarketingFigures returns the figure component names exported from the
// marketing figures kit, excluding its frame and defs helpers.
func loadMarketingFigures(repoRoot string) map[string]bool {
	figures := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(repoRoot, "website_marketing", "src", "components", "marketing", "figures"))
	if err != nil {
		return figures
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jsx")
		if e.IsDir() || !ok || name == "Figure" || name == "FigureDefs" {
			continue
		}
		figures[name] = true
	}
	return figures
}

// validateStatusFields checks horizon, maturity, note, roadmap, and showcase
// on every row, and that every badged roadmap section is claimed by a row.
// Shipped and partial rows default to horizon now.
func (v *validator) validateStatusFields(reg *Registry, sections map[string]roadmapSection, routes, figures map[string]bool) {
	claimed := map[string]bool{}
	orders := map[int]string{}
	check := func(f *Feature) {
		if f.Horizon == "" && (f.Status == "shipped" || f.Status == "partial") {
			f.Horizon = "now"
		}
		switch f.Horizon {
		case "", "now", "next", "later":
		default:
			v.errf("%s: invalid horizon %q (must be now|next|later)", f.Slug, f.Horizon)
		}
		switch f.Status {
		case "shipped", "partial":
			if f.Horizon != "now" {
				v.errf("%s: status=%s ships today, so horizon must be now (got %q)", f.Slug, f.Status, f.Horizon)
			}
		case "roadmap":
			if f.Horizon == "now" {
				v.errf("%s: status=roadmap cannot have horizon now", f.Slug)
			}
			if f.Audience == "product" && f.Kind == "product" {
				if f.Horizon == "" {
					v.errf("%s: product roadmap rows need horizon next or later", f.Slug)
				}
				if f.Roadmap == "" {
					v.errf("%s: product roadmap rows need a roadmap section anchor", f.Slug)
				}
			}
		case "gap":
			if f.Horizon != "" {
				v.errf("%s: status=gap is outside the product and cannot have a horizon", f.Slug)
			}
		}
		switch f.Maturity {
		case "":
		case "beta":
			if f.Status != "shipped" && f.Status != "partial" {
				v.errf("%s: maturity beta applies only to shipped or partial rows", f.Slug)
			}
		default:
			v.errf("%s: invalid maturity %q (must be beta or unset)", f.Slug, f.Maturity)
		}
		if f.Status == "partial" && f.Audience == "product" && strings.TrimSpace(f.Note) == "" {
			v.errf("%s: product rows with status=partial need a public note saying what is not there yet", f.Slug)
		}
		if f.Roadmap != "" {
			section, ok := sections[f.Roadmap]
			switch {
			case !ok:
				v.errf("%s: roadmap anchor %q is not a badged section in %s", f.Slug, f.Roadmap, roadmapDocPath)
			case f.Status == "roadmap" && f.Horizon != "" && section.Horizon != f.Horizon:
				v.errf("%s: horizon %s disagrees with the roadmap badge %q on %q", f.Slug, f.Horizon, section.Horizon, section.Title)
			}
			claimed[f.Roadmap] = true
		}
		if f.Showcase != nil {
			v.validateShowcase(f, routes, figures, orders)
		}
	}
	for i := range reg.Features {
		check(&reg.Features[i])
		for j := range reg.Features[i].Subitems {
			check(&reg.Features[i].Subitems[j])
		}
	}
	anchors := make([]string, 0, len(sections))
	for anchor := range sections {
		anchors = append(anchors, anchor)
	}
	sort.Strings(anchors)
	for _, anchor := range anchors {
		if !claimed[anchor] {
			v.errf("roadmap section %q (#%s) is claimed by no registry row; set roadmap: %s on the row it describes", sections[anchor].Title, anchor, anchor)
		}
	}
}

func (v *validator) validateShowcase(f *Feature, routes, figures map[string]bool, orders map[int]string) {
	s := f.Showcase
	if f.Audience != "product" {
		v.errf("%s: showcase applies only to product-audience rows", f.Slug)
	}
	if f.Horizon == "" {
		v.errf("%s: showcase rows need a horizon", f.Slug)
	}
	if s.Order <= 0 {
		v.errf("%s: showcase.order must be a positive integer", f.Slug)
	} else if other, dup := orders[s.Order]; dup {
		v.errf("%s: showcase.order %d is already used by %s", f.Slug, s.Order, other)
	} else {
		orders[s.Order] = f.Slug
	}
	if strings.TrimSpace(s.Summary) == "" {
		v.errf("%s: showcase.summary is required", f.Slug)
	}
	if len(s.Bullets) > maxShowcaseBullets {
		v.errf("%s: showcase has %d bullets (max %d)", f.Slug, len(s.Bullets), maxShowcaseBullets)
	}
	for _, b := range s.Bullets {
		if strings.TrimSpace(b) == "" {
			v.errf("%s: showcase has an empty bullet", f.Slug)
		}
	}
	if s.Figure != "" && !figures[s.Figure] {
		v.errf("%s: showcase.figure %q has no %s.jsx under website_marketing/src/components/marketing/figures/", f.Slug, s.Figure, s.Figure)
	}
	if l := s.Link; l != nil {
		if strings.TrimSpace(l.Label) == "" {
			v.errf("%s: showcase.link.label is required", f.Slug)
		}
		switch {
		case (l.To == "") == (l.Docs == ""):
			v.errf("%s: showcase.link needs exactly one of to (marketing route) or docs (docs page)", f.Slug)
		case l.To != "":
			if !routes[routePath(l.To)] {
				v.errf("%s: showcase.link.to %q has no route under website_marketing/app/routes/", f.Slug, l.To)
			}
		default:
			if !v.docsPageExists(l.Docs) {
				v.errf("%s: showcase.link.docs %q not found under website_docs/src/content/docs/", f.Slug, l.Docs)
			}
		}
	}
}

// routePath strips the query and fragment from a marketing link.
func routePath(to string) string {
	if i := strings.IndexAny(to, "?#"); i >= 0 {
		to = to[:i]
	}
	if to == "" {
		return "/"
	}
	return to
}

// docsURLPath turns a docs content path ("builders/go-live-browser.mdx") into
// its site path ("/builders/go-live-browser").
func docsURLPath(page string) string {
	p := strings.TrimSuffix(strings.TrimSuffix(page, ".mdx"), ".md")
	p = strings.TrimSuffix(p, "/index")
	if p == "index" {
		return "/"
	}
	return "/" + p
}

// statusDoc is the shape of website_marketing/src/data/status.json. Every
// key is always present; absent values are null so consumers never branch on
// a missing key.
type statusDoc struct {
	Source string `json:"source"`
	// Tiers lists the three horizons in display order with their labels.
	Tiers []statusTier `json:"tiers"`
	// Showcase lists, per horizon, the ids of rows with showcase copy in
	// showcase.order.
	Showcase map[string][]string `json:"showcase"`
	// Features holds every product-audience row that has a horizon, by id.
	Features map[string]statusFeature `json:"features"`
}

type statusFeature struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Status is the registry status: shipped, partial, or roadmap.
	Status string `json:"status"`
	// Availability is the docs matrix label: Available, Expanding, or Planned.
	Availability string `json:"availability"`
	// Maturity is "beta" or null.
	Maturity *string `json:"maturity"`
	Horizon  string  `json:"horizon"`
	// Tier is the showcase group label for the horizon.
	Tier string `json:"tier"`
	// Tag is the inline status badge: Shipping, Beta, Up next, or Further out.
	Tag      string          `json:"tag"`
	Note     *string         `json:"note"`
	Roadmap  *statusRoadmap  `json:"roadmap"`
	Showcase *statusShowcase `json:"showcase"`
}

type statusRoadmap struct {
	Anchor string `json:"anchor"`
	// Path is the docs-site path, e.g. /roadmap#drm.
	Path string `json:"path"`
}

type statusShowcase struct {
	Title   string      `json:"title"`
	Summary string      `json:"summary"`
	Bullets []string    `json:"bullets"`
	Figure  *string     `json:"figure"`
	Link    *statusLink `json:"link"`
}

type statusLink struct {
	Label string `json:"label"`
	// Site is "marketing" (Path is a marketing route) or "docs" (Path is a
	// docs-site path to prefix with the docs origin).
	Site string `json:"site"`
	Path string `json:"path"`
}

func optional(s string) *string {
	s = compactWhitespace(s)
	if s == "" {
		return nil
	}
	return &s
}

// renderStatusJSON builds status.json from an audience=product registry whose
// horizons have been defaulted by validateStatusFields.
func renderStatusJSON(reg Registry) ([]byte, error) {
	doc := statusDoc{
		Source:   "docs/platform-features.yaml (generated by make generate-feature-registry; do not edit)",
		Tiers:    statusTiers,
		Showcase: map[string][]string{},
		Features: map[string]statusFeature{},
	}
	type ranked struct {
		order int
		id    string
	}
	byTier := map[string][]ranked{}
	add := func(f Feature) {
		tier, ok := tierFor(f.Horizon)
		if !ok {
			return
		}
		tag := tier.Tag
		if f.Horizon == "now" && f.Maturity == "beta" {
			tag = "Beta"
		}
		sf := statusFeature{
			ID:           f.Slug,
			Title:        f.Name,
			Status:       f.Status,
			Availability: publicStatusLabel(f.Status),
			Maturity:     optional(f.Maturity),
			Horizon:      f.Horizon,
			Tier:         tier.Label,
			Tag:          tag,
			Note:         optional(f.Note),
		}
		if f.Roadmap != "" {
			sf.Roadmap = &statusRoadmap{Anchor: f.Roadmap, Path: "/roadmap#" + f.Roadmap}
		}
		if s := f.Showcase; s != nil {
			title := s.Title
			if title == "" {
				title = f.Name
			}
			bullets := make([]string, 0, len(s.Bullets))
			for _, b := range s.Bullets {
				bullets = append(bullets, compactWhitespace(b))
			}
			sc := &statusShowcase{Title: title, Summary: compactWhitespace(s.Summary), Bullets: bullets, Figure: optional(s.Figure)}
			if l := s.Link; l != nil {
				if l.To != "" {
					sc.Link = &statusLink{Label: l.Label, Site: "marketing", Path: l.To}
				} else {
					sc.Link = &statusLink{Label: l.Label, Site: "docs", Path: docsURLPath(l.Docs)}
				}
			}
			sf.Showcase = sc
			byTier[f.Horizon] = append(byTier[f.Horizon], ranked{order: s.Order, id: f.Slug})
		}
		doc.Features[f.Slug] = sf
	}
	for _, f := range reg.Features {
		add(f)
		for _, s := range f.Subitems {
			add(s)
		}
	}
	for _, t := range statusTiers {
		items := byTier[t.Horizon]
		sort.Slice(items, func(i, j int) bool { return items[i].order < items[j].order })
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.id)
		}
		doc.Showcase[t.Horizon] = ids
	}
	buf, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(buf, '\n'), nil
}

// Marketing files that import status.json are its consumers. In them, a
// status value or tier label written as a literal would drift from the
// registry, so the check rejects it; files that do not import the data yet
// are not checked.
var (
	marketingStatusImportRe  = regexp.MustCompile(`data/status\.json["']`)
	marketingStatusLiteralRe = regexp.MustCompile(`\bstatus\s*[:=]\s*\{?\s*["'](available|expanding|planned|now|next|later|beta|pipeline|shipped|partial|roadmap)["']`)
	// The tier labels plus the retired names they replaced.
	tierLabelLiterals = []string{"Shipping today", "Up next", "Further out", "In the pipeline", "After that", "On the roadmap"}
)

func (v *validator) checkMarketingStatusConsumers(repoRoot string) {
	root := filepath.Join(repoRoot, "website_marketing", "src")
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".jsx", ".ts", ".tsx":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, msg := range marketingStatusLiteralErrors(string(data)) {
			v.errf("%s: %s", relativeOrAbs(repoRoot, path), msg)
		}
		return nil
	})
	if walkErr != nil {
		v.errf("scan marketing sources for status literals: %v", walkErr)
	}
}

// marketingStatusLiteralErrors reports literal status values and tier labels
// in a source file that imports status.json.
func marketingStatusLiteralErrors(src string) []string {
	if !marketingStatusImportRe.MatchString(src) {
		return nil
	}
	var out []string
	for _, m := range marketingStatusLiteralRe.FindAllStringSubmatch(src, -1) {
		out = append(out, fmt.Sprintf("literal status %q; read the status from status.json", m[1]))
	}
	for _, label := range tierLabelLiterals {
		if strings.Contains(src, `"`+label+`"`) || strings.Contains(src, ">"+label+"<") {
			out = append(out, fmt.Sprintf("literal tier label %q; use tiers[].label or features[id].tag from status.json", label))
		}
	}
	return out
}
