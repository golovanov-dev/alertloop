# Changelog

## Unreleased

### Security

- Docker: publish the API port on `127.0.0.1` instead of all interfaces, so a
  container port cannot slip past a host firewall (a published Docker port is not
  filtered by ufw). Use the bundled reverse proxy in `deploy/proxy/` for external
  access.
- Docker: harden the `alertloop` containers — drop all Linux capabilities, set
  `no-new-privileges`, and run with a read-only root filesystem; the image now
  makes the `/data` directory owned by the non-root runtime user so SQLite works
  under a read-only rootfs on native-Linux hosts.

### Changed

- Clarified that Pro and Enterprise editions are planned, not yet available:
  README, `NOTICE`, and the admin console no longer imply the paid editions or
  their channels (e.g. WhatsApp) already exist.

## 0.1.0 - 2026-07-05

First public release of AlertLoop Community Edition.

### Added

- Events HTTP API with API-key authentication (scopes: `ingest`, `read`, `full`) and an OpenAPI contract served with embedded Swagger UI at `/swagger`.
- Three event families: `incident`, `business_event`, `audit`.
- Event lifecycle `new` / `acknowledged` / `resolved` / `muted` / `escalated`, including a manual `escalate` action.
- Idempotent ingestion via `dedupe_key` (repeated keys return the existing event).
- Delivery channels: Email (SMTP with STARTTLS/SMTPS), Telegram, and HMAC-signed Webhook.
- Delivery worker with retries, exponential backoff, dead-letter state, and dead-letter replay.
- Delivery attempt history, stored separately from event state.
- Embedded React admin console at `/admin` (Overview, Events, Event detail, Deliveries with replay, About), protected by an admin token; simple events web page as well.
- Cursor-based pagination on list endpoints.
- Structured logs (text or JSON) to stdout or a file.
- 30-day event retention with automatic cleanup.
- SQLite (local/demo) and PostgreSQL (production) storage with automatic migrations.
- Single static CGO-free binary with `server`, `worker`, and `all` modes; cross-compiled release binaries (linux/amd64, linux/arm64, darwin, windows) built by CI with checksums.
- Docker Compose deployment (SQLite demo and PostgreSQL profiles) and a systemd unit example with install script.
- Reverse-proxy configuration examples for nginx and Apache (HTTPS termination).
- Built-in rate limiting for ingestion (global token bucket and per-IP), enabled by default.
