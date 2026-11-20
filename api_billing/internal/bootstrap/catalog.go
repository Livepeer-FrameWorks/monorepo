// Package bootstrap holds Purser's reconcilers for the bootstrap-desired-state
// schema (see docs/architecture/bootstrap-desired-state.md). Both `purser bootstrap`
// and the gRPC handler delegate to these reconcilers so there is one source of truth
// per Purser-owned table.
package bootstrap

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/transcode/contract"
	"gopkg.in/yaml.v3"
)

//go:embed catalog/billing_tiers.yaml
var embeddedBillingTiersYAML []byte

// CatalogPricingRule is one priced behavior for a meter on a tier. Fields map
// 1:1 to columns in purser.tier_pricing_rules.
type CatalogPricingRule struct {
	Meter            string         `yaml:"meter"`
	Model            string         `yaml:"model"`
	Currency         string         `yaml:"currency,omitempty"`
	IncludedQuantity float64        `yaml:"included_quantity,omitempty"`
	UnitPrice        string         `yaml:"unit_price"` // string for decimal precision
	Config           map[string]any `yaml:"config,omitempty"`
}

// CatalogTier is the embedded-catalog row that ReconcileBillingTierCatalog upserts
// into purser.billing_tiers (+ tier_entitlements + tier_pricing_rules). Field
// shapes match the SQL columns 1:1 where applicable; the entitlement map and
// rule slice are flattened into normalized rows by the reconciler.
type CatalogTier struct {
	TierName          string               `yaml:"tier_name"`
	DisplayName       string               `yaml:"display_name"`
	Description       string               `yaml:"description"`
	BasePrice         float64              `yaml:"base_price"`
	Currency          string               `yaml:"currency"`
	BillingPeriod     string               `yaml:"billing_period"`
	Features          map[string]any       `yaml:"features"`
	SupportLevel      string               `yaml:"support_level"`
	SLALevel          string               `yaml:"sla_level"`
	MeteringEnabled   bool                 `yaml:"metering_enabled"`
	Entitlements      map[string]any       `yaml:"entitlements"`
	PricingRules      []CatalogPricingRule `yaml:"pricing_rules"`
	TierLevel         int                  `yaml:"tier_level"`
	IsEnterprise      bool                 `yaml:"is_enterprise"`
	IsDefaultPrepaid  bool                 `yaml:"is_default_prepaid"`
	IsDefaultPostpaid bool                 `yaml:"is_default_postpaid"`
	// Process fields arrive as YAML strings carrying raw JSON.
	// MistServer reads them verbatim, so we keep them as strings end-to-end.
	ProcessesLive        string `yaml:"processes_live"`
	ProcessesDVR         string `yaml:"processes_dvr"`
	ProcessesClip        string `yaml:"processes_clip"`
	ProcessesDVRFinalize string `yaml:"processes_dvr_finalize"`
	ProcessesVOD         string `yaml:"processes_vod"`
	// AllowedTranscodeLadderNames names the catalog's transcode_ladders the
	// tier allows; loading resolves them into TranscodeLadders.
	AllowedTranscodeLadderNames []string `yaml:"allowed_transcode_ladders"`
	// TranscodeLadders are the ladders the tier allows, in catalog order, and
	// DefaultTranscodeLadder the one whose renditions its Livepeer entries carry.
	TranscodeLadders       []CatalogTranscodeLadder `yaml:"-"`
	DefaultTranscodeLadder string                   `yaml:"default_transcode_ladder"`
}

// CatalogTranscodeLadder is a named set of video renditions for the video
// transcode process. Its JSON shape is what purser.billing_tiers.transcode_ladders holds.
type CatalogTranscodeLadder struct {
	Name        string                   `yaml:"name" json:"name"`
	DisplayName string                   `yaml:"display_name" json:"display_name"`
	Description string                   `yaml:"description" json:"description"`
	Renditions  []contract.RenditionSpec `yaml:"-" json:"renditions"`
}

// catalogTranscodeLadder is a transcode_ladders entry as the YAML writes it:
// renditions use the Mist process option names (track_inhibit), which the
// contract type carries as JSON tags only.
type catalogTranscodeLadder struct {
	CatalogTranscodeLadder `yaml:",inline"`
	Renditions             []map[string]any `yaml:"renditions"`
}

// catalog is the parsed shape of catalog/billing_tiers.yaml.
type catalog struct {
	TranscodeLadders []catalogTranscodeLadder `yaml:"transcode_ladders"`
	Tiers            []CatalogTier            `yaml:"tiers"`
}

