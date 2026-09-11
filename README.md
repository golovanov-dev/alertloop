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
- [Incident lifecycle](#incident-lifecycle-firing-and-resolved): a monitoring
  source reports `status: firing` while a problem lasts and `status: resolved`
  when it ends; repeats update the open incident instead of notifying again, and
  a recovery closes it automatically.
- **Recovery notifications**: closing an incident tells the channels that were
  told about it, with how long the outage lasted. On by default.
- [Monit integration](integrations/monit/): watch processes, ports,
  filesystems, load, workers, and cron jobs on a Linux host, and have every
  finding become an incident that opens and closes on its own.
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
- Structured logs (text or JSON) to stdout, optionally copied to a size-rotated
  file at the same time.
- Event retention with automatic cleanup (30 days by default, configurable via
  `retention_days`).
- Health probes under both names: `/health` and `/health/live`, `/ready` and
  `/health/ready`.

Operating it — backup and restore, upgrades, and runbooks for the failures that
actually happen — is in [OPERATIONS.md](OPERATIONS.md). Reporting a
vulnerability is in [SECURITY.md](SECURITY.md).

## Requirements

- **Go**: 1.25 or newer — only to build from source. Release binaries are static
  and need no runtime; there is no CGO dependency.
- **SQLite**: nothing to install — it is embedded (pure-Go driver). This is the
  default and needs no decision.
- **PostgreSQL**, only if you choose it: 12 or newer (14+ recommended, tested on
  16). Uses `JSONB`, partial indexes, and `FOR UPDATE SKIP LOCKED`.
- **Docker** (optional): any recent Docker Engine with Compose v2 for the
  container deployment path.

## Install

There is **one** AlertLoop and **one** configuration file. What differs between
the paths below is only how the process is supervised — and the database is a
single line in that file, not a separate flavour of the product.

Pick by what you are doing:

| You want to… | Path | Database |
|---|---|---|
| see what it is, in a minute | Docker Compose demo | SQLite, thrown away with the volume |
| run it on a server, no Docker | prebuilt binary + systemd | SQLite by default |
| run it on a server, with Docker | Compose + PostgreSQL profile | PostgreSQL |
| split API and workers | Compose + PostgreSQL profile | PostgreSQL (required) |

### Try it (Docker Compose, SQLite)

```bash
cp .env.example .env      # presets COMPOSE_PROFILES=demo
docker compose up -d --wait --wait-timeout 120
```

`--wait` makes Compose wait for the containers' health checks, and fail if they
do not pass. Without it `up -d` reports success as soon as the containers exist,
even if AlertLoop then fails to start and restarts in a loop.

Both deployments live in the single `docker-compose.yml` at the repository root,
selected by profile — `demo` here, `postgres` below. Switch by **editing**
`COMPOSE_PROFILES` in `.env`; do not leave it set and add `--profile` as well,
because Compose combines the two and would start both on the same port. With no
`.env` at all, `docker compose --profile demo up -d` selects one directly.

The images are pulled from GHCR; nothing is built from the compose file.

Then open:

- Admin console: <http://localhost:8080/admin>
- API docs: <http://localhost:8080/swagger>
- Events page: <http://localhost:8080/events?token=change-me-admin> (admin token)

Nothing is configured yet, so events are stored and delivered nowhere — which
is a valid way to run. Add channels when you want notifications.

### Run it on a server without Docker (binary + systemd)

Download a release binary (linux/amd64, linux/arm64; darwin/windows for local
evaluation) and give it a config file:

```bash
cp alertloop.example.yaml alertloop.yaml   # edit: admin_token, channels
./alertloop --config alertloop.yaml all
```

`deploy/systemd/install.sh` turns that into a supervised service (unit file,
`alertloop` user, `/etc/alertloop/alertloop.yaml`, `/var/lib/alertloop`). The
binary is static and CGO-free, so there is nothing else to install — SQLite is
embedded.

### Run it on a server with Docker (PostgreSQL)

```bash
cp .env.example .env                        # then edit it:
                                            #   COMPOSE_PROFILES=postgres
                                            #   ALERTLOOP_ADMIN_TOKEN=$(openssl rand -hex 32)
                                            #   POSTGRES_PASSWORD=$(openssl rand -hex 32)
cp alertloop.example.yaml alertloop.yaml    # edit: channels, routing
docker compose up -d --wait --wait-timeout 120
```

Create `alertloop.yaml` **before** starting. Docker creates a directory in place
of a bind mount whose source is missing, and AlertLoop then fails on "is a
directory".

This profile runs the API and the delivery worker as separate containers against
a PostgreSQL container, with **one** `alertloop.yaml` mounted into both — so the
channel and routing configuration can never drift between them. The database
driver and DSN reach that file from the environment, which is why the same
example config serves this profile and a binary install.

It refuses to start without `ALERTLOOP_ADMIN_TOKEN` and `POSTGRES_PASSWORD`
rather than falling back to placeholders published in this repository. Pin
`ALERTLOOP_IMAGE` to a version tag in production: `latest` moves under you on
the next `docker compose pull`.

Each AlertLoop container has a health check, visible in `docker compose ps`:
the api probes `/health/ready`, and the worker, which serves no HTTP, runs
`alertloop check-db`. That is what `--wait` waits for.

If port 8080 on the host is taken, set `ALERTLOOP_PORT` in `.env`. It moves the
host side of the mapping only, and it applies to both profiles.

### Build from source

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

### Incident lifecycle: `firing` and `resolved`

A monitoring source does not send one-off notifications. It reports that a
problem *is happening*, keeps reporting it while it lasts, and reports when it
is over. Add `status` to say so:

```bash
# The problem starts. Creates an incident and notifies. -> 201
curl -X POST http://localhost:8080/v1/events \
  -H "X-API-Key: $KEY" -H "Content-Type: application/json" \
  -d '{"status":"firing","type":"incident","severity":"critical","source":"monit",
       "message":"PostgreSQL does not respond on port 5432",
       "dedupe_key":"server-01:postgresql:availability"}'

# Still broken, reported every minute. Updates the SAME incident with the newest
# severity and message. No second incident, and nobody is notified again. -> 200
# (identical request)

# Fixed. Closes the incident and records when. -> 200
curl -X POST http://localhost:8080/v1/events \
  -H "X-API-Key: $KEY" -H "Content-Type: application/json" \
  -d '{"status":"resolved","dedupe_key":"server-01:postgresql:availability"}'
```

Rules worth knowing:

- **`dedupe_key` is the incident's identity.** Keep it stable for one logical
  check — `host:service:check`. Never put a timestamp, a random id, a measured
  value, or varying error text in it.
- **Resolving frees the key.** The same failure happening again opens a *new*
  incident and notifies again, which is what you want from a service that
  flaps.
- **A recovery only needs `dedupe_key`.** It identifies an incident to close; it
  does not describe a new event.
- **A recovery for something unknown is a success, not an error.** It returns
  `204` when no event has ever carried that key — the source may be reporting a
  recovery after retention removed the incident, or after restarting without
  having seen the failure.
- **Repeats do not re-notify.** Refreshing an incident creates no delivery
  attempts. A check that fails every minute must not page anyone every minute.
- **An acknowledged incident stays acknowledged** while it keeps firing.
- **Closing an incident sends a recovery notice** to the channels that received
  the alert, and to no others:

  ```
  [RESOLVED] PostgreSQL does not respond on port 5432
    Key:      server-01:postgresql:availability
    Started:  2026-08-22 03:14:07 UTC
    Resolved: 2026-08-22 03:26:37 UTC
    Duration: 12m30s
  ```

  Resolving is deliberately **not** re-routed: routing ran once at ingestion,
  and the audience for "it is fixed" is the audience of "it is broken". A
  channel whose alert dead-lettered is skipped — it never learned there was a
  problem. Repeating a recovery does not notify twice. The manual resolve action
  in the console notifies too. Turn it all off with `notify_on_resolve: false`.

**Without `status`, nothing changes from earlier versions.** `dedupe_key` remains
a plain idempotency key: a repeat returns the stored event untouched. The two
readings of that field are now explicit rather than conflated.

### Event state is not delivery state

Two separate things, kept apart on purpose:

- **Delivery state** — `sent`, `failed`, `dead_letter` — answers *was the
  message delivered*. Visible per channel in `/deliveries` and in
  `GET /v1/delivery-attempts`.
- **Event state** — `new`, `acknowledged`, `resolved`, `muted`, `escalated` —
  answers *has anyone dealt with this*. Visible on the event itself.

An event delivered to every channel stays `new`. That is not a stuck job:
nothing moves an event's state by itself. It moves when a person acts on it
(the buttons in `/admin`, or `POST /v1/events/{id}/ack|resolve|…`), or when a
monitoring source closes an incident with `status: resolved` and the same
`dedupe_key`.

**For a `business_event` or an `audit` entry, `new` is a normal resting state.**
A submitted form, a completed order or an admin action is a fact, not a problem
with an end; there is nothing to resolve. Use the state as a reading mark
("taken care of") if it helps, or leave it — such events are removed by
`retention_days` (30 by default, counted from the last time the event was seen)
either way. The `firing → resolved` cycle exists for `incident`.

One consequence worth knowing before you build on it: **do not send
`status: firing` for a stream of business events under one stable
`dedupe_key`** such as `contact-form`. The first submission opens an event and
notifies; every later one refreshes that same open event and creates no
delivery, so notifications stop with no error anywhere. That behaviour is
correct for a check that fails every minute and wrong for a queue of requests.
Send business events without `status`, or give each one its own `dedupe_key`
(the request id). The same applies to `audit` entries. AlertLoop logs a warning
whenever `firing` arrives on a `business_event` or an `audit` event.

### Monitoring a Linux server

The [Monit integration](integrations/monit/) connects AlertLoop to Monit, which
watches processes, ports, filesystems, memory, load, workers, and cron jobs:

```bash
cd integrations/monit
sudo ./install.sh --with-examples
```

Monit detects; AlertLoop receives, deduplicates, routes, and delivers. AlertLoop
does not grow its own server monitoring, and Monit runs on the host rather than
in a container — it has to see host processes and real filesystems, and it has
to be able to notice that Docker itself has died.

It ships eleven example rules, `install`/`uninstall` scripts, wrappers for cron
jobs and worker heartbeats, and a README that is explicit about what it cannot
do. It is Community, like every inbound integration: what stays paid is the
policy layer on top — escalation policies, on-call schedules, per-project
routing.

## Runtime Modes

The single `alertloop` binary runs in three modes:

- `server` — HTTP API and web UI.
- `worker` — delivery workers and retention cleanup.
- `all` — both in one process (default; ideal for small installs).

`alertloop check-db` is not a mode but a one-shot check: it loads the config,
connects to the database, and exits 0 if it answered. It migrates nothing and
changes nothing in the database. It is the health check of the worker
container. Outside Docker it needs what the service has: the same user, the
variables the config references, and the working directory a relative SQLite
path resolves against. For an install made by `deploy/systemd/install.sh`:

```bash
sudo -u alertloop sh -c 'cd /var/lib/alertloop && set -a &&
  . /etc/alertloop/alertloop.env &&
  exec /usr/local/bin/alertloop --config /etc/alertloop/alertloop.yaml check-db'
```

That reads `alertloop.env` as a shell file, which works for the plain
`NAME=value` lines the installer writes; quote any value you add there that
contains spaces.

## Admin console

AlertLoop ships a React admin console (Overview, Events, Event detail,
Deliveries with dead-letter replay, About). **It is embedded in the binary** and
served at `/admin` on the same origin as the API — nothing extra to install or
run. With either deployment path you simply open `/admin` — but *where* it is
reachable from differs, because the two paths bind the port differently:

- **Local (either path)**: <http://localhost:8080/admin>.
- **Binary / systemd on a server**: AlertLoop listens on `:8080` on all
  interfaces, so it is also reachable at `http://<server-ip>:8080/admin`.
- **Docker Compose on a server**: both profiles publish the port on `127.0.0.1`
  only, so `http://<server-ip>:8080/admin` is refused **by design** — a published
  Docker port is not filtered by a host firewall such as ufw, so binding
  `0.0.0.0` would silently expose the console. Reach it through the reverse
  proxy in `deploy/proxy/`, through an SSH tunnel
  (`ssh -L 8080:127.0.0.1:8080 user@server`, then use the Local URL above), or by
  deliberately publishing it on all interfaces if you accept the exposure — in a
  `docker-compose.override.yml`, with `ports: !override` and `- "8080:8080"`
  under the service. The `!override` tag (Compose v2.24+) is required: without
  it Compose adds your entry to the existing list instead of replacing it.

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
- **Docker Compose**: both profiles publish the port as `127.0.0.1:8080:8080`
  (the host side is `ALERTLOOP_PORT` from `.env`, 8080 by default), so the
  container is reachable from the host only. That is deliberate: a published
  Docker port is not filtered by ufw/firewalld, so binding `0.0.0.0` would put
  the admin console on the public internet without the host firewall noticing.

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

The database is **one line of configuration**, not a different edition of the
product. Both drivers run the same code, the same migrations, and the same
queue; they differ in what they let you do around it.

**SQLite** — the default. Nothing to install: it is embedded (pure Go, no CGO),
and AlertLoop creates the file and its tables on first run. Right for a
single-host install, which is most installs. `:memory:` works for throwaway runs.

```yaml
database:
  driver: sqlite
  dsn: alertloop.db
```

**PostgreSQL** — when you need what SQLite cannot give: the API and the delivery
worker as separate processes (they share the queue through the database), a
database you already operate and back up, or room to grow.

```yaml
database:
  driver: postgres
  dsn: "postgres://USER:PASSWORD@HOST:5432/alertloop?sslmode=require"
```

In the URL form, a password containing `/`, `?`, `#`, `@`, `%` or a space has
to be percent-encoded (`/` as `%2F`, `@` as `%40`), or the URL does not parse.
The keyword/value form needs no encoding, and it is what the Compose postgres
profile uses:

```yaml
database:
  driver: postgres
  dsn: "host=HOST port=5432 user=USER dbname=alertloop sslmode=require password=PASSWORD"
```

A value there that contains a space or a backslash, or starts with a single
quote, goes in single quotes, with `'` and `\` inside it written as `\'` and
`\\`. A DSN AlertLoop
cannot parse stops it at startup with a message that says which form was
expected; the message never repeats the DSN, because the DSN holds the password.

The DSN carries a password, so it is a good candidate for `${VAR}` — see
[Configuration](#configuration). Use `sslmode=require` for a remote database;
`sslmode=disable` only on a local or private-network Postgres.

AlertLoop connects to an **existing database** and creates only its own tables
(`events`, `delivery_attempts`, `schema_migrations`) through migrations that run
at startup. **It does not create the database itself** — create it once:

```bash
createdb -U postgres alertloop
#   or:  psql -U postgres -c "CREATE DATABASE alertloop;"
```

Sharing that database with another application is fine: AlertLoop never touches
tables that are not its own. Just make sure nothing else owns tables named
`events`, `delivery_attempts`, or `schema_migrations`. (A configurable table
prefix is on the roadmap for shared databases.)

Switching later means changing those two lines and starting with an empty
history: there is no migration path between the two engines, and event history
is not carried over.

## Logs

AlertLoop writes structured logs to **stdout**. Set `log.file` to write them to
a file as well — stdout keeps working either way, so `docker compose logs`,
journald and log shippers are never silenced by turning a file on.

```yaml
log:
  level: "info"     # debug | info | warn | error
  format: "text"    # text (human-readable) | json (for Loki/Elasticsearch/Vector)
  file: ${ALERTLOOP_LOG_FILE:-}   # empty = stdout only; a path adds a copy on disk
  max_size_mb: 50   # rotate the file to <file>.1 at this size; 0 disables rotation
  max_files: 5      # rotated files kept besides the active one
```

`file` is written as a reference, exactly as in `alertloop.example.yaml`, and
that form matters under Compose: the api and the worker share one config file,
so the only way to give them separate logs is for the path to come from the
environment (`ALERTLOOP_LOG_FILE_API` / `ALERTLOOP_LOG_FILE_WORKER`). With the
variable unset it is the same as `file: ""`. A plain `file: "/path/to.log"`
works fine for a single process — but then the variable configures nothing, and
AlertLoop says so at startup rather than pretending otherwise.

The file is rotated by AlertLoop itself: at `max_size_mb` the active file
becomes `<file>.1`, older generations shift up, and anything past `max_files` is
deleted. Disk use is bounded by `max_size_mb × (max_files + 1)` — 300 MB with
the values above. Set `max_size_mb: 0` if logrotate or a shipping agent handles
the file instead. The directory is created if it does not exist; AlertLoop must
be able to write to it, and fails at startup saying so if it cannot. If a
rotation fails later, logging continues and the reason is printed once on
stderr.

How to read logs by deployment:

- **Binary (stdout)**: run in a terminal, or redirect: `./alertloop all >> alertloop.log 2>&1`.
- **Binary (file)**: set `log.file` and read it with any tool:
  `tail -f /var/log/alertloop/alertloop.log`.
- **systemd**: logs go to the journal — `journalctl -u alertloop -f`. Set
  `format: json` for machine-readable output that log shippers can parse.
- **Docker**: `docker compose logs -f alertloop` (or `docker logs`). To get
  ordinary files on the host instead, see
  [Reading AlertLoop's own logs](OPERATIONS.md#reading-alertloops-own-logs) in
  OPERATIONS.md — the Compose file has a `./logs` mount and a per-service
  `ALERTLOOP_LOG_FILE_*` variable for it.

Give each process its own file when you run `server` and `worker` separately:
two processes appending to and rotating one file cut each other's history short.

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

The PostgreSQL Compose profile does this correctly by mounting a **single**
`alertloop.yaml` into both the api and worker containers — one source of truth,
no drift. To use it:

```bash
cp alertloop.example.yaml alertloop.yaml    # api_keys & channels optional
COMPOSE_PROFILES=postgres docker compose up -d --wait --wait-timeout 120
```

The single-process `all` mode has no such split and needs no special handling.

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
- **Monitor AlertLoop itself**: alert on `deliveries.dead_letter` from
  `/v1/stats`, and check `/health/ready` from *outside* this host — a service
  that is down cannot report that it is down. See
  [OPERATIONS.md](OPERATIONS.md).
- **Back up before upgrading**: migrations run automatically at startup and are
  not reversible in place. Downgrading is not supported; restoring a backup is.

## License

Community Edition is licensed under **AGPL-3.0-only** (see `LICENSE` and
`NOTICE`). Planned Pro and Enterprise editions will be offered under a separate
commercial license.
