# Changelog

## 0.3.1 - 2026-08-22

Fixes for defects found reviewing 0.3.0 right after it shipped. Two of them made
0.3.0 either not start or start insecurely, so upgrade rather than staying on
0.3.0. No configuration changes are required.

### Fixed

- `${VAR}` now works in every field, not only string ones. In 0.3.0 the
  substituted value was forced to a string, so `retention_days: ${DAYS}`,
  `rate_limit.enabled: ${FLAG}`, `worker.concurrency`, an SMTP `port`, or
  `cors_origins` failed at startup with `cannot unmarshal !!str into int`. The
  documentation promised no such limitation.
- The PostgreSQL Compose profile now passes `ALERTLOOP_ADMIN_TOKEN` into the
  `api` and `worker` containers. In 0.3.0 the profile's example config read the
  token from that variable, but nothing forwarded it, so an operator who set it
  in `.env` — as documented — silently got the placeholder token published in
  this repository. The example config no longer carries a fallback either: a
  production profile with no token now refuses to start instead of running on a
  well-known one.
- A leftover pre-0.3.0 `ALERTLOOP_*` variable is now refused only when nothing
  else supplies that setting. If the config file sets the same field, the file
  wins and the variable is reported as a warning — 0.3.0 refused to start there
  too, which blocked a correct configuration (mounting your own config into a
  container that still exports the variable).
- `/events` no longer tells an unauthenticated visitor to set
  `ALERTLOOP_ADMIN_TOKEN` — advice that made 0.3.0 refuse to start. It now names
  the config file setting.
- Two adjacent references (`${A:-x}${B}`) are left verbatim, as documented for
  partial interpolation. 0.3.0 read the default greedily and produced `x}${B`.
- A config file with more than one YAML document is refused instead of silently
  ignoring everything after the `---` separator.
- `deploy/systemd/install.sh` installs the config owned by the `alertloop`
  service user. It was installed root-owned with mode 0640 while the unit runs
  as `User=alertloop`, so a fresh systemd install failed to start with
  `permission denied` — a defect present since 0.1.0.
- Startup no longer suggests configuring channels "in your config file/env";
  channels have always been file-only.

## 0.3.0 - 2026-08-22

One configuration file, one place to look. **Upgrading requires a configuration
change** if you set any `ALERTLOOP_*` variable other than `ALERTLOOP_CONFIG`:
those values move into the config file, and AlertLoop refuses to start while a
leftover one is set. Nothing about your stored events changes.

### Changed

- **The YAML file is now the only place AlertLoop is configured.** The
  `ALERTLOOP_*` settings variables (`ALERTLOOP_ADDR`, `ALERTLOOP_DB_DSN`,
  `ALERTLOOP_DB_DRIVER`, `ALERTLOOP_ADMIN_TOKEN`, `ALERTLOOP_RETENTION_DAYS`,
  `ALERTLOOP_LOG_*`, `ALERTLOOP_CORS_ORIGINS`, `ALERTLOOP_WORKER_*`,
  `ALERTLOOP_RATELIMIT_ENABLED`) no longer configure anything, and the
  `--addr`, `--db-dsn`, and `--db-driver` flags are gone. **What to do:** move
  those values into your config file. A leftover variable makes AlertLoop
  refuse to start, naming the config line to write instead — ignoring it could
  leave a process running on a database you did not choose. `ALERTLOOP_CONFIG`
  still selects the config file, and `--config` still overrides it.
- Secrets stay out of the config file through `${VAR}` references: a value
  written as exactly `${VAR}` or `${VAR:-default}` is replaced from the
  environment at startup. Only a whole value is substituted, so a password
  containing `$` is never mangled; a missing variable without a default stops
  the process instead of leaving the setting empty; and substituted text is
  treated as data, so a password containing `: ` or `#` stays a password.
- **Log timestamps are now always UTC**, matching the timestamps AlertLoop
  stores and serves. They previously followed the server's local zone, so the
  same build logged UTC inside Docker and local time under systemd. Not
  configurable: which zone a log line is in should not depend on how the process
  was deployed. If you parse logs, expect `...Z` instead of a local offset.
- Timestamps are now labelled with their timezone wherever they are shown. The
  built-in pages (`/events`, `/deliveries`) display UTC and say so in the column
  headers; the admin console keeps rendering in each viewer's own timezone and
  now names it. Previously the same event showed two different readings in the
  two interfaces with nothing to tell them apart.
- The Docker image ships a small default config file (`/etc/alertloop/alertloop.yaml`,
  overridable by mounting your own or pointing `ALERTLOOP_CONFIG` elsewhere) and
  no longer bakes a database DSN into the image. The bundled Compose profiles no
  longer need the workaround that blanked that variable so the configured
  PostgreSQL DSN would win.
- The README is organised around what you are trying to do — try it, run it on a
  server with or without Docker, grow into split roles — instead of a grid of
  deployment variants. The database is presented as the single configuration
  line it is, rather than as a demo/production split.
- Release binaries and the Docker image are now built with Go 1.27. Go stops
  issuing security fixes for a release once two newer ones exist, so 1.25 — the
  toolchain used until now — no longer receives them. Building from source still
  works with Go 1.25 or newer: the minimum in `go.mod` is unchanged.
  Two consequences of the newer toolchain are worth knowing:
  - the macOS release binaries now require macOS 13 or later;
  - Go removed the escape hatches that re-enabled TLS 1.0, RSA key exchange, and
    3DES (`GODEBUG=tls10server`, `tlsrsakex`, `tls3des`). If your SMTP server is
    old enough to need one of those, the Email channel can no longer connect to
    it; the server has to be upgraded or fronted by a modern relay.

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
