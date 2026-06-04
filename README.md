# UniFi Network List Sync

[![CI](https://img.shields.io/github/actions/workflow/status/mstrhakr/network-list-sync/ci.yml?branch=main&label=ci)](https://github.com/mstrhakr/network-list-sync/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/mstrhakr/network-list-sync?display_name=tag)](https://github.com/mstrhakr/network-list-sync/releases/latest)
[![Go Report Card](https://img.shields.io/badge/go%20report-A%2B-brightgreen?logo=go)](https://goreportcard.com/report/github.com/mstrhakr/network-list-sync)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mstrhakr/network-list-sync)](https://github.com/mstrhakr/network-list-sync/blob/main/go.mod)
[![License](https://img.shields.io/github/license/mstrhakr/network-list-sync)](LICENSE)

Automatically resolves DNS hostnames to IPs and pushes them into UniFi controller firewall groups. Replaces the manual process of maintaining IP allow-lists.

**Features:**

- **Web UI** for creating and managing sync jobs
- **Cron scheduling** to keep firewall groups automatically updated
- **Multi-resolver DNS lookups** across authoritative DNS plus Cloudflare, Google, Quad9, and OpenDNS
- **Literal IPv4 and IPv4 CIDR support** in the input list for mixed hostname and direct-entry workflows
- **DNS preview** to test hostname resolution before syncing
- **Run history** with detailed diffs of every sync
- **Single binary** with embedded web interface — no external dependencies
- **SQLite** storage for jobs and logs

## Why Use It

Use this when you maintain UniFi firewall/address groups that depend on hostnames whose IPs change over time.

Typical examples:

- Monitoring providers with rotating probe IPs
- Third-party integrations that publish hostnames instead of fixed IPs
- Mixed allow-lists where some entries are hostnames and others are static CIDRs

Without automation, these groups drift and access breaks. This tool keeps the group aligned with current DNS results on a schedule.

## How It Works

1. You create a sync job in the web UI.
2. The job stores UniFi credentials, target group ID, and hostname/CIDR inputs.
3. On manual run or schedule, the app resolves hostnames using multiple DNS resolvers.
4. It computes the desired final list and updates the UniFi firewall group.
5. It stores run history so you can review diffs and failures.

## Before You Start

1. A reachable UniFi Network Controller URL.
2. A UniFi API key with permission to update firewall groups.
3. The target Site name (usually `default`).
4. The firewall/address group ID you want to keep in sync.
5. A host machine where Docker can keep a persistent `/data` volume.

## Quick Start (Docker Preferred)

### Run Latest Development Build

```bash
docker run --rm -p 8080:8080 \
   -v unifi-sync-data:/data \
   ghcr.io/mstrhakr/network-list-sync:main
```

Open [http://localhost:8080](http://localhost:8080) in your browser.

### Run A Stable Release

```bash
docker run --rm -p 8080:8080 \
   -v unifi-sync-data:/data \
   ghcr.io/mstrhakr/network-list-sync:v0.1.0
```

The image is published for `linux/amd64` and `linux/arm64`.

### Image Tag Strategy

- `main`: latest build from the `main` branch
- `vMAJOR`: latest patch release in a major series
- `vMAJOR.MINOR`: latest patch release in a minor series
- `vMAJOR.MINOR.PATCH`: exact immutable release
- `sha-<commit>`: commit-specific image

### Data Persistence

The container defaults to:

- `-addr :8080`
- `-db /data/sync.db`
- `-log-file /data/sync.log`

Use a bind mount or named volume for `/data` so DB and logs persist across upgrades.

### Unraid Permissions (Optional)

For Unraid-style deployments, you can run the process as a specific user/group
and set file mode defaults with environment variables:

- `PUID`: runtime user ID (for example `99`)
- `PGID`: runtime group ID (for example `100`)
- `UMASK`: process umask (for example `002`)

Example:

```bash
docker run --rm -p 8080:8080 \
   -e PUID=99 \
   -e PGID=100 \
   -e UMASK=002 \
   -v unifi-sync-data:/data \
   ghcr.io/mstrhakr/network-list-sync:main
```

### Optional: Build The Image Locally

```bash
docker build -t network-list-sync:dev .
```

### Optional: Docker Compose Example

Full example file: `docs/docker-compose.unraid.yml`

```yaml
services:
   network-list-sync:
      image: ghcr.io/mstrhakr/network-list-sync:main
      container_name: network-list-sync
      restart: unless-stopped
      ports:
         - "8080:8080"
      environment:
         PUID: "99"
         PGID: "100"
         UMASK: "022"
      volumes:
         - /mnt/user/appdata/network-list-sync:/data
```

### Optional: Run From Binary

```bash
go build -o network-list-sync .
./network-list-sync
```

For maintainer and contributor workflows (local dev, CI, and release process), see [docs/development.md](docs/development.md).

### Options

| Flag       | Default   | Description                                |
|------------|-----------|--------------------------------------------|
| `-addr`    | `:8080`   | HTTP listen address                        |
| `-db`      | `sync.db` | SQLite database path                       |
| `-debug`   | `false`   | Enable debug logging                       |
| `-verbose` | `false`   | Enable verbose logging                     |
| `-log-file`| `sync.log`| Log file path (`""` disables file logging) |
| `-version` | `false`   | Print build version metadata and exit      |

```bash
./network-list-sync -addr :9090 -db /var/lib/sync/data.db -log-file ./sync.log
```

## Usage

### First-Time Setup Walkthrough

1. Start the container and open the UI at [http://localhost:8080](http://localhost:8080).
2. Click **+ New Sync Job**.
3. Enter a clear job name, for example `grafana-probes-prod`.
4. Set **Controller URL** to your UniFi endpoint, for example `https://192.168.1.4:8443`.
5. Set **API Key** from UniFi OS (`Settings -> System -> Advanced -> API Keys`).
6. Set **Site** to your UniFi site name (usually `default`).
7. Set **Firewall Group ID** by opening the target group in UniFi and copying the hex ID from the URL.
8. In **Hostnames**, add one entry per line.
9. Use comments with `#` for documentation.
10. Add a schedule such as `0 */6 * * *` for every 6 hours, or leave blank for manual runs.
11. Click **Save**.
12. Click **Run Now** to verify connectivity and output.

Example host list:

```text
# Grafana synthetic probes
synthetics.grafana.net

# Static office egress
203.0.113.10
203.0.113.0/24
```

### What To Check After First Run

1. The run status is successful in job history.
2. The log shows resolved records and applied changes.
3. The target UniFi group now contains the expected entries.
4. A second run without DNS changes produces little or no diff.

### Recommended Operations

1. Start with manual runs while validating job configuration.
2. Enable schedule only after one or two clean manual runs.
3. Use stable tags (`vMAJOR.MINOR.PATCH`) in production.
4. Use `main` for testing new behavior.
5. Keep `/data` persisted so jobs and history survive container updates.

### Common Failure Causes

1. Invalid controller URL or TLS trust issues.
2. API key missing required permissions.
3. Wrong site or firewall group ID.
4. DNS resolver/network restrictions from the container host.
5. Container started without persistent `/data` volume.

## API Endpoints

| Method | Path                    | Description                     |
|--------|-------------------------|---------------------------------|
| GET    | `/api/jobs`             | List all sync jobs              |
| POST   | `/api/jobs`             | Create a new sync job           |
| GET    | `/api/jobs/{id}`        | Get a specific job              |
| PUT    | `/api/jobs/{id}`        | Update a job                    |
| DELETE | `/api/jobs/{id}`        | Delete a job and its history    |
| POST   | `/api/jobs/{id}/run`    | Trigger a sync immediately      |
| GET    | `/api/jobs/{id}/logs`   | Get run history for a job       |
| POST   | `/api/resolve`          | Preview DNS resolution          |

## Cron Schedule Examples

| Expression      | Meaning          |
|-----------------|------------------|
| `*/30 * * * *`  | Every 30 minutes |
| `0 */6 * * *`   | Every 6 hours    |
| `0 0 * * *`     | Daily at midnight|
| `0 0 * * 1`     | Every Monday     |

## UniFi API Schema Reference

- Local copy: `docs/reference/unifi-network-10.1.85.json`
- Source: `https://github.com/beezly/unifi-apis/raw/refs/heads/main/unifi-network/10.1.85.json`

This file is treated as the authoritative schema reference for endpoint paths and response payload shapes.

## License

MIT
