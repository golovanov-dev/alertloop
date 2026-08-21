# AlertLoop

AlertLoop is a self-hosted event, incident, and business notification center for
software teams and operations-heavy businesses.

It helps applications send important technical incidents and business events into
one reliable place, track their status, and deliver them through channels such as
email, Telegram, and webhooks. Planned paid editions will add channels such as
WhatsApp.

## Product Direction

AlertLoop starts as a self-hosted product. Cloud SaaS is intentionally deferred
until the core product and market positioning are validated.

## Editions

- **Community** (this repository): free, self-hosted, backend API, Swagger/OpenAPI,
  basic events list page, Email/Telegram/Webhook delivery, routing rules,
  Telegram delivery through a proxy, delivery retries and dead-letter replay,
  SQLite and PostgreSQL.
- **Pro Self-hosted** (planned, paid): multi-project, SDKs, WhatsApp, RBAC,
  retention policies (per-project and per-event-type rules managed from the UI),
  escalation policies.
- **Enterprise** (planned, paid): on-prem license, SSO, HA, audit, custom
  adapters, support.

## Features (Community)

- Events API with API-key auth and OpenAPI/Swagger.
- Three event families: `incident`, `business_event`, `audit`.
- Event lifecycle: `new`, `acknowledged`, `resolved`, `muted`, `escalated`
  (with a manual `escalate` action).
