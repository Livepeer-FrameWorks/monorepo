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

## Key Files

- `.github/workflows/release.yml` — image, binary, tray app, `.pkg`, manifest, GitHub release, and Homebrew tap jobs
- `.github/release-components.json` — service/webapp release matrix and `darwin_binary` flags
- `scripts/macos-pkg/build-pkg.sh` — .pkg build + sign + notarize
- `scripts/macos-pkg/Distribution.xml` — Installer metadata
- `scripts/install.sh` — curl-pipe-sh CLI installer
- `cli/pkg/selfupdate/updater.go` — packaged asset selection and in-place CLI update logic

## Gotchas

- Homebrew tap bump is `continue-on-error: true` — a failed tap update doesn't block the release.
- The macOS CLI zip is the shipped notarized container and the only supported macOS CLI release asset.
- The `GITOPS_APP_ID` variable (not secret) is reused for both gitops and homebrew-tap repo access. The app needs `repositories: homebrew-tap` in the token scope.
- `libsrtp` Homebrew package is called `srtp`, not `libsrtp`.
