# AlertLoop

AlertLoop is a self-hosted event, incident, and business notification center for
software teams and operations-heavy businesses. Applications and monitors send
events to one API; AlertLoop stores them, deduplicates them, routes them, and
delivers them by email, Telegram, and webhooks.

## Editions

- **Community** (this repository): free, self-hosted, AGPL-3.0-only.
- **Pro Self-hosted** (planned, paid): multi-project, SDKs, WhatsApp, RBAC,
  retention policies, escalation policies.
- **Enterprise** (planned, paid): on-prem license, SSO, HA, audit, custom
  adapters, support.

## Features (Community)

- Events API with scoped API keys (`ingest`, `read`, `full`) and OpenAPI/Swagger.
- Event types `incident`, `business_event`, `audit`; states `new`,
  `acknowledged`, `resolved`, `muted`, `escalated`.
- Deduplication by `dedupe_key`, and an [incident lifecycle](#incident-lifecycle)
  with automatic recovery notifications.
- Email (SMTP), Telegram (optionally through a proxy), HMAC-signed webhooks,
  Slack (also Mattermost and Rocket.Chat), Microsoft Teams, Discord, ntfy, and
  Pushover, several of each; with `public_url` set, every notification links
  to its event in the console.
- [Routing rules](#routing-rules) by type, severity, source, and category.
- Retries with backoff, dead-letter, and replay; a [fallback channel](#fallback-channel)
  for alerts a channel could not deliver; retention cleanup (30 days).
- Admin console at `/admin`, embedded in the binary, with user accounts.
- SQLite (embedded) or PostgreSQL 12+.
- [Monit integration](integrations/monit/) for Linux server monitoring.

Operating it — monitoring, logs, backup, upgrades, runbooks — is in
[OPERATIONS.md](OPERATIONS.md). Reporting a vulnerability is in
[SECURITY.md](SECURITY.md).

## Install and get the first message in Telegram

Both paths below run on a Linux server: Docker Engine with Compose v2, or a
prebuilt binary (linux/amd64, linux/arm64) under systemd. Both end with a
message in a Telegram chat. You need:

- a bot token from [@BotFather](https://t.me/BotFather);
- the chat id: send your bot a message (add it to the group for a group chat),
  then run `curl -s https://api.telegram.org/bot<BOT_TOKEN>/getUpdates` and take
  `chat.id` from the answer. Group ids start with `-`.

### Docker Compose (PostgreSQL)

On the server, as a user who can run `docker`:

```bash
git clone https://github.com/golovanov-dev/alertloop.git && cd alertloop
git checkout "$(git describe --tags --abbrev=0)"    # the latest release
cp .env.example .env
cp alertloop.example.yaml alertloop.yaml
openssl rand -hex 32                                # run twice: two values for .env
nano .env
```

In `.env`, set these three lines. Paste the output of `openssl rand -hex 32`, not
the command: Compose does not run commands in `.env`.

```dotenv
COMPOSE_PROFILES=postgres
ALERTLOOP_ADMIN_TOKEN=<first value>
POSTGRES_PASSWORD=<second value>
```

In `alertloop.yaml`, add these lines directly under the existing `channels:`
line (keep that line, do not add a second one). Write the bot token as a
literal: under Compose a `${VAR}` of your own does not reach the container.

```yaml
  telegram:
    - name: alerts
      bot_token: "123456:ABC-DEF"      # from @BotFather
      chat_id: "-1001234567890"
```

Start it and send a test event:

```bash
docker compose up -d --wait --wait-timeout 120
TOKEN="$(grep '^ALERTLOOP_ADMIN_TOKEN=' .env | cut -d= -f2-)"
curl -X POST http://127.0.0.1:8080/v1/events -H "X-API-Key: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"type":"incident","severity":"critical","source":"test","message":"hello"}'
```

The message arrives in Telegram within a few seconds. If it does not, see
[Events arrive but nothing is delivered](OPERATIONS.md#events-arrive-but-nothing-is-delivered).

- `--wait` fails when a container does not become healthy; `docker compose ps`
  shows which one.
- Switch profiles by editing `COMPOSE_PROFILES` in `.env`, not with `--profile`:
  the flag replaces the value for that one command.
- Port 8080 taken on the host: set `ALERTLOOP_PORT` in `.env`.
- The image is pinned: `ALERTLOOP_IMAGE` in `.env` names the release you run
  (`0.7.0`, without the `v`); without it Compose runs the release of the
  checked-out tag, never `:latest`.
- After editing `alertloop.yaml`, apply it with
  `docker compose up -d --force-recreate --wait --wait-timeout 120 api worker`.

### Binary and systemd

On the server, as a user with `sudo` (use `linux_arm64` on ARM):

```bash
git clone https://github.com/golovanov-dev/alertloop.git && cd alertloop
VERSION="$(git describe --tags --abbrev=0)" && git checkout "$VERSION"
curl -fLO "https://github.com/golovanov-dev/alertloop/releases/download/$VERSION/alertloop_${VERSION}_linux_amd64"
curl -fLO "https://github.com/golovanov-dev/alertloop/releases/download/$VERSION/checksums_${VERSION}.txt"
sha256sum --ignore-missing -c "checksums_${VERSION}.txt"
sudo bash deploy/systemd/install.sh "alertloop_${VERSION}_linux_amd64"
sudo nano /etc/alertloop/alertloop.yaml      # add the Telegram channel shown above
sudo systemctl enable --now alertloop
TOKEN="$(sudo grep '^ALERTLOOP_ADMIN_TOKEN=' /etc/alertloop/alertloop.env | cut -d= -f2-)"
curl -X POST http://127.0.0.1:8080/v1/events -H "X-API-Key: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"type":"incident","severity":"critical","source":"test","message":"hello"}'
```

The installer creates the `alertloop` user, `/etc/alertloop/alertloop.yaml`,
`/var/lib/alertloop` for the SQLite database, the systemd unit, and
`/etc/alertloop/alertloop.env` with a generated admin token. Logs:
`journalctl -u alertloop -f`.

The binary listens on `127.0.0.1:8080` (`addr` in the config); reach it from
outside through the reverse proxy below.

### Try it without configuring anything

In a fresh clone. Where you already set up `.env`, the demo is not for that
directory: `-n` keeps your `.env`, and its `COMPOSE_PROFILES` would start your
own profile instead.

```bash
cp -n .env.example .env   # COMPOSE_PROFILES=demo is preset
docker compose up -d --wait --wait-timeout 120
```

One container on SQLite. Open <http://localhost:8080/admin> and create the first
administrator: `change-me-admin` as the admin token, then a login and a password
of at least 12 characters. The demo takes no channels: it stores events and delivers
nothing. This public token works only from loopback or a private network: from a
public address, or through a reverse proxy that sends `X-Forwarded-For` or
`X-Real-IP` (the examples in `deploy/proxy/` do), it gets 403. Beyond a local
try, set `ALERTLOOP_ADMIN_TOKEN` in `.env` to the output of
`openssl rand -hex 32`.

## Admin console and HTTPS

The admin console is at `/admin`, served by the same process as the API. Sign
in with a login and password; every user is an administrator.

The first time, the console asks you to create the first administrator: the
admin token (`ALERTLOOP_ADMIN_TOKEN` in `.env`, or in
`/etc/alertloop/alertloop.env` under systemd), a login, and a password of at
least 12 characters. The admin token does not open the console after that. Add
more users on the console's Users screen or with `alertloop user`:
[Console users](OPERATIONS.md#console-users).

The binary and the Compose `postgres` profile listen on `127.0.0.1` and speak
plain HTTP, and sign-in over plain HTTP is accepted only from this machine or
its private network reached directly (an SSH tunnel, the Compose gateway).
On a server, reach the console one of two ways.

**With a domain**, through an HTTPS reverse proxy; one rule covers the API,
`/admin`, and `/swagger`. Ready-to-adapt configs: `deploy/proxy/nginx.conf` and
`deploy/proxy/apache.conf`, both forwarding to `127.0.0.1:8080`. Get a
certificate with `certbot`. Set `rate_limit.trusted_proxies` in the config to
the proxy's address, or sign-in through the proxy gets 403, and rate limit on
the proxy too. The proxy must pass the original `Host` header
(`proxy_set_header Host $host;` in nginx, `ProxyPreserveHost On` in Apache;
both shipped configs do) or send `X-Forwarded-Host`, or the console's requests
get 403. Under Compose the proxy arrives from the Compose network
gateway, not `127.0.0.1`:

```bash
docker network inspect alertloop_default -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}'
```

**Without a domain**, through an SSH tunnel from your workstation (a changed
port goes after `127.0.0.1:`), then open <http://localhost:8080/admin>:

```bash
ssh -N -L 8080:127.0.0.1:8080 user@server
```

## Sending events

The API accepts the admin token (full access) or an API key. For each event
source, create a key with the least scope it needs — `ingest` to send events,
`read` for dashboards, `full` for trusted tools — under `api_keys` in the
config (see `alertloop.example.yaml`). Limit an `ingest` key to its sources
with `sources: [web-01]`: it then cannot touch other sources' events. An
`ingest` key gets back only `id`, `state` and `outcome`. The full reference is Swagger UI at
`/swagger`; the contract is `api/openapi.yaml`, also served at `/openapi.yaml`.

### Incident lifecycle

A monitoring source reports `status: firing` while a problem lasts and
`status: resolved` when it ends:

```bash
# The problem starts: creates an incident and notifies. -> 201
curl -X POST http://127.0.0.1:8080/v1/events \
  -H "X-API-Key: $KEY" -H "Content-Type: application/json" \
  -d '{"status":"firing","type":"incident","severity":"critical","source":"monit",
       "message":"PostgreSQL does not respond on port 5432",
       "dedupe_key":"server-01:postgresql:availability"}'

# The same request again updates the same incident and notifies nobody. -> 200

# Fixed: closes the incident and sends a recovery notice. -> 200
curl -X POST http://127.0.0.1:8080/v1/events \
  -H "X-API-Key: $KEY" -H "Content-Type: application/json" \
  -d '{"status":"resolved","dedupe_key":"server-01:postgresql:availability"}'
```

- `dedupe_key` is the incident's identity: keep it stable for one check
  (`host:service:check`), with no timestamps, ids, or measured values in it.
- After a resolve, the same failure opens a new incident and notifies again.
- A `firing` with a higher severity alerts the channels the new severity routes
  to that have not had an alert for this incident; a muted incident alerts
  nobody, and a rise during mute is not sent after unmute either. The same or
  a lower severity notifies nobody.
- A resolve for a key AlertLoop has never seen returns `204`.
- The recovery notice goes to the channels that received the alert and to no
  others. Turn it off with `notify_on_resolve: false`.
- Mute stops notifications about an incident: alerts not sent yet are
  `cancelled`, and an incident closed while muted gets no recovery notice,
  whoever closes it. An alert
  being sent at the moment of mute is cancelled too if that send fails; one
  that already went out is delivered. That send is cancelled only while the
  incident is still muted when it fails: acknowledged or resolved in the
  meantime, the alert is retried, and after a resolve no recovery follows it.
  A `dead_letter` alert is not cancelled: replaying it sends it.
- Without `status`, `dedupe_key` is a plain idempotency key while the event is
  open: a repeat returns the stored event unchanged. Once the event is resolved,
  the same key creates a new event.

### Event state is not delivery state

Delivery state (`sent`, `failed`, `dead_letter`, `cancelled`) says whether a message was
delivered; see it per channel in the console or at `GET /v1/delivery-attempts`.
Event state says whether anyone has dealt with the event, and it changes only
when a person acts on it or a source sends `status: resolved`. A delivered event
stays `new`. Actions apply to incidents only, so for a `business_event` or an
`audit` entry `new` is the normal resting state. `escalate` only marks an
incident as escalated; it sends no notifications.

Do not send `status: firing` for a stream of business events under one
`dedupe_key`: only the first one would notify. Send them without `status`, or
give each its own `dedupe_key`.

## Configuration

One YAML file configures everything: `--config /path/to/alertloop.yaml` (or
`ALERTLOOP_CONFIG`). `alertloop.example.yaml` lists every key there is; a key
AlertLoop does not read stops the start, with its line.

A value written as exactly `${VAR}` or `${VAR:-default}` is taken from the
environment at startup, to keep secrets out of the file:

- only a whole value is substituted; `${VAR}` inside a longer string stays as is;
- an unset or empty variable without `:-default` stops the start;
- the substituted text is data, not YAML.

Under Compose only the variables `docker-compose.yml` passes reach the
container: the admin token, the database settings, and the log file. Under
systemd, put variables in `/etc/alertloop/alertloop.env`.

`server` and `all` need `admin_token` or `api_keys` in the config file; without
either they do not start, and without a config file they do not start either.

### Channels

No channels is a valid setup: events are stored and delivered nowhere. A channel
with a missing required field stops the start. Each type is a list of named
channels; every entry of every type is shown in `alertloop.example.yaml`, and
each takes `timeout` (default 10s) and `fallback`.

| Type | Where to get what it needs | Required keys |
|---|---|---|
| `email` | Your SMTP provider. Port 587 needs `starttls: true`, port 465 `tls: true`. | `host`, `from`, `to` |
| `telegram` | A bot from @BotFather, and the id of the chat it is in. | `bot_token`, `chat_id` |
| `webhook` | Your own receiver. | `url` |
| `slack` | Slack: api.slack.com/apps → Create New App → From scratch → Incoming Webhooks: On → Add New Webhook → pick the channel → copy the Webhook URL. Mattermost: Integrations → Incoming Webhooks. Rocket.Chat: Administration → Integrations → New → Incoming. | `url` |
| `teams` | In the Teams channel: ⋯ → Workflows → "Send webhook alerts to a channel"; copy the URL the workflow shows. | `url` |
| `discord` | Channel settings → Integrations → Webhooks → New Webhook → Copy Webhook URL (needs the Manage Webhooks permission). | `url` |
| `ntfy` | A topic on ntfy.sh or on your server (`server`); subscribe to it in the ntfy app (Android, iOS or web), or nobody sees the messages. With access control: `token` (ntfy.sh: Account → Access tokens; your server: `ntfy token add <user>`), or `username` and `password`. | `topic` |
| `pushover` | pushover.net: the user key on your dashboard, and a token from "Create an Application/API Token". | `token`, `user_key` |

```yaml
channels:
  slack:
    - name: ops-chat          # the same type for Mattermost and Rocket.Chat
      url: "https://hooks.slack.com/services/T000/B000/XXXX"
      fallback: ops-phone
  ntfy:
    - name: ops-phone
      topic: "alertloop-ops-4f9c2e"   # on ntfy.sh, whoever knows the topic reads it
  pushover:
    - name: ops-pager
      token: "APP_TOKEN"
      user_key: "USER_KEY"
```

The `url` of a `slack`, `teams` or `discord` channel lets anyone post to that
channel, and an ntfy `topic` without access control lets anyone read it: keep
them like passwords. Delivery errors show only the scheme and host, and never a
topic, token or key.

Each channel shows the severity its own way; a recovery starts with
`[RESOLVED]` and is green or quiet:

| | critical | error | warning | info, success | recovery |
|---|---|---|---|---|---|
| Slack, Teams, Discord | red | orange | yellow | blue, green | green |
| ntfy priority | 5 | 4 | 3 | 2 | 2 |
| Pushover priority | 1 | 0 | 0 | −1 | −1 |

Text past a service's length limit is cut and ends with `… [truncated]`.

With `public_url` set to the address people open AlertLoop at
(`public_url: "https://alerts.example.com"`, without `/admin`), every
notification links to its event in the console: a `Link:` line in email and
Telegram, a link in Slack, Teams and Discord, the click action in ntfy and
Pushover, and `event_url` in the webhook payload.

A host that cannot reach `api.telegram.org` can use, per Telegram channel,
either a `proxy` (`http`, `https`, `socks5`, `socks5h`) or an `api_base` Bot API
mirror:

```yaml
    - name: alerts
      bot_token: "123456:ABC-DEF"
      chat_id: "-1001234567890"
      proxy: "socks5://user:pass@127.0.0.1:1080"
      # or: api_base: "https://tg-mirror.example.com"
```

MTProto proxies do not work for the Bot API. Without `proxy`, and for every
other channel type, the standard `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`
variables apply; under Docker Compose, set them in `.env`.

A receiver on the same host as a Compose install (your ntfy, Mattermost,
Rocket.Chat or webhook) is not at `127.0.0.1`: inside a container that is the
container itself. Create `docker-compose.override.yml` next to
`docker-compose.yml`:

```yaml
services:
  alertloop:   # demo profile
    extra_hosts: ["host.docker.internal:host-gateway"]
  worker:      # postgres profile
    extra_hosts: ["host.docker.internal:host-gateway"]
```

and put `http://host.docker.internal:<port>` in the channel; the receiver must
listen on an address other than `127.0.0.1`. Without the override file, use the
gateway address of the Compose network instead:
`docker network inspect alertloop_default --format '{{(index .IPAM.Config 0).Gateway}}'`.

A webhook channel POSTs JSON `{"event": {...}, "kind": "alert"|"recovery",
"timestamp": "<RFC 3339>"}`, plus `"event_url"` when `public_url` is set. With `secret` set, each request carries
`X-AlertLoop-Signature-Version: v1` and `X-AlertLoop-Signature`: the lowercase
hex HMAC-SHA256 of the raw request body, keyed by the secret, with no prefix.
The receiver computes the same over the bytes it received and compares in
constant time.

### Fallback channel

A channel can name another channel as its `fallback`. When an alert to it
exhausts its retries (`dead_letter`), the same alert is queued once to the
fallback, opening with the channel that failed and its error:

```yaml
  telegram:
    - name: alerts
      bot_token: "123456:ABC-DEF"
      chat_id: "-1001234567890"
      fallback: ops-mail          # the name of another channel, of any type
  email:
    - name: ops-mail
      # ...
```

- One hop: an alert that reached the fallback and dead-letters there goes no
  further. A fallback to the channel itself or to a name that does not exist
  stops the start.
- Alerts only. Every alert, the copy included, gets its own recovery, sent
  only after that alert went out: the fallback hears "resolved" after the
  copy, even if the incident was already resolved when the copy was queued,
  and the broken channel's recovery waits until its alert is replayed. A
  recovery that dead-letters is not redirected.
- Nothing is redirected for a muted incident.
- The copy goes to the fallback even if it got the alert directly. Without
  routing every channel gets every alert, so the fallback channel may see the
  same alert twice: directly, and as the copy that names the broken channel.
  It then gets two recoveries as well, one for each.
- Whoever reads the fallback channel sees the name of the channel that failed
  and its error. Pick a fallback whose readers may see that. The error is the
  one AlertLoop stores, with channel secrets already removed. A webhook
  fallback gets it as a `fallback` object:
  `{"channel": "<name>", "error": "<text>"}`.
- The dead-lettered alert stays in Deliveries, marked with where it was
  redirected and the state of the copy there (`sent` means it got through);
  replaying it does not redirect it a second time.

A fallback on a different transport (Telegram to email, for example) is what
tells you a channel is broken; without one, only `/v1/stats` and the console
show it.

### Routing rules

Without a `routing` section every event goes to every channel. With one, each
event goes to the channels of the **first** rule it matches:

```yaml
routing:
  rules:
    - name: incidents-to-dev
      match:
        type: [incident]
        min_severity: warning
      channels: [dev-telegram, dev-email]
    - name: orders-to-customer
      match:
        type: [business_event]
        category: ["order.*"]
      channels: [customer-telegram]
  default: [dev-telegram]       # events that matched no rule
```

| Field in `match` | Format |
|---|---|
| `type` | list of `incident`, `business_event`, `audit` |
| `severity` | list of `info`, `success`, `warning`, `error`, `critical` |
| `min_severity` | one level; matches it and above (`success` ranks with `info`) |
| `source`, `category` | list; a trailing `*` is a prefix wildcard |

Values in one field are OR-ed, fields are AND-ed, comparison ignores case.
`channels: []` stores the event and delivers it nowhere. A rule naming an
unknown channel stops the start. Without `default`, unmatched
events are delivered nowhere and logged at `warn`. Check a rule set without
creating anything:

```bash
curl -X POST http://127.0.0.1:8080/v1/routing/preview \
  -H "X-API-Key: $TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"business_event","source":"shop","category":"order.created"}'
```

`GET /v1/routing` returns the table in effect.

## Database

SQLite is the default and needs nothing installed; AlertLoop creates the file.
PostgreSQL is for running `server` and `worker` as separate processes or for a
database you already operate:

```yaml
database:
  driver: postgres
  dsn: "host=HOST port=5432 user=USER dbname=alertloop sslmode=require password=PASSWORD"
```

In this form a value that contains a space or a backslash, or starts with a
single quote, goes in single quotes, with `'` and `\` inside it written as `\'`
and `\\`.

The URL form `postgres://USER:PASSWORD@HOST:5432/alertloop?sslmode=require`
works too; percent-encode `/ ? # @ % space` in its password. Use
`sslmode=require` for a remote database. AlertLoop creates
its tables (`events`, `delivery_attempts`, `schema_migrations`) but not the
database:

```bash
createdb -U postgres -O USER alertloop
```

In a database owned by someone else, grant the right to create tables:
`psql -U postgres -d DBNAME -c "GRANT CREATE ON SCHEMA public TO USER;"`.
There is no migration between SQLite and PostgreSQL.

## Logs

Logs go to stdout: `journalctl -u alertloop -f` under systemd,
`docker compose logs -f` under Compose. `log.file` adds a copy in a file, which
you rotate with logrotate; `log.format: json` suits log collectors. Log files and
rotation: [Reading AlertLoop's own logs](OPERATIONS.md#reading-alertloops-own-logs).

## Runtime modes

`alertloop [--config FILE] MODE`:

- `all` — API and delivery worker in one process (default);
- `server` — HTTP API and admin console;
- `worker` — delivery and retention cleanup;
- `check-db` — check what `server` and `all` check at startup (config,
  credential, database reachable and not newer than the binary, log file
  writable), exit 0 if all pass. It changes nothing;
- `user` — console users: `user add|passwd [--password-stdin] <login>`,
  `user disable|enable <login>`, `user list`.

With `server` and `worker` separate, both must load the same config file and run
the same version. The Compose postgres profile mounts one `alertloop.yaml` into
both.

## Monitoring a Linux server

```bash
cd integrations/monit
sudo ./install.sh --with-examples
```

Monit watches processes, ports, filesystems, load, workers, and cron jobs; each
finding becomes an incident that opens and closes by itself. When AlertLoop
cannot be reached, the adapter keeps the event in a local spool and sends it
later, oldest first. Details:
[integrations/monit/README.md](integrations/monit/README.md).

## Production notes

- Serve it over HTTPS (or reach the console through an SSH tunnel): API keys
  and console passwords travel with requests.
- Alert on `worker_last_tick_at`, `oldest_due_delivery_age_seconds` and
  `dead_letter_last_24h` from `/v1/stats`, and check `/health/ready` from
  another host: see [OPERATIONS.md](OPERATIONS.md).
- Back up before upgrading: migrations run at startup, and downgrading is not
  supported.

## Contributing

Building from source, the admin console, and release builds are in
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

Community Edition is licensed under **AGPL-3.0-only** (see `LICENSE` and
`NOTICE`). Planned Pro and Enterprise editions will be offered under a separate
commercial license.
