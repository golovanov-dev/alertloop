# Changelog

## 0.2.0 - 2026-08-15

Routing rules and Telegram delivery through a proxy — both in Community.
**Upgrading from 0.1.1 requires no configuration changes**: with no `routing`
section, every event is still delivered to every configured channel.

### Added

- Routing rules (`routing` section): send each event to the channels that should
  receive it, matched on `type`, `severity`, `min_severity`, `source`, and
  `category`. Values inside one field are OR-ed, fields are AND-ed, `source` and
  `category` accept a trailing `*`, and the first matching rule wins. `channels: []`
  suppresses delivery while still storing the event; `default` catches events that
  match no rule.
- Routing diagnostics at startup: the resolved routing table is logged, along
  with warnings for rules made unreachable by an earlier catch-all and for
  configured channels nothing routes to. A rule naming a channel that does not
  exist stops the process, and an event that matches no rule with no `default`
  configured is logged at `warn` level.
- `GET /v1/routing` and `POST /v1/routing/preview` (scope `full`): inspect the
  routing table and check where an event would be delivered, without creating or
  delivering anything.
- Telegram channels accept a `proxy` (`http`, `https`, `socks5`, `socks5h`) for
  hosts that cannot reach `api.telegram.org` directly. It is per channel, so a
  webhook into an internal network stays direct. An unsupported scheme or an
  unusable URL stops the process at startup. With `proxy` unset, `HTTP_PROXY` /
  `HTTPS_PROXY` / `NO_PROXY` keep working as before.

### Changed

- Routing rules and the Telegram proxy are Community capabilities, not planned
  Pro ones as previously documented. Pro keeps multi-project, RBAC, escalation
  policies, UI-managed retention policies, WhatsApp, and SDKs.

### Security

- The password in a proxy URL is redacted from delivery errors, logs, the API,
  and the admin console, as the bot token already was. Startup messages and the
  channel list show a proxy as `scheme://host:port` only.

## 0.1.1 - 2026-07-30

Hardening and documentation release. No API or configuration changes are
required to upgrade.

### Security

- Docker: publish the API port on `127.0.0.1` instead of all interfaces, so a
  container port cannot slip past a host firewall (a published Docker port is not
  filtered by ufw). Use the bundled reverse proxy in `deploy/proxy/` for external
  access. **Note:** on a remote server `http://<server-ip>:8080` no longer
  answers for the Compose deployments — reach it through a reverse proxy, an SSH
  tunnel, or by deliberately changing the port mapping.
- Docker: harden the `alertloop` containers — drop all Linux capabilities, set
  `no-new-privileges`, and run with a read-only root filesystem; the image now
  makes the `/data` directory owned by the non-root runtime user so SQLite works
  under a read-only rootfs on native-Linux hosts.
- Documented setting strong secrets (admin token, DB password) via `.env`; the
  demo admin token is now overridable through the environment instead of a fixed
  inline default.
- Documented that with **neither** `admin_token` **nor** `api_keys` configured,
  the API accepts unauthenticated requests with full scope (a local-demo
  convenience that already logged a startup warning). Always set `admin_token` on
  anything reachable by others.

### Fixed

- PostgreSQL Compose profile: the `dsn` in `alertloop.yaml` is authoritative
  again. The runtime image bakes in a SQLite `ALERTLOOP_DB_DSN`, and environment
  variables override YAML, so the `api` and `worker` containers could silently
  use SQLite instead of the configured PostgreSQL database.
- nginx example config: pass HTTP/2 as a `listen` parameter, so the config works
  on nginx builds that do not accept the newer standalone directive.
- Email and Telegram notifications no longer repeat the event message: it was
  printed both in the email subject and as the first line of the body, and twice
  in a row inside a single Telegram message.

### Changed

- Clarified that Pro and Enterprise editions are planned, not yet available:
  README, `NOTICE`, and the admin console no longer imply the paid editions or
  their channels (e.g. WhatsApp) already exist.
- Corrected the documented behaviour of event retention: `retention_days` (and
  `ALERTLOOP_RETENTION_DAYS`) has always been configurable with no upper bound —
  30 days is the default, not a fixed limit. Earlier documentation described it as
  fixed. Behaviour is unchanged.
- README now documents the two deployment paths separately where they differ:
  the binary listens on `:8080` on all interfaces, while both Compose files
  publish the port on loopback only.

### Added

- OpenAPI contract now documents `GET /v1/stats` (event and delivery counts by
  state) and `GET /v1/info` (version, edition, license). Both already existed and
  required the `read` scope; only the contract was missing them.
- `cors_origins` is now shown as a commented example in `alertloop.example.yaml`,
  and `.env.example` lists the supported `ALERTLOOP_WORKER_CONCURRENCY`,
  `ALERTLOOP_WORKER_MAX_ATTEMPTS`, `ALERTLOOP_RATELIMIT_ENABLED`, and
  `ALERTLOOP_CORS_ORIGINS` variables.
- Documentation for the three built-in web pages (`/events`, `/events/{id}`,
  `/deliveries`); previously only the events page was mentioned.

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