- Idempotent ingestion via `dedupe_key`.
- Delivery channels: Email (SMTP), Telegram, Webhook (HMAC-signed).
- [Routing rules](#routing-rules): send each event to the channels that should
  get it (by type, severity, source, or category), with a dry-run preview
  endpoint.
- [Telegram from a restricted network](#telegram-from-a-restricted-network): a
  per-channel HTTP/SOCKS5 `proxy`, or an `api_base` mirror.
- Delivery worker with retries, exponential backoff, dead-letter, and replay.
- Delivery attempt history, separate from event state.
- Three simple built-in web pages protected by an admin token: events list
  (`/events`), event detail (`/events/{id}`), and deliveries (`/deliveries`) —
  next to the richer React admin console at `/admin`.
- Cursor-paginated list endpoints.
- Structured logs (text or JSON), to stdout or a file.
- Event retention with automatic cleanup (30 days by default, configurable via
  `retention_days`).

## Requirements

- **Go**: 1.25 or newer — only to build from source. Release binaries are static
  and need no runtime; there is no CGO dependency.
- **PostgreSQL** (production): 12 or newer (14+ recommended, tested on 16). Uses
  `JSONB`, partial indexes, and `FOR UPDATE SKIP LOCKED`.
- **SQLite** (local/demo): none — it is embedded (pure-Go driver). Just a file.
- **Docker** (optional): any recent Docker Engine with Compose v2 for the
  container deployment path.

## Quick Start

### Docker Compose (demo, SQLite)

```bash
docker compose up -d
```

Then open:

- API docs: <http://localhost:8080/swagger>
- Events page: <http://localhost:8080/events?token=change-me-admin> (admin token)

### Prebuilt binary

Download a release binary (linux/amd64, linux/arm64; darwin/windows for local
evaluation), then run all-in-one mode backed by SQLite:

```bash
./alertloop --config alertloop.yaml all
```

To install as a systemd service, see `deploy/systemd/` (`install.sh`).

### From source

```bash
make run                 # build + run all-in-one on :8080 (SQLite)
make test                # run the test suite
```

### Send your first event

The API accepts your **admin token** (full access) or a scoped **API key**. For
a quick test, use the admin token from your config:

```bash
curl -X POST http://localhost:8080/v1/events \
  -H "X-API-Key: change-me-admin" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "incident",
    "severity": "critical",
    "source": "feeds_worker",
    "message": "Feed processing failed",
    "dedupe_key": "feeds:developer:15:flats"
  }'
```

For real integrations, create least-privilege **API keys** with a scope
(`ingest` for event sources, `read` for dashboards, `full` for trusted tools) in
the config file — see `alertloop.example.yaml`.

## Runtime Modes

The single `alertloop` binary runs in three modes:

- `server` — HTTP API and web UI.
- `worker` — delivery workers and retention cleanup.
- `all` — both in one process (default; ideal for small installs).

## Admin console

AlertLoop ships a React admin console (Overview, Events, Event detail,
Deliveries with dead-letter replay, About). **It is embedded in the binary** and
served at `/admin` on the same origin as the API — nothing extra to install or
run. With either deployment path you simply open `/admin` — but *where* it is
reachable from differs, because the two paths bind the port differently:

- **Local (either path)**: <http://localhost:8080/admin>.
- **Binary / systemd on a server**: AlertLoop listens on `:8080` on all
  interfaces, so it is also reachable at `http://<server-ip>:8080/admin`.
- **Docker Compose on a server**: both bundled Compose files publish the port on
  `127.0.0.1` only, so `http://<server-ip>:8080/admin` is refused **by design** —
  a published Docker port is not filtered by a host firewall such as ufw, so
  binding `0.0.0.0` would silently expose the console. Reach it through the
  reverse proxy in `deploy/proxy/`, through an SSH tunnel
  (`ssh -L 8080:127.0.0.1:8080 user@server`, then use the Local URL above), or by
  deliberately changing the mapping to `"8080:8080"` if you accept the exposure.

For a real domain with HTTPS (the recommended setup for either path), see
[Access over a domain](#access-over-a-domain-https).

Sign in with the **admin token** from your config (there are no user accounts in
Community). The token is kept in the browser's session storage and sent to the
API. Because the console is same-origin with the API, it works at any host or
domain with no rebuild and no CORS configuration.

The console has a light responsive layout: on phones the sidebar collapses into
a menu and wide tables scroll horizontally. A full mobile experience is not a
Community goal — use a desktop browser for day-to-day work.

### Developing the console

```bash
cd web/admin
npm install
npm run dev        # http://localhost:5273, proxies /v1 to localhost:8080
```

Build it into the binary with `make admin` (outputs to `internal/adminui/dist`,
which the Go build embeds). The Docker image build does this automatically, so
`go build` and `docker build` both produce a binary/image with the console
already inside.

## Access over a domain (HTTPS)

Where AlertLoop is reachable before you add a proxy depends on how you run it:

- **Binary / systemd**: the process listens on `:8080` on all interfaces, so on a
  server it is reachable at `http://<server-ip>:8080` (restrict it with your host
  firewall, or bind it to loopback with `addr: "127.0.0.1:8080"`).
- **Docker Compose**: both bundled Compose files publish the port as
  `127.0.0.1:8080:8080`, so the container is reachable from the host only. That is
  deliberate: a published Docker port is not filtered by ufw/firewalld, so
  binding `0.0.0.0` would put the admin console on the public internet without
  the host firewall noticing.

Either way, for a real domain put an HTTPS reverse proxy in front — AlertLoop
itself speaks plain HTTP and does not terminate TLS. Since the API, admin
console, and Swagger are all one origin, a single proxy rule covers everything:

```text
[browser] --HTTPS--> [nginx/Apache :443] --HTTP--> [alertloop 127.0.0.1:8080]
```

Then everything is on your domain, same-origin, no CORS:

- `https://alerts.example.com/admin` — admin console
- `https://alerts.example.com/v1/...` — API
- `https://alerts.example.com/swagger` — API docs

Ready-to-adapt configs are in `deploy/proxy/`:

- **nginx** — `deploy/proxy/nginx.conf`
- **Apache** — `deploy/proxy/apache.conf`

Both redirect HTTP→HTTPS and forward to `127.0.0.1:8080` — which is exactly what
the Compose port mapping publishes, so the proxy path needs no Compose changes.
Get a certificate with Let's Encrypt (`certbot`). HTTPS is required in
production — the admin token travels with each request and must not go over
plaintext HTTP.

## Database

### SQLite (local/demo)

Point the DSN at a file path; AlertLoop creates the file and its tables
automatically on first run. `:memory:` is supported for throwaway runs.

```yaml
database:
  driver: sqlite
  dsn: alertloop.db
```

### PostgreSQL (production)

AlertLoop connects to an **existing database** and creates/updates only its own
tables (`events`, `delivery_attempts`, `schema_migrations`) via migrations that
run automatically at startup. **It does not create the database itself** — the
target database must already exist (this is standard PostgreSQL client behavior:
you cannot connect to a database that has not been created).

Create the database once:

```bash
createdb -U postgres alertloop
#   or:  psql -U postgres -c "CREATE DATABASE alertloop;"
```

Then point AlertLoop at it. With the **binary**, put it in your config file
(e.g. `alertloop.yaml`) — this is the only part you change to switch from SQLite
to PostgreSQL:

```yaml
database:
  driver: postgres
  dsn: "postgres://USER:PASSWORD@HOST:5432/alertloop?sslmode=require"
```

```bash
./alertloop --config alertloop.yaml all
```

(The DSN carries a password, so it is a good candidate for `${VAR}` — see
[Configuration](#configuration).)
Use `sslmode=require` for a remote database; `sslmode=disable` is only for a
local/private-network Postgres.

Using an **existing/shared database** is fine: AlertLoop only touches its own
tables and never drops or alters unrelated tables. Just make sure no other
application uses tables named `events`, `delivery_attempts`, or
`schema_migrations` in that database. (A configurable table prefix/schema is on
the roadmap for shared databases.)

## Logs

AlertLoop writes structured logs. By default they go to **stdout**; set a file
path to also persist them for external tools.

```yaml
log:
  level: "info"     # debug | info | warn | error
  format: "text"    # text (human-readable) | json (for Loki/Elasticsearch/Vector)
  file: ""          # empty = stdout; e.g. /var/log/alertloop/alertloop.log
```

How to read logs by deployment:

- **Binary (stdout)**: run in a terminal, or redirect: `./alertloop all >> alertloop.log 2>&1`.
- **Binary (file)**: set `log.file` and read it with any tool:
  `tail -f /var/log/alertloop/alertloop.log`.
- **systemd**: logs go to the journal — `journalctl -u alertloop -f`. Set
  `format: json` for machine-readable output that log shippers can parse.
- **Docker**: `docker compose logs -f alertloop` (or `docker logs`).

For centralized logging, set `format: json` and point your log collector at the
file or the container's stdout.

## Configuration

**One YAML file configures everything**, on top of built-in defaults. Point the
binary at it with `--config /path/to/alertloop.yaml` (or `ALERTLOOP_CONFIG`).
See `alertloop.example.yaml` for the full list of settings.

The environment is not a second place to configure AlertLoop — it exists to keep
secrets out of the file. Any value written as exactly `${VAR}` or
`${VAR:-default}` is replaced from the environment at startup:

```yaml
admin_token: ${ALERTLOOP_ADMIN_TOKEN}
channels:
  telegram:
    - name: alerts
      bot_token: ${TELEGRAM_BOT_TOKEN}
      chat_id: "-1001234567890"
database:
  dsn: ${ALERTLOOP_DB_DSN:-alertloop.db}
```

Rules, and there are only three:

- **The whole value, or nothing.** `${VAR}` inside a longer string is left
  alone — that way a password containing a literal `$` is never mangled.
- **A missing variable stops the process**, unless the reference carries a
  `:-default`. An empty `admin_token` caused by a typo in a variable name would
  leave the API open, so it is refused instead.
- **The substituted text is data, not YAML.** A password containing `: ` or `#`
  stays a password.

Everything else — channels, API keys, routing, worker and rate-limit tuning —
lives in the file only.

> **Upgrading from 0.2.x:** the `ALERTLOOP_ADDR`, `ALERTLOOP_DB_DSN`,
> `ALERTLOOP_LOG_*`, `ALERTLOOP_WORKER_*`, and related variables no longer
> configure anything. If one is still set, AlertLoop **refuses to start** and
> names the config line to write instead — ignoring them could leave a process
> running on a database its operator did not choose.

### Delivery channels are optional

You can run AlertLoop **with no channels configured** — it will accept and store
events (visible in the API and at `/admin`) and simply deliver nothing. This is
the default in the example configs, so the app starts out of the box. To send
notifications, configure one or more channels (email/telegram/webhook) in the
YAML file. If you enable a channel you must fill in all of its required fields,
or startup fails with a clear message (this catches typos like a missing
`bot_token`).

With no `routing` section configured, every event is delivered to **every**
configured channel. To split events between audiences, see
[Routing rules](#routing-rules).

### Routing rules

One AlertLoop instance usually serves more than one audience: the customer who
only wants orders, and the developer who also wants the technical failures. The
optional `routing` section decides which channels each event goes to.

```yaml
routing:
  # Rules are checked top to bottom; the FIRST match wins.
  rules:
    - name: silence-healthchecks
      match:
        source: [healthcheck]
      channels: []              # explicit "nowhere": stored, never delivered

    - name: incidents-to-dev
      match:
        type: [incident]
      channels: [dev-telegram, dev-email]

    - name: orders-to-customer
      match:
        type: [business_event]
        category: ["order.*"]
      channels: [customer-telegram]

  # Where events that matched no rule go.
  default: [dev-telegram]
```

Conditions inside `match`:

| Field | Matches | Format |
|---|---|---|
| `type` | event family | list of `incident`, `business_event`, `audit` |
| `severity` | severity | list of `info`, `success`, `warning`, `error`, `critical` |
| `min_severity` | severity | one value; matches that level and above |
| `source` | event source | list; a trailing `*` is a prefix wildcard |
| `category` | event category | list; a trailing `*` is a prefix wildcard |

Rules of the road:

- Values inside one field are OR-ed; different fields are AND-ed. An omitted
  field constrains nothing, and a rule with no `match` at all is a catch-all.
- Comparison ignores case and surrounding whitespace.
- Only a **trailing** `*` is supported, and only for `source` and `category`:
  `order.*` matches `order.created`. A `*` anywhere else is a configuration
  error — there are no regular expressions.
- `min_severity` ranks are `info` = 10, `success` = 10, `warning` = 20,
  `error` = 30, `critical` = 40. `success` deliberately shares `info`'s rank:
  this is an order of **alarm**, not of importance, and a successful outcome is
  not more alarming than a notice.
- **The first matching rule wins** and later rules are not consulted. Channel
  lists from several rules are never merged — at three in the morning,
  predictability beats expressiveness.
- `channels: []` is deliberate suppression: the event is stored (and visible in
  the API and the console) but nothing is delivered.
- `default` applies **only** when no rule matched. Without it, unmatched events
  are delivered nowhere and each one is logged at `warn` level with its id,
  type, severity, source, and category.

The startup log prints the resolved table, plus warnings for the two mistakes
that are otherwise invisible: rules that can never match because a catch-all
sits above them, and configured channels that no rule and no default sends to.
A rule naming a channel that does not exist stops the process.

To check rules against a live instance without raising a false incident, use the
preview endpoint (`full` scope, creates nothing):

```bash
curl -X POST http://localhost:8080/v1/routing/preview \
  -H "X-API-Key: change-me-admin" -H "Content-Type: application/json" \
  -d '{"type":"business_event","source":"shop","category":"order.created"}'
# {"matched_rule":"orders-to-customer","channels":["customer-telegram"]}
```

`GET /v1/routing` returns the whole table as it took effect.

**Omitting the `routing` section keeps the pre-0.2.0 behavior**: every event goes
to every configured channel. Upgrading from 0.1.1 needs no configuration change.

### Telegram from a restricted network

Some hosts cannot reach `api.telegram.org` directly. Two independent ways out,
both per channel:

```yaml
channels:
  telegram:
    - name: dev-alerts
      bot_token: "123456:ABC-DEF"
      chat_id: "-1001234567890"
      proxy: "socks5://user:pass@127.0.0.1:1080"   # or http://, https://
    - name: customer-alerts
      bot_token: "123456:ABC-DEF"
      chat_id: "-1009876543210"
      api_base: "https://tg-mirror.example.com"    # a trusted Bot API mirror
```

- **`proxy`** — when you have your own HTTP or SOCKS5 proxy and the traffic
  should go through it. Supported schemes: `http`, `https`, `socks5`, and
  `socks5h`. Anything else stops the process at startup with a clear message,
  rather than quietly sending nothing.
- **`api_base`** — when you have a trusted mirror or reverse proxy in front of
  the Bot API.

Notes:

- **MTProto proxies do not work for the Bot API.** They speak Telegram's client
  protocol, not HTTP; pointing `proxy` at one produces an obscure network error.
  Use an HTTP/SOCKS5 proxy or a mirror instead.
- `socks5` and `socks5h` behave **identically** here, and the difference people
  expect does not apply: Go's HTTP transport sends the target **host name** to
  the proxy (SOCKS5 address type `0x03`), so the **proxy resolves DNS**, not
  AlertLoop. Verified by a test with a local SOCKS5 server
  (`TestTelegramSendsThroughSOCKS5Proxy`), not by assumption.
- The setting is per channel, not per process, so a Telegram channel can use a
  proxy while a webhook into your internal network stays direct.
- With `proxy` unset, the process-wide `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`
  variables keep working exactly as before.
- Credentials in the proxy URL are supported, and the password is redacted from
  logs, delivery errors, the API, and the console — as the bot token already is.
- There is no way to disable TLS verification, by design.
- A proxy for the email and webhook channels is not implemented.

### Split deployments (separate server + worker)

The channel configuration is used by **both** roles: the `server` decides which
delivery jobs to create when an event arrives, and the `worker` performs the
actual sending. **Both processes must load the same channel configuration** — if
the server has no channels, no delivery jobs are created and nothing is ever
sent (it will log a prominent warning at startup).

The PostgreSQL Compose profile (`deploy/docker/docker-compose.postgres.yml`)
does this correctly by mounting a **single** `alertloop.yaml` into both the api
and worker containers — one source of truth, no drift. To use it:

```bash
cd deploy/docker
cp alertloop.yaml.example alertloop.yaml    # set admin_token; api_keys & channels optional
docker compose -f docker-compose.postgres.yml up -d
```

The single-process `all` mode (and the SQLite demo) has no such split and needs
no special handling.

## API

The OpenAPI contract lives at `api/openapi.yaml` and is served live at
`/openapi.yaml`, with Swagger UI at `/swagger`.

Documentation lives in two places today: this README (installation, deployment,
configuration, operations) and the interactive Swagger UI at `/swagger` on a
running instance (the full endpoint reference, request/response schemas, and
examples).

## Releases and building

**Download a prebuilt binary** for your OS/arch from the repository's GitHub
**Releases** page — pick `linux_amd64`, `linux_arm64`, `darwin_arm64`,
`windows_amd64`, etc., unpack, and run. Each release also ships a
`checksums_*.txt` to verify the download.

You do **not** need a Windows machine to get a Windows binary (or a Mac for a Mac
binary). AlertLoop is CGO-free (pure-Go SQLite), so it **cross-compiles** — a
single build machine produces every OS/arch at once. Releases are built
automatically by CI when a version tag is pushed (`.github/workflows/release.yml`),
which builds the admin UI, cross-compiles all targets, and uploads them to the
GitHub Release.

To build the release artifacts yourself:

```bash
make admin                 # build the embedded admin UI (needs Node)
make release               # cross-compile all targets into dist/
#   equivalently: VERSION=v0.1.0 ./scripts/build-release.sh
```

For a single local binary for your own machine, just `make build` (Go only).

## Production notes

- **Always set `admin_token`**: if neither `admin_token` nor `api_keys` are set,
  the API accepts unauthenticated requests with **full** scope (a local-demo
  convenience; the process logs a warning at startup). Always set `admin_token`
  on anything reachable by others.
- **TLS**: AlertLoop serves plain HTTP and is designed to run **behind an
  HTTPS reverse proxy** (nginx, Caddy, Traefik). Terminate TLS there and forward
  to the container/port.
- **External PostgreSQL**: use `sslmode=require` (or stricter) in the DSN. The
  bundled Compose Postgres uses `sslmode=disable` only because it is on a private
  Docker network.
- **Rate limiting** is on by default (`rate_limit` in the config). If your proxy
  already rate limits, you can disable it here.
- **SMTP**: set `starttls: true` (required upgrade, port 587) or `tls: true`
  (implicit TLS / SMTPS, port 465). AlertLoop will not send mail in plaintext
  when TLS is requested but unavailable.
- **Secrets**: never commit real `alertloop.yaml` / `.env` (they are gitignored);
  keep the `*.example` templates only.

## License

Community Edition is licensed under **AGPL-3.0-only** (see `LICENSE` and
`NOTICE`). Planned Pro and Enterprise editions will be offered under a separate
commercial license.
