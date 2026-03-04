# Build & Packaging - Multi-Platform Release Pipeline

FrameWorks builds for 3 targets: `linux/amd64`, `linux/arm64`, `darwin/arm64`. Linux amd64 builds run on GitHub-hosted ubuntu runners. All arm64 and darwin builds run on a self-hosted Mac Mini ARM runner, eliminating QEMU emulation. darwin/amd64 (Intel Mac) is not supported — Apple is phasing out Rosetta 2 in macOS 28.

## Architecture

```
                  release.yml
                       │
        ┌──────────────┼──────────────┐
        ▼              ▼              ▼
  ubuntu-latest    Mac Mini ARM    ubuntu-latest
  ┌────────────┐   ┌────────────┐  ┌────────────┐
  │ Go amd64   │   │ Go arm64   │  │ Docker     │
  │ binaries   │   │ Go darwin  │  │ amd64 imgs │
  │            │   │ codesign   │  └────────────┘
  └────────────┘   │ notarize   │
                   │ .pkg build │
                   │ Docker     │
                   │ arm64 imgs │
                   └────────────┘
                        │
                   ┌────┴────┐
                   ▼         ▼
              GitHub     Homebrew
              Release    tap bump
```

## Runner Setup

**Self-hosted runner:** `ddvtech-mac-mini-arm` at `macos-arm64-self-hosted` label.

Installed software:

- Go (latest), Meson, Ninja, pkg-config
- FFmpeg, SRT, SRTP (MistServer deps)
- `filosottile/musl-cross/musl-cross` (CGO linux cross-compilation)
- Docker Desktop (linux/arm64 container builds, native ARM VM)
  Certs are NOT on disk — injected at runtime via `apple-actions/import-codesign-certs@v2` from GitHub Secrets.

## Build Matrix

### Go Services (`release.yml`)

| Target         | Runner        | CGO      | Notes                                          |
| -------------- | ------------- | -------- | ---------------------------------------------- |
| `linux/amd64`  | ubuntu-latest | zig musl | All services                                   |
| `linux/arm64`  | Mac Mini      | zig musl | All services, native ARM                       |
| `darwin/arm64` | Mac Mini      | No       | Non-CGO only (excludes quartermaster, foghorn) |

### MistServer (`build.yml` in mistserver repo)

MistServer is C++, built with Meson/Ninja.

| Target                | Runner        | Method                               |
| --------------------- | ------------- | ------------------------------------ |
| `linux/amd64` binary  | ubuntu-latest | `apt-get` deps + meson               |
| `linux/arm64` binary  | Mac Mini      | Alpine Docker container (native ARM) |
| `darwin/arm64` binary | Mac Mini      | `brew` deps + meson                  |
| Docker `linux/amd64`  | ubuntu-latest | buildx                               |
| Docker `linux/arm64`  | Mac Mini      | buildx native (Docker Desktop)       |

All targets: `WITH_AV=true` (transcoding required).

### Docker Images

Multi-arch manifests assembled from per-arch images pushed from separate runners. A `create-manifest` job runs after both arch-specific image jobs complete.

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

- `frameworks-cli-v*-{linux-amd64,linux-arm64,darwin-arm64}.tar.gz`
- `frameworks-{service}-v*-{linux-amd64,linux-arm64,darwin-arm64}.tar.gz`
- `frameworks-v*.pkg` (macOS installer: CLI + tray app)
- `manifest.yaml` (machine-readable release metadata)
- Docker images pushed to registry

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

`scripts/install.sh` — curl-pipe-sh installer for CLI binary. Detects OS and arch, downloads from GitHub Release.

## Key Files

- `.github/workflows/release.yml` — `build-arm64-and-darwin`, `build-darwin-pkg`, manifest jobs
- `scripts/macos-pkg/build-pkg.sh` — .pkg build + sign + notarize
- `scripts/macos-pkg/Distribution.xml` — Installer metadata
- `scripts/install.sh` — curl-pipe-sh CLI installer

## Gotchas

- MistServer linux/arm64 binary is built inside a Docker container on the Mac, not cross-compiled. Docker Desktop must be running.
- Homebrew tap bump is `continue-on-error: true` — a failed tap update doesn't block the release.
- The `GITOPS_APP_ID` variable (not secret) is reused for both gitops and homebrew-tap repo access. The app needs `repositories: homebrew-tap` in the token scope.
- `libsrtp` Homebrew package is called `srtp`, not `libsrtp`.
