# AlertLoop

AlertLoop is a self-hosted event, incident, and business notification center for
software teams and operations-heavy businesses.

It helps applications send important technical incidents and business events into
one reliable place, track their status, and deliver them through channels such as
email, Telegram, and webhooks. Paid editions add channels such as WhatsApp.

## Product Direction

AlertLoop starts as a self-hosted product. Cloud SaaS is intentionally deferred
until the core product and market positioning are validated.

## Editions

- **Community** (this repository): free, self-hosted, backend API, Swagger/OpenAPI,
  basic events list page, Email/Telegram/Webhook delivery, delivery retries and
  dead-letter replay, SQLite and PostgreSQL.
- **Pro Self-hosted** (paid): multi-project, routing rules, SDKs, WhatsApp,
  Telegram fallback/proxy, RBAC, longer retention, escalation policies.
- **Enterprise** (paid): on-prem license, SSO, HA, audit, custom adapters, support.

## Features (Community)

- Events API with API-key auth and OpenAPI/Swagger.
- Three event families: `incident`, `business_event`, `audit`.
- Event lifecycle: `new`, `acknowledged`, `resolved`, `muted`, `escalated`
  (with a manual `escalate` action).
- Idempotent ingestion via `dedupe_key`.
- Delivery channels: Email (SMTP), Telegram, Webhook (HMAC-signed).
- Delivery worker with retries, exponential backoff, dead-letter, and replay.
- Delivery attempt history, separate from event state.
- Simple events web page protected by an admin token.
- Cursor-paginated list endpoints.
- Structured logs (text or JSON), to stdout or a file.
- 30-day event retention.

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
run. Whether you use the prebuilt binary or Docker Compose, just open `/admin`:

- Local: <http://localhost:8080/admin>
- Remote server: `http://<server-ip>:8080/admin`, or your domain behind a proxy
  (see [Access over a domain](#access-over-a-domain-https)).

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

AlertLoop listens on `:8080` (all interfaces), so on a server it is reachable at
`http://<server-ip>:8080`. For a real domain, put an HTTPS reverse proxy in
front — AlertLoop itself speaks plain HTTP and does not terminate TLS. Since the
API, admin console, and Swagger are all one origin, a single proxy rule covers
everything:

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

Both redirect HTTP→HTTPS and forward to `127.0.0.1:8080`. Get a certificate with
Let's Encrypt (`certbot`). HTTPS is required in production — the admin token
travels with each request and must not go over plaintext HTTP.

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

(Or via environment variables instead of the file:
`ALERTLOOP_DB_DRIVER=postgres ALERTLOOP_DB_DSN="postgres://…" ./alertloop all`.)
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
- **Binary (file)**: set `log.file` (or `ALERTLOOP_LOG_FILE`) and read it with any
  tool: `tail -f /var/log/alertloop/alertloop.log`.
- **systemd**: logs go to the journal — `journalctl -u alertloop -f`. Set
  `format: json` for machine-readable output that log shippers can parse.
- **Docker**: `docker compose logs -f alertloop` (or `docker logs`).

For centralized logging, set `format: json` and point your log collector at the
file or the container's stdout.

## Configuration

Precedence (highest wins): command flags → environment variables → YAML config
file → built-in defaults. See `alertloop.example.yaml` and `.env.example` for the
full list.

### Delivery channels are optional

You can run AlertLoop **with no channels configured** — it will accept and store
events (visible in the API and at `/admin`) and simply deliver nothing. This is
the default in the example configs, so the app starts out of the box. To send
notifications, configure one or more channels (email/telegram/webhook) in the
YAML file. If you enable a channel you must fill in all of its required fields,
or startup fails with a clear message (this catches typos like a missing
`bot_token`).

Every event is delivered to **every** configured channel — Community has no
per-event routing (that is a paid capability).

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
`/openapi.yaml`, with Swagger UI at `/swagger`. Full product documentation will
be published on a dedicated documentation site.

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
`NOTICE`). Pro and Enterprise editions are offered under a separate commercial
license.
