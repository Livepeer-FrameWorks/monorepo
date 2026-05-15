# Build & Packaging - Multi-Platform Release Pipeline

FrameWorks releases Linux Docker images, Linux service binaries, a signed/notarized macOS CLI, signed/notarized macOS service binaries where supported, a macOS tray app, and a macOS `.pkg` installer.

Current target set:

- Docker images: `linux/amd64`, `linux/arm64`
- CLI: `linux/amd64`, `linux/arm64`, `darwin/arm64`
- Service binaries: `linux/amd64`, `linux/arm64`, plus `darwin/arm64` for services marked `darwin_binary=true` in `.github/release-components.json`

Linux amd64 jobs run on GitHub-hosted `ubuntu-latest`. Linux arm64 image and service-binary jobs run on GitHub-hosted `ubuntu-24.04-arm`. macOS signing/notarization jobs run on the self-hosted `macos-arm64-self-hosted` runner. `darwin/amd64` is not produced.

## Architecture

```
                         release.yml
                              │
        ┌─────────────────────┼──────────────────────┐
        ▼                     ▼                      ▼
  ubuntu-latest        ubuntu-24.04-arm      macos-arm64-self-hosted
  ┌──────────────┐     ┌──────────────┐      ┌────────────────────┐
  │ Docker amd64 │     │ Docker arm64 │      │ CLI darwin/arm64   │
  │ svc linux/64 │     │ svc linux/64 │      │ svc darwin/arm64   │
  │ CLI linux    │     │ webapps arm64│      │ tray app, .pkg     │
  │ webapps amd64│     └──────────────┘      │ codesign/notarize  │
  └──────────────┘                           └────────────────────┘
                              │
                     ┌────────┴────────┐
                     ▼                 ▼
                GitHub Release     Homebrew tap
```

## Runner Setup

**Self-hosted runner:** `macos-arm64-self-hosted`.

Installed software:

- Go, Homebrew, Xcode/XcodeGen, and packaging/notarization tooling
- Docker Desktop for any local Mac-runner container work

Signing certificates are not expected to be preinstalled on disk. Release jobs import them into a temporary keychain from GitHub Secrets via `scripts/ci/setup-signing-keychain.sh` and clean up with `scripts/ci/cleanup-signing-keychain.sh`.

## Build Matrix

### Go Services (`release.yml`)

| Target         | Runner                  | CGO      | Notes                                   |
| -------------- | ----------------------- | -------- | --------------------------------------- |
| `linux/amd64`  | ubuntu-latest           | zig musl | All services                            |
| `linux/arm64`  | ubuntu-24.04-arm        | zig musl | All services, native ARM                |
| `darwin/arm64` | macos-arm64-self-hosted | No       | Services with `darwin_binary=true` only |

### External Media Dependencies

The monorepo release manifest can include external MistServer and Livepeer release assets, but
their build workflows live outside this repository. Treat those upstream workflows as the source
of truth for their runner and compiler details.

### Docker Images

Service and webapp Docker images are built per arch, then `merge-image-manifests` and `merge-webapp-manifests` assemble multi-arch tags for GHCR and Docker Hub.

## Code Signing & Notarization

All darwin binaries and the .pkg are signed and notarized on the Mac runner.

**Binary signing:**

```
codesign --sign "$APPLE_DEVELOPER_ID" --timestamp --options runtime <binary>
```

**Notarization:**

```
xcrun notarytool submit <zip> --apple-id ... --team-id ... --password ... --wait
xcrun stapler staple <artifact>
```

**.pkg signing** uses a separate Developer ID Installer certificate:

```
productsign --sign "$APPLE_INSTALLER_ID" unsigned.pkg signed.pkg
```

### Secrets

| Secret                        | Purpose                             |
| ----------------------------- | ----------------------------------- |
| `APPLE_CERTIFICATE_P12`       | Developer ID Application cert       |
| `APPLE_INSTALLER_P12`         | Developer ID Installer cert         |
| `APPLE_CERTIFICATE_PASSWORD`  | Application cert P12 password       |
| `APPLE_INSTALLER_PASSWORD`    | Installer cert P12 password         |
| `APPLE_DEVELOPER_ID`          | Application signing identity string |
| `APPLE_INSTALLER_ID`          | Installer signing identity string   |
| `APPLE_ID`                    | Apple ID email (notarization)       |
| `APPLE_APP_SPECIFIC_PASSWORD` | Notarytool password                 |
| `APPLE_TEAM_ID`               | Developer Team ID                   |

