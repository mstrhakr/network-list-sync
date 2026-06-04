# Development And Release Guide

This document is for maintainers and contributors. For end-user setup, use [README.md](../README.md).

## Local Development

```bash
# Run in development mode
go run . -addr :8080

# Build local binary
go build -o network-list-sync .

# Build for Linux
GOOS=linux GOARCH=amd64 go build -o network-list-sync .
```

## CI/CD Workflows

### CI

Workflow: `.github/workflows/ci.yml`

- Triggers on pull requests and pushes to `main`
- Runs:
  - `go vet ./...`
  - `go test ./...`
  - `go build` with CI version metadata

### Binary Release

Workflow: `.github/workflows/release.yml`

- Triggers when a Git tag matching `v*` is pushed
- Runs GoReleaser to publish GitHub Release assets

GoReleaser config: `.goreleaser.yaml`

Artifacts:

- Linux, Darwin, and Windows binaries
- amd64 and arm64 architectures
- `.tar.gz` archives (and `.zip` for Windows)
- `checksums.txt`

### Container Publish

Workflow: `.github/workflows/container.yml`

- Triggers on:
  - push to `main`
  - published GitHub Release
- Publishes multi-arch images (`linux/amd64`, `linux/arm64`) to GHCR
- Tag behavior:
  - on `main`: `main`, `sha-<commit>`
  - on release: `vMAJOR`, `vMAJOR.MINOR`, `vMAJOR.MINOR.PATCH`, `sha-<commit>`

## Versioning Policy (SemVer)

- Use `vMAJOR.MINOR.PATCH` tags
- `v1.4.0` for backward-compatible features
- `v1.4.1` for fixes
- `v2.0.0` for breaking changes
- Prereleases are supported (for example `v1.5.0-rc.1`)

## Release Flow

1. Merge tested changes into `main`.
2. Create and push a SemVer tag.

```bash
git tag v1.0.0
git push origin v1.0.0
```

1. GitHub Actions runs `Release` and publishes binary artifacts.
2. Publish the GitHub Release in the UI to trigger container tags for `vMAJOR`, `vMAJOR.MINOR`, and `vMAJOR.MINOR.PATCH`.

## Version Metadata

Build metadata is embedded with ldflags and visible via:

```bash
./network-list-sync -version
```