// EmbeddedTiers parses the binary-embedded catalog. Production callers typically
// use this to seed ReconcileBillingTierCatalog; tests can replace tiers via
// ReconcileBillingTiersWith.
func EmbeddedTiers() ([]CatalogTier, error) {
	var c catalog
	if err := yaml.Unmarshal(embeddedBillingTiersYAML, &c); err != nil {
		return nil, fmt.Errorf("parse embedded billing tier catalog: %w", err)
	}
	if len(c.Tiers) == 0 {
		return nil, fmt.Errorf("embedded billing tier catalog has zero tiers")
	}
	for i, t := range c.Tiers {
		if t.TierName == "" {
			return nil, fmt.Errorf("embedded catalog tier[%d] missing tier_name", i)
		}
	}
	if err := composeTranscodeLadders(c.TranscodeLadders, c.Tiers); err != nil {
		return nil, fmt.Errorf("embedded catalog: %w", err)
	}
	if err := validateCatalogTiers(c.Tiers); err != nil {
		return nil, fmt.Errorf("embedded catalog: %w", err)
	}
	return c.Tiers, nil
}

func validateCatalogTiers(tiers []CatalogTier) error {
	if err := validateCatalogDNSEntitlements(tiers); err != nil {
		return err
	}
	if err := validateCatalogTranscodeLadders(tiers); err != nil {
		return err
	}
	return validateCatalogFeatures(tiers)
}

var transcodeLadderName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// composeTranscodeLadders resolves each tier's allowed ladder names against the
// catalog's ladders and fills the default ladder's renditions into the tier's
// Livepeer entries, which the catalog writes without renditions.
func composeTranscodeLadders(defined []catalogTranscodeLadder, tiers []CatalogTier) error {
	byName := make(map[string]CatalogTranscodeLadder, len(defined))
	for _, entry := range defined {
		ladder := entry.CatalogTranscodeLadder
		raw, err := json.Marshal(entry.Renditions)
		if err != nil {
			return fmt.Errorf("transcode ladder %q renditions: %w", ladder.Name, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&ladder.Renditions); err != nil {
			return fmt.Errorf("transcode ladder %q renditions: %w", ladder.Name, err)
		}
		if !transcodeLadderName.MatchString(ladder.Name) {
			return fmt.Errorf("invalid transcode ladder name %q", ladder.Name)
		}
		if err := contract.ValidatePolicy(ladder.Renditions); err != nil {
			return fmt.Errorf("transcode ladder %q: %w", ladder.Name, err)
		}
		if _, dup := byName[ladder.Name]; dup {
			return fmt.Errorf("transcode ladder %q is defined twice", ladder.Name)
		}
		byName[ladder.Name] = ladder
	}
	for i := range tiers {
		tier := &tiers[i]
		tier.TranscodeLadders = nil
		for _, name := range tier.AllowedTranscodeLadderNames {
			ladder, ok := byName[name]
			if !ok {
				return fmt.Errorf("tier %q allows undefined transcode ladder %q", tier.TierName, name)
			}
			tier.TranscodeLadders = append(tier.TranscodeLadders, ladder)
		}
		ladder, ok := tier.TranscodeLadder(tier.DefaultTranscodeLadder)
		if !ok {
			continue
		}
		for _, field := range tier.processFields() {
			if hasTranscodeRenditions(*field) {
				return fmt.Errorf("tier %q: a Livepeer entry takes its renditions from the default transcode ladder, not its own", tier.TierName)
			}
			composed, err := mist.SetTranscodeLadder(processOrEmpty(*field), ladder.Renditions)
			if err != nil {
				return fmt.Errorf("tier %q: %w", tier.TierName, err)
			}
			if *field != "" {
				*field = composed
			}
		}
	}
	return nil
}

// validateCatalogTranscodeLadders checks each tier's ladders: valid, uniquely
// named, a default among them whenever there are any, and every Livepeer entry
// carrying exactly the default ladder's renditions.
func validateCatalogTranscodeLadders(tiers []CatalogTier) error {
	for _, tier := range tiers {
		seen := map[string]bool{}
		for _, ladder := range tier.TranscodeLadders {
			if !transcodeLadderName.MatchString(ladder.Name) || seen[ladder.Name] {
				return fmt.Errorf("tier %q transcode ladder name %q is invalid or not unique", tier.TierName, ladder.Name)
			}
			seen[ladder.Name] = true
			if err := contract.ValidatePolicy(ladder.Renditions); err != nil {
				return fmt.Errorf("tier %q transcode ladder %q: %w", tier.TierName, ladder.Name, err)
			}
		}
		if (len(tier.TranscodeLadders) > 0 || tier.DefaultTranscodeLadder != "") && !seen[tier.DefaultTranscodeLadder] {
			return fmt.Errorf("tier %q default transcode ladder %q is not one it allows", tier.TierName, tier.DefaultTranscodeLadder)
		}
		defaultLadder, hasDefault := tier.TranscodeLadder(tier.DefaultTranscodeLadder)
		for _, field := range tier.processFields() {
			renditions, hasTranscode, err := transcodeEntryRenditions(*field)
			if err != nil {
				return fmt.Errorf("tier %q: %w", tier.TierName, err)
			}
			if !hasTranscode {
				continue
			}
			if !hasDefault {
				return fmt.Errorf("tier %q has a Livepeer entry but no default transcode ladder", tier.TierName)
			}
			if !catalogProfilesEqual(renditions, mist.LadderProfiles(defaultLadder.Renditions)) {
				return fmt.Errorf("tier %q Livepeer profiles differ from its default transcode ladder %q", tier.TierName, defaultLadder.Name)
			}
		}
	}
	return nil
}

// TranscodeLadder returns the tier's allowed ladder of that name.
func (t CatalogTier) TranscodeLadder(name string) (CatalogTranscodeLadder, bool) {
	for _, ladder := range t.TranscodeLadders {
		if ladder.Name == name {
			return ladder, true
		}
	}
	return CatalogTranscodeLadder{}, false
}

func (t *CatalogTier) processFields() []*string {
	return []*string{&t.ProcessesLive, &t.ProcessesDVR, &t.ProcessesClip, &t.ProcessesDVRFinalize, &t.ProcessesVOD}
}

func hasTranscodeRenditions(processesJSON string) bool {
	var processes []map[string]json.RawMessage
	if json.Unmarshal([]byte(processOrEmpty(processesJSON)), &processes) != nil {
		return false
	}
	for _, proc := range processes {
		var name string
		if json.Unmarshal(proc["process"], &name) == nil && name == "Livepeer" {
			if _, ok := proc["target_profiles"]; ok {
				return true
			}
		}
	}
	return false
}

// transcodeEntryRenditions reads the profiles every Livepeer entry carries;
// entries that disagree cannot share one named ladder.
func transcodeEntryRenditions(processesJSON string) ([]mist.LivepeerJSONProfile, bool, error) {
	var processes []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(processOrEmpty(processesJSON)), &processes); err != nil {
		return nil, false, err
	}
	var out []mist.LivepeerJSONProfile
	found := false
	for _, proc := range processes {
		var name string
		if json.Unmarshal(proc["process"], &name) != nil || name != "Livepeer" {
			continue
		}
		var profiles []mist.LivepeerJSONProfile
		if err := json.Unmarshal(proc["target_profiles"], &profiles); err != nil || len(profiles) == 0 {
			return nil, false, fmt.Errorf("invalid Livepeer target_profiles")
		}
		if found && !catalogProfilesEqual(out, profiles) {
			return nil, false, fmt.Errorf("livepeer entries carry different profiles")
		}
		out, found = profiles, true
	}
	return out, found, nil
}

