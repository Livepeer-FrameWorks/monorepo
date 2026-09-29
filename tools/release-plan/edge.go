package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// edgeDecisionName is the manifest services[] name of the single-image edge
// container (frameworks-edge) and its key in PlanOutput.Decisions.
const edgeDecisionName = "edge"

// edgeHelmsmanComponent is the release component whose native linux tarball
// the edge image bakes.
const edgeHelmsmanComponent = "helmsman"

// EdgeImageVariant is one frameworks-edge profile and the platforms its image
// is built for. It mirrors the build-edge-image matrix in release.yml;
// TestEdgeImageVariantsMatchReleaseWorkflow keeps the two in lockstep.
type EdgeImageVariant struct {
	Profile   string
	Platforms []string
}

var edgeImageVariants = []EdgeImageVariant{
	{Profile: "cpu", Platforms: []string{"linux/amd64", "linux/arm64"}},
	{Profile: "cuda", Platforms: []string{"linux/amd64"}},
	{Profile: "tensorrt", Platforms: []string{"linux/amd64"}},
	{Profile: "openvino", Platforms: []string{"linux/amd64"}},
}

// MistReleaseIndex is the subset of the MistServer release index
// (mistserver-release-index.json, schema mistserver.release/v1) that decides
// what the edge image bakes: the release tag (MIST_VERSION), each variant's
// native tarball, and each accelerator variant's base image.
type MistReleaseIndex struct {
	Schema     string                        `json:"schema"`
	ReleaseTag string                        `json:"release_tag"`
	Profiles   map[string]MistReleaseProfile `json:"profiles"`
}

type MistReleaseProfile struct {
	Platforms map[string]MistReleasePlatform `json:"platforms"`
}

type MistReleasePlatform struct {
	Artifact MistReleaseArtifact `json:"artifact"`
	Image    *MistReleaseImage   `json:"image,omitempty"`
}

type MistReleaseArtifact struct {
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}

type MistReleaseImage struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// LoadMistReleaseIndex reads the index the resolve-mistserver job pinned.
func LoadMistReleaseIndex(path string) (*MistReleaseIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var idx MistReleaseIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	if idx.Schema != "mistserver.release/v1" {
		return nil, fmt.Errorf("%s: schema %q, want mistserver.release/v1", path, idx.Schema)
	}
	if strings.TrimSpace(idx.ReleaseTag) == "" {
		return nil, fmt.Errorf("%s: release_tag is empty", path)
	}
	return &idx, nil
}

// EdgeHashInputs is everything that ends up inside a frameworks-edge image.
type EdgeHashInputs struct {
	MonorepoRoot string
	// HelmsmanSourceHash is the helmsman decision's source_hash: the edge
	// image bakes that component's linux tarball.
	HelmsmanSourceHash string
	MistIndex          *MistReleaseIndex
	WorkflowSalt       string
}