## Distribution Channels

### GitHub Release

Every tagged release publishes:

- `frameworks-cli-v*-{linux-amd64,linux-arm64}.tar.gz`
- `frameworks-cli-v*-darwin-arm64.zip` (primary macOS CLI artifact)
- `frameworks-{service}-v*-{linux-amd64,linux-arm64}.tar.gz`
- `frameworks-{service}-v*-darwin-arm64.zip` for services marked `darwin_binary=true`
- `frameworks-v*.pkg` (macOS installer: CLI + tray app)
- `manifest.yaml` (machine-readable release metadata)
- service and webapp Docker images pushed to GHCR and Docker Hub

### .pkg Installer (`scripts/macos-pkg/`)

Bundles CLI binary (`/usr/local/bin/frameworks`) and tray app (`/Applications/FrameWorks.app`). Built with `pkgbuild` → `productbuild` → `productsign`. Requires macOS 14.0+.

The .pkg does NOT install the edge stack. Edge provisioning is done through the CLI.

### Homebrew Tap (`Livepeer-FrameWorks/homebrew-tap`)

```
brew tap livepeer-frameworks/tap
brew install frameworks-cli          # CLI (macOS + Linux)
brew install --cask frameworks       # Tray app (macOS only)
```

Auto-bumped on each release by `scripts/bump.sh` in the tap repo, triggered from the monorepo's manifest job using `GITOPS_APP_ID` for cross-repo auth.

### Install Script

`scripts/install.sh` — curl-pipe-sh installer for CLI binary. Detects OS and arch and downloads the packaged release asset (`.zip` on macOS, `.tar.gz` on Linux).

## Deployment Identity Model

A service instance on a node is a `(deploy_mode, artifact_identity)` tuple. `deploy_mode` is `docker` or `native` and is selectable per service per node — a single cluster mixes both freely.

- **Docker mode identity** is `image@sha256:<digest>`. The OCI manifest digest is the source of truth; the tag string is cosmetic.
- **Native mode identity** is `(url, sha256:<checksum>)`. The tarball SHA is what `ansible.builtin.get_url` validates and what Helmsman compares against when applying agent-pulled updates.
- **`platform_version`** (e.g. `v0.2.40`) is the umbrella tag for the release.
- **`service_version`** in `manifest.services[*]` is **artefact provenance**: which release actually produced this component's bytes. It usually equals `platform_version`, but for carried-forward components it stays at the baseline tag — see the next section.

Mixed fleets mean an "unchanged" component is a per-mode statement: `target_identity == installed_identity` for the mode the node runs. Not "same version label."

## Carry-Forward (Source-Hash Decided)

A release that touches one service does not need to rebuild seventeen. The `tools/release-plan/` Go program runs before the build matrix, computes a deterministic source-hash per component, looks up the baseline release's recorded hash, and emits a per-component decision: `build` or `carry_forward`.

### Source-hash recipe (per Go service)

The hash inputs match `tools/release-plan/hash.go`:

| Input                                                                                                                   | Why                                                                                 |
| ----------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Files in the import closure of `release-components.json:.cmd` for the service, restricted to monorepo-internal packages | The actual source compiled into the binary                                          |
| `<service>/go.mod` + `go.sum`                                                                                           | Third-party dependency pins                                                         |
| `pkg/go.mod` + `pkg/go.sum`                                                                                             | Required because of `replace github.com/Livepeer-FrameWorks/monorepo/pkg => ../pkg` |
| `<service>/Dockerfile`                                                                                                  | Image build is a function of the Dockerfile too                                     |
| The component's `release-components.json` entry (cgo, darwin_binary flags)                                              | Build flags affect output                                                           |
| `.go-version` (Go toolchain)                                                                                            | Toolchain bumps invalidate everything                                               |
| Workflow salt: `sha256(release.yml + tools/release-plan/*.go excluding *_test.go)`                                      | CI build-logic changes force a full rebuild                                         |

Test files (`*_test.go`) are excluded — they don't affect the binary. The import closure comes from `go list -deps -json <cmd>`, run from the service's module root with a canonical Linux/amd64 environment. CGO is enabled only for components marked `cgo: true` in `.github/release-components.json`.