func catalogProfilesEqual(a, b []mist.LivepeerJSONProfile) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && jsonEq(string(left), right)
}

// catalogFeatureKeys are the JSON keys of models.BillingFeatures. Purser drops
// any other key when it reads features, so the catalog refuses to store one.
var catalogFeatureKeys = map[string]bool{
	"support_level":           true,
	"sla":                     true,
	"processing_customizable": true,
}

func validateCatalogFeatures(tiers []CatalogTier) error {
	for _, tier := range tiers {
		for key := range tier.Features {
			if !catalogFeatureKeys[key] {
				return fmt.Errorf("tier %q feature %q is not a billing feature (allowed: support_level, sla, processing_customizable)", tier.TierName, key)
			}
		}
	}
	return nil
}

func validateCatalogDNSEntitlements(tiers []CatalogTier) error {
	for _, tier := range tiers {
		for _, key := range []string{"custom_subdomain_enabled", "custom_domain_enabled"} {
			value, ok := tier.Entitlements[key]
			if !ok {
				return fmt.Errorf("tier %q missing required %s entitlement", tier.TierName, key)
			}
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("tier %q entitlement %s must be boolean", tier.TierName, key)
			}
		}
	}
	return nil
}

// jsonBytes marshals an any-typed JSONB value into `[]byte` for jsonEq
// comparisons. nil maps become a literal `{}` to match the column DEFAULT.
// Generated repository parameters accept the result as json.RawMessage so the
// driver binds JSONB rather than treating an untyped byte slice as bytea.
func jsonBytes(v map[string]any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}