// ComputeEdgeImageSourceHash fingerprints every frameworks-edge variant at once:
//
//   - every file under edge/ except the staged edge/dist/ output (Dockerfile
//     with its base-image default and S6 overlay version + checksums,
//     stage-dist.sh, rootfs/ including each file's executable bit);
//   - the helmsman source hash;
//   - the MistServer release tag and, per variant and platform, the native
//     tarball name + checksum and the accelerator base image ref + digest;
//   - the Caddy pin from config/infrastructure.yaml (version + linux
//     artifacts);
//   - the workflow salt, which covers the build-edge-image steps, the CPU base
//     image and the stage-dist arguments in release.yml.
//
// One hash covers all variants because the manifest records one source_hash
// per services[] entry and the variants share every input but Mist's
// per-profile artifacts, which come from the same Mist release.
func ComputeEdgeImageSourceHash(inputs EdgeHashInputs) (string, error) {
	if inputs.MistIndex == nil {
		return "", fmt.Errorf("mist release index required")
	}
	if strings.TrimSpace(inputs.HelmsmanSourceHash) == "" {
		return "", fmt.Errorf("helmsman source hash required")
	}
	h := sha256.New()
	mustWrite(h, []byte("edge-image-recipe:v1\n"))

	edgeDir := filepath.Join(inputs.MonorepoRoot, "edge")
	stagedDir := filepath.Join(edgeDir, "dist")
	var files []string
	if err := filepath.WalkDir(edgeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == stagedDir {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk %s: %w", edgeDir, err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("%s has no files", edgeDir)
	}
	sort.Strings(files)
	for _, path := range files {
		rel, err := filepath.Rel(inputs.MonorepoRoot, path)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return "", linkErr
			}
			content = []byte("symlink:" + target)
		} else {
			content, err = os.ReadFile(path)
			if err != nil {
				return "", err
			}
		}
		// Git and COPY keep only the executable bit; s6 run scripts need it.
		execBit := "0"
		if info.Mode().Perm()&0o111 != 0 {
			execBit = "1"
		}
		mustWrite(h, []byte("file:"), []byte(filepath.ToSlash(rel)), []byte(" exec:"+execBit+"\n"), content, []byte("\n"))
	}

	mustWrite(h, []byte("helmsman-source-hash:"), []byte(inputs.HelmsmanSourceHash), []byte("\n"))

	mist := inputs.MistIndex
	mustWrite(h, []byte("mist-release-tag:"), []byte(mist.ReleaseTag), []byte("\n"))
	for _, variant := range edgeImageVariants {
		profile, ok := mist.Profiles[variant.Profile]
		if !ok {
			return "", fmt.Errorf("mist release %s has no %s profile", mist.ReleaseTag, variant.Profile)
		}
		for _, platform := range variant.Platforms {
			entry, ok := profile.Platforms[platform]
			if !ok {
				return "", fmt.Errorf("mist release %s profile %s has no %s platform", mist.ReleaseTag, variant.Profile, platform)
			}
			if entry.Artifact.Name == "" || entry.Artifact.Checksum == "" {
				return "", fmt.Errorf("mist release %s profile %s %s artifact is not checksum-pinned", mist.ReleaseTag, variant.Profile, platform)
			}
			mustWrite(h, []byte("mist-artifact:"+variant.Profile+" "+platform+" "+entry.Artifact.Name+" "+entry.Artifact.Checksum+"\n"))
			if variant.Profile == "cpu" {
				continue
			}
			if entry.Image == nil || entry.Image.Ref == "" || entry.Image.Digest == "" {
				return "", fmt.Errorf("mist release %s profile %s %s has no digest-pinned base image", mist.ReleaseTag, variant.Profile, platform)
			}
			mustWrite(h, []byte("mist-image:"+variant.Profile+" "+platform+" "+entry.Image.Ref+"@"+entry.Image.Digest+"\n"))
		}
	}

	caddy, err := readEdgeCaddyPin(inputs.MonorepoRoot)
	if err != nil {
		return "", err
	}
	mustWrite(h, []byte("caddy:"), caddy, []byte("\n"))

	mustWrite(h, []byte("workflow-salt:"), []byte(inputs.WorkflowSalt), []byte("\n"))
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// readEdgeCaddyPin returns the canonical Caddy version + linux artifact pins
// the edge image bakes from config/infrastructure.yaml.
func readEdgeCaddyPin(monorepoRoot string) ([]byte, error) {
	path := filepath.Join(monorepoRoot, "config", "infrastructure.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Infrastructure []InfrastructureEntry `yaml:"infrastructure"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	for _, entry := range doc.Infrastructure {
		if entry.Name != "caddy" {
			continue
		}
		pin := struct {
			Version   string     `json:"version"`
			Artifacts []Artifact `json:"artifacts"`
		}{Version: entry.Version}
		for _, arch := range []string{"linux-amd64", "linux-arm64"} {
			var found *Artifact
			for i := range entry.Artifacts {
				if entry.Artifacts[i].Arch == arch {
					found = &entry.Artifacts[i]
					break
				}
			}
			if found == nil || found.URL == "" || found.Checksum == "" {
				return nil, fmt.Errorf("%s: caddy has no checksum-pinned %s artifact", path, arch)
			}
			pin.Artifacts = append(pin.Artifacts, Artifact{Arch: arch, URL: found.URL, Checksum: found.Checksum})
		}
		return json.Marshal(pin)
	}
	return nil, fmt.Errorf("%s: no caddy entry", path)
}

// decideForEdgeImage decides the frameworks-edge image as one unit. It can
// carry forward only when its hash matches the baseline's AND helmsman itself
// carries forward, so the baked helmsman bytes and HELMSMAN_VERSION are the
// ones the baseline image already contains.
func (p *Planner) decideForEdgeImage(decisions map[string]Decision, baseline *Manifest) (Decision, error) {
	if _, clash := decisions[edgeDecisionName]; clash {
		return Decision{}, fmt.Errorf("release component %q collides with the edge image decision", edgeDecisionName)
	}
	helmsman, ok := decisions[edgeHelmsmanComponent]
	if !ok {
		return Decision{}, fmt.Errorf("edge image bakes %s, but release-components has no %s component", edgeHelmsmanComponent, edgeHelmsmanComponent)
	}
	mistIndex, err := LoadMistReleaseIndex(p.MistIndexPath)
	if err != nil {
		return Decision{}, err
	}
	hash, err := ComputeEdgeImageSourceHash(EdgeHashInputs{
		MonorepoRoot:       p.MonorepoRoot,
		HelmsmanSourceHash: helmsman.SourceHash,
		MistIndex:          mistIndex,
		WorkflowSalt:       p.workflowSalt,
	})
	if err != nil {
		return Decision{}, err
	}

	d := Decision{
		Name:       edgeDecisionName,
		Kind:       KindService,
		Action:     ActionBuild,
		SourceHash: hash,
	}
	if baseline == nil {
		return d, nil
	}
	prior := findServiceInManifest(baseline, edgeDecisionName)
	if prior == nil {
		return d, nil
	}
	d.BaselineSourceHash = prior.SourceHash
	if prior.SourceHash == "" || prior.SourceHash != hash {
		return d, nil
	}
	if helmsman.Action != ActionCarryForward {
		return d, nil
	}
	if !edgeEntryCarriesEveryVariant(prior) {
		return d, nil
	}

	d.Action = ActionCarryForward
	d.BaselineTag = baseline.PlatformVersion
	carried := *prior
	d.CarriedService = &carried
	return d, nil
}

// edgeEntryCarriesEveryVariant reports whether a baseline edge entry pins
// every variant in both registries, which the retag and the manifest need.
func edgeEntryCarriesEveryVariant(entry *ServiceEntry) bool {
	if entry.ServiceVersion == "" || entry.Image == "" || entry.Digest == "" {
		return false
	}
	for _, variant := range edgeImageVariants {
		v, ok := entry.Variants[variant.Profile]
		if !ok || v.Image == "" || v.Digest == "" {
			return false
		}
		for _, registry := range []string{"dockerhub", "ghcr"} {
			img, ok := v.Images[registry]
			if !ok || img.Image == "" || img.Digest == "" {
				return false
			}
		}
	}
	return true
}