### Baseline resolution (track-aware)

Releases live on two tracks: `stable` (e.g. `v0.2.39`) and `rc` (e.g. `v0.2.40-rc1`). The baseline lookup respects the new tag's track:

1. **stable → stable**: most recent stable strictly earlier than the new tag.
2. **rc → rc**: most recent rc strictly earlier than the new tag.
3. **rc → stable promotion** (`v0.2.40` after `v0.2.40-rc3`): baseline is the most recent rc with the same major.minor.patch. A no-op promotion skips the entire build matrix.
4. **stable → rc** (first rc on a new MMR): falls back to the most recent stable. Source-hash comparisons catch actually-changed components.

`tools/release-plan/baseline.go` implements these rules and records each step in the output's `baseline_lineage` so operators can see why a particular release was chosen.

### Atomic BOM carry-forward

When a component carries forward, the new release manifest copies the baseline's **entire component BOM** verbatim — both Docker and native identity blocks together:

```yaml
services:
  - name: helmsman
    service_version: v0.2.37 # preserved from baseline, NOT restamped to platform tag
    image: ghcr.io/.../frameworks-helmsman:v0.2.37
    digest: sha256:<v0.2.37 digest>
    images:
      dockerhub: { image: ..., digest: ... }
      ghcr: { image: ..., digest: ... }
    source_hash: sha256:<unchanged>
    carried_from: v0.2.37
native_binaries:
  - name: helmsman
    source_hash: sha256:<unchanged>
    carried_from: v0.2.37
    artifacts:
      - { arch: linux-amd64, url: <v0.2.37 url>, checksum: sha256:<v0.2.37> }
      - { arch: linux-arm64, ... }
      - { arch: darwin-arm64, ... }
```

This is the key invariant for mixed Docker/native fleets: either mode resolves to a content-addressed pointer in the new manifest without rebuilding anything. Preserving `service_version` is what keeps Foghorn's edge release reconciler from rolling every edge node for a no-op (see `docs/architecture/edge-deployment.md` for that path).

### Notarization caveats (Darwin / `.pkg`)

Darwin binaries are signed and notarized at build time. A carried-forward Darwin binary is safe **within the notarization-cert validity window** of the original build (typically ~90 days). Beyond that, re-stapling (`xcrun stapler staple`) is required even when the binary bytes are identical. The `.pkg` installer is itself a function of the binaries it bundles plus the installer cert; carry it forward only when CLI Darwin + tray app + installer cert + pkg-build script are all unchanged.

### External dependencies

`external_dependencies.mistserver` and `external_dependencies.go-livepeer` are never built by this monorepo's CI — `release.yml` queries the GitHub API for the upstream release tag and records the digest + asset URLs. If the upstream tag is unchanged, the new manifest records the same image digest and native asset checksums; provisioning then naturally no-ops because Docker compares the digest and native installs compare the tarball checksum.

## Key Files

- `.github/workflows/release.yml` — image, binary, tray app, `.pkg`, manifest, GitHub release, and Homebrew tap jobs
- `.github/release-components.json` — service/webapp release matrix and `darwin_binary` flags
- `config/infrastructure.yaml` — pinned third-party Docker images (`caddy`, postgres, kafka, ...) with `image + digest + artifacts`
- `tools/release-plan/` — source-hash + baseline + BOM carry-forward decision engine; run as `make release-plan TAG=vX.Y.Z`
- `cli/pkg/provisioner/artifact_resolver.go` — resolves Docker images from the manifest as `image@digest` for first-party services, external deps, and infrastructure entries
- `scripts/macos-pkg/build-pkg.sh` — .pkg build + sign + notarize
- `scripts/macos-pkg/Distribution.xml` — Installer metadata
- `scripts/install.sh` — curl-pipe-sh CLI installer
- `cli/pkg/selfupdate/updater.go` — packaged asset selection and in-place CLI update logic

## Gotchas

- Homebrew tap bump is `continue-on-error: true` — a failed tap update doesn't block the release.
- The macOS CLI zip is the shipped notarized container and the only supported macOS CLI release asset.
- The `GITOPS_APP_ID` variable (not secret) is reused for both gitops and homebrew-tap repo access. The app needs `repositories: homebrew-tap` in the token scope.
- `libsrtp` Homebrew package is called `srtp`, not `libsrtp`.
