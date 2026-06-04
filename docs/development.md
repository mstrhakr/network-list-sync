# Development And Release Guide

This document is for maintainers and contributors. For end-user setup and operations, see [README.md](../README.md).

## Local Development

### Run

```bash
go run . -addr :8080
```

### Build

```bash
go build -o network-list-sync .
```

### Cross-Compile Example

```bash
GOOS=linux GOARCH=amd64 go build -o network-list-sync .
```

### Test

```bash
go test ./...
```

## Project Model

- Endpoint-based provider abstraction (currently UniFi and NPM).
- Jobs can target one or many endpoint/list pairs.
- DNS server configuration is stored in SQLite and used by resolve preview and sync runs.
- Web UI is embedded from [ui/index.html](../ui/index.html).

## CI/CD Workflows

### CI

Workflow: .github/workflows/ci.yml

- Triggers on pull requests and pushes to main.
- Runs:
  - go vet ./...
  - go test ./...
  - go build with version metadata

### Binary Release

Workflow: .github/workflows/release.yml

- Trigger: push tag matching v*
- Uses GoReleaser configured in .goreleaser.yaml
- Publishes GitHub Release artifacts:
  - Linux, Darwin, Windows
  - amd64 and arm64
  - tar.gz and zip archives
  - checksums.txt

### Container Publish

Workflow: .github/workflows/container.yml

- Triggers on:
  - push to main
  - published GitHub Release
- Publishes multi-arch GHCR images:
  - linux/amd64
  - linux/arm64
- Tag strategy:
  - main branch: main, sha-<commit>
  - releases: vMAJOR, vMAJOR.MINOR, vMAJOR.MINOR.PATCH, sha-<commit>

## Versioning Policy

- SemVer tags: vMAJOR.MINOR.PATCH
- Minor versions for backward-compatible features
- Patch versions for fixes
- Major versions for breaking changes
- Prereleases supported (for example v1.5.0-rc.1)

## Release Flow

1. Merge tested changes into main.
2. Create and push a SemVer tag.

```bash
git tag v1.0.0
git push origin v1.0.0
```

3. GitHub Actions runs release workflows and publishes assets.
4. Publish GitHub Release to promote container version tags.

## Version Metadata

Build metadata is embedded with ldflags and available via:

```bash
./network-list-sync -version
```
