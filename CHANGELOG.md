# Changelog

## Unreleased

### Changed

- The admin console signs in with a login and password instead of the admin
  token. After upgrading, open `/admin`: it asks for the admin token once, to
  create the first administrator. The admin token and `full` API keys stay API
  credentials and no longer open the console. Over plain HTTP, sign-in is
  accepted only from this machine or a private network reached directly (an
  SSH tunnel, the Compose gateway). Behind an HTTPS reverse proxy,
  `rate_limit.trusted_proxies` must list the proxy, and the proxy must pass the
  original `Host` header (nginx `proxy_set_header Host $host;`, Apache
  `ProxyPreserveHost On`, as the shipped configs do) or send
  `X-Forwarded-Host`; otherwise sign-in through it gets 403, and the message
  says which of the two is missing.
- A recovery waits for its own alert, not for every alert of its channel: one
  recovery per alert of the closed incident (cancelled alerts excepted), sent
  only after that alert is `sent`. Before, a direct alert that dead-lettered
  or was cancelled in a fallback channel held that channel's recoveries
  forever. `GET /v1/delivery-attempts` gains `recovery_for`
  (`{id, channel_name, state}` of the alert a recovery waits for). Migration
  0008 adds the column and links recoveries already queued to the alert of the
  same event and channel; after it 0.7.x does not start on the database.
- Monit examples: `install.sh --with-examples` puts the example rules in
  `/etc/alertloop/monit-examples/`, not in Monit's `conf.d` as
  `*.conf.disabled`: Monit on Debian and Ubuntu includes `conf.d/*` and ran
  those files as live checks, paging about services the host does not run.
  Re-run `sudo ./install.sh` from the new version: it removes its own
  `*.conf.disabled` files from `conf.d` (moves ones you edited to the examples
  directory) and lists them; then `sudo monit -t && sudo monit reload`. To
  enable an example, copy it into `conf.d`.
- `cron-wrapper.sh` runs as root, the job through `/usr/sbin/runuser -u <user> --`
  (full path: cron's PATH has no `/usr/sbin`): run
  as another user it cannot read `monit.env` and reported nothing. Rewrite
  crontab lines like `… app /usr/local/bin/cron-wrapper.sh …` as shown in the
  integration's README, "Cron jobs". The adapter and the wrapper now also log
  an unreadable config and a failed report to syslog.
- Webhook and Telegram channels no longer follow HTTP redirects: a redirected
  POST was repeated as a GET, and a 200 to it marked a message nobody received
  as `sent`. A 3xx answer now fails the attempt (`redirect to <scheme://host>
  not followed`) and is retried; point `url` or `api_base` at the final
  address.

### Added

- Channel types `slack` (also for Mattermost and Rocket.Chat incoming
  webhooks), `teams` (a Workflows webhook), `discord`, `ntfy` (ntfy.sh or your
  own server, optional token or login) and `pushover`, several of each, with
  routing, `fallback` and recovery notices like the others. Each shows the
  severity its own way: a colour and an emoji in chat, a priority in ntfy and
  Pushover. `GET /v1/delivery-attempts` returns these values in `channel` and
  accepts them in the `channel` filter: a client that checks `channel` against
  `email`, `telegram` and `webhook` must accept the new values.
- `public_url`: with it, every notification links to its event in the admin
  console, and the webhook payload gains `event_url`.
- Console user accounts with one role, `admin`: a Users screen (add, disable,
  enable, reset another user's password), changing your own password, signing
  out everywhere, and `alertloop user add|passwd|disable|enable|list` for the
  command line and for recovering access. Sessions are kept on the server in an `HttpOnly` cookie,
  which also authenticates `/v1` (`ConsoleSession` in the OpenAPI spec); the
  access log names the user as `credential=user:<id>`. Migration 0006 adds the
  `users` and `sessions` tables.
- A channel may name a `fallback` channel. An alert that dead-letters on it is
  queued once to the fallback, with the failed channel and its error at the
  top of the text (in a webhook, a `fallback` object); the copy gets its own
  recovery, sent after it. The fallback channel may get the alert twice:
  directly, and as the copy that names the broken channel (always so without
  routing), and then two recoveries. A
  fallback alert that dead-letters goes nowhere further, and a fallback to the
  channel itself or to a missing one fails the config check.
  `GET /v1/delivery-attempts` gains `fallback_of` and `fallback_to`, each with
  the linked attempt's `state`, and the console shows whether the redirected
  alert arrived. Migration 0007 adds the link column.
- The Monit adapter keeps an event it could not send (network, 409, 429, 5xx) in
  `/var/lib/alertloop-monit/spool` and sends it, oldest first, on the next run
  or with `alertloop-send --flush`; it then exits with the new code 9. The spool
  holds at most `ALERTLOOP_SPOOL_MAX` events (1000); both `ALERTLOOP_SPOOL_*`
  settings are optional. A spooled event refused for the key or the address
  (401, 403 other than `source_not_allowed`, 404) stays in the spool and the
  run exits 4; one refused for what it is (400, 413, 422, 403
  `source_not_allowed`) moves to `rejected/` in the spool directory with a log
  line, and the events behind it go on; `--flush` exits 4 while `rejected/` is
  not empty. Re-run `install.sh` to create the
  directory, and add the `--flush` timer from the integration's README. The
  adapter now needs `flock` (util-linux); `install.sh` refuses to install
  without it.
- `cron-wrapper.sh` reports `resolved` only on the first success after a
  failure, not after every successful run; the first run of the new wrapper
  for a job still reports one `resolved`, so an incident opened before the
  upgrade closes. Re-run `install.sh` to update it.

## 0.7.0 - 2026-09-23

### Added

- `GET /v1/stats` reports `open_incidents`, `oldest_due_delivery_age_seconds`,
  `dead_letter_last_24h` and `worker_last_tick_at`, so AlertLoop itself can be
  monitored from one reading; OPERATIONS.md, "What to alert on", has the
  thresholds and a check script. Migration 0005 adds the worker's tick table.
- `GET /v1/events?q=<text>` searches event messages, ignoring case.
- The access log carries `client_ip` and, for an authenticated request,
  `credential` (`admin` or the key id: the first 8 hex digits of the key's
  SHA-256) and `scope`. The key itself is never logged.
- A backup script with a systemd timer and a Compose crontab line
  (`deploy/backup/`); OPERATIONS.md, "Backup and restore". `install.sh` keeps
  the binary and unit it replaces as `.prev`.
- Admin console: Overview shows open incidents and delivery problems; message
  search runs on the server; actions follow the server's rules, with
  confirmations; the console works from the keyboard and on a phone.

### Security

- An `ingest` key can be limited to its sources: `sources: [web-01]` under the
  key in `api_keys`. It then touches only those sources' events; anything else
  gets 403 `source_not_allowed`. Until now any `ingest` key could refresh,
  resolve and read any incident by `dedupe_key`. A key without `sources` works
  as before; `sources:` with no value is a config error. Moving Monit hosts
  from one shared `source: monit` to their own keys, keep this order: add the
  host names next to `monit` in routing rules and give each host its own key
  without `sources`; switch `ALERTLOOP_SOURCE` on each host; add `sources` only
  after the incidents opened as `monit` have closed. The adapter does not retry
  a 403, so an event refused in between is lost.
- `POST /v1/events` answers an `ingest` key with only `id`, `state` and
  `outcome` instead of the whole event.
- The demo token `change-me-admin` is refused with 403 through a reverse proxy
  or from a public address. Set your own `ALERTLOOP_ADMIN_TOKEN`.
- A failed webhook delivery no longer stores the webhook URL in `last_error`.
  Errors stored before the upgrade keep it until retention removes them: if
  the URL carries a secret, change it at the receiver.

### Changed

- Event actions apply to incidents only: for `business_event` and `audit` they
  return 409. Resolving an already resolved incident returns 409.
- Muting an incident cancels its unsent alerts: new delivery state `cancelled`.
- A `firing` that raises an open incident's severity alerts the channels the
  new severity routes to that have not had an alert yet.
- Replaying a dead-lettered delivery starts a new cycle of retries.
- `POST /v1/events` returns `outcome`. A body over 512 KiB gets 413, over
  64 KiB for `POST /v1/routing/preview`.
- `GET /v1/events` and `GET /v1/delivery-attempts` return 400 for a filter
  value outside its enum and a `limit` that is not a positive integer;
  `GET /v1/events` also for `source` or `q` that is not valid UTF-8.
- The worker's delivery log lines: `channel` is the channel type, the name is
  in `channel_name`.
- Config: an unknown `log.level` or `log.format` is an error, and the worker
  loads the config as strictly as `server`. `check-db` refuses everything a
  start would refuse, including a missing credential — a worker's own config
  needs one too. An unknown mode is refused before migrations run.
- Every role refuses a database with migrations the binary does not know.
  Going back from 0.7.0 to 0.6.1 still works.
- `alertloop.example.yaml` listens on `127.0.0.1:8080`; the image sets
  `ALERTLOOP_ADDR=:8080`. An existing `alertloop.yaml` keeps its `addr`.
- Compose runs the release it belongs to, not `:latest`, and `.env.example`
  pins `ALERTLOOP_IMAGE`.
- `deploy/proxy/nginx.conf` uses one rate-limit zone for every path.
- The Monit self-check example no longer waits for a pidfile the unit does not
  write (it restarted a healthy AlertLoop in a loop). Copy it again if you use it.

### Removed

- Admin console `config.js` and its undocumented `apiBase` setting.

### Fixed

- One channel that hangs or is down no longer delays notifications to the other
  channels. The 0.4.0 entry called this fixed; it was not. With
  `worker.concurrency: 1` there is no spare slot: keep it at 2 (the default)
  when you have several channels; the worker warns at startup.
- Two earlier entries claimed more than the code did: a muted incident closed
  by its source sent a recovery notice (0.4.0), and `check-db` missed several
  startup refusals (0.6.0). Both now hold.
- 30 smaller fixes: 3 security, 9 reliability, 13 in the admin console,
  5 in the documentation.

## 0.6.1 - 2026-09-21

The first container image of the 0.6 line. AlertLoop itself is the same as in
0.6.0.

**Upgrading from 0.5.x:** read "Before upgrading" under 0.6.0 in
`CHANGELOG.md` first; everything there applies to this version. A Compose
deployment that runs the default image (`:latest`, no `ALERTLOOP_IMAGE` in
`.env`) moves from 0.5.3 to this version on its next `docker compose pull`.

### Fixed

- The container image for 0.6.0 was not published: its `linux/arm64` build
  failed. 0.6.1 publishes the image for `linux/amd64` and `linux/arm64`; pin
  `ghcr.io/golovanov-dev/alertloop:0.6.1`, since `:0.6.0` does not exist. The
  0.6.0 binaries are unaffected.

## 0.6.0 - 2026-09-21

A setting AlertLoop does not read now stops the start instead of being
ignored, AlertLoop no longer serves its API without a credential, and a
recovery no longer arrives before its alert. `cors_origins`, the log rotation
settings, the built-in `/events` and `/deliveries` pages and the container
entrypoint script are removed.

**Before upgrading:** delete `cors_origins`, `log.max_size_mb` and
`log.max_files` from your `alertloop.yaml` if it has them, make sure it sets
`admin_token` or `api_keys`, and run `check-db` with the new version before
restarting, as "Upgrades and downgrades" in `OPERATIONS.md` describes: it
names every key the new version refuses. If you run `server` or `all` without
`--config`, or with a config that sets neither credential, give it a config
file with `admin_token` or `api_keys`: it no longer starts without one. If a
script or client passes the admin token as `?token=` or in the `X-Admin-Token`
header, move it to the `Authorization: Bearer` or `X-API-Key` header; links and
bookmarks to `/events` and `/deliveries` answer 404, use the admin console at
`/admin` instead.

### Changed

- A config file that contains a key AlertLoop does not read **no longer
  starts**. Such a key used to be ignored, so `retention_day` ran on the
  built-in 30 days and `trusted_proxy` left the trusted-proxy list empty: the
  setting was written in the file, it was not in effect, and nothing said so.
  The load now stops and lists every unknown key at once, each with its full
  path (`rate_limit.trusted_proxy`), its line, and the spelling that was
  probably meant. Keys inside sections and inside list entries are checked too.
  Compare your `alertloop.yaml` with `alertloop.example.yaml` before upgrading,
  and run `check-db` with the new version before restarting ("Upgrades and
  downgrades" in `OPERATIONS.md`): it loads the config and names what it
  refuses. A top-level key starting with `x-` is still left alone: it holds
  YAML anchors, as it does in Compose.
- A setting written as a path on one line (`log.level: debug`) is one of those
  unknown keys. YAML has no key called `log.level`, so such a line never set the
  log level; the refusal says that a key name is not a path.
- `server` and `all` **no longer start without a credential** — neither
  `admin_token` nor `api_keys` — whether they were given a config file or
  none at all. With neither, the JSON API and the admin console accepted every
  request from anyone who could reach the process, with full scope, and the only
  sign was one warning in the log. There is no open mode any more: a request
  without a credential is always refused. A `worker` has no listener and is not
  stopped. To start, set `admin_token` or `api_keys` in the config file; a
  binary run without `--config` needs one now too, for example
  `ALERTLOOP_ADMIN_TOKEN=<token> alertloop --config alertloop.example.yaml all`.
- `${VAR}` is substituted in **values** only. A reference standing in a key
  position is now the key it literally is, which means an unknown one.
- The error for an unset variable names the **field** as well as the variable
  and the line (`admin_token — ${ALERTLOOP_ADMIN_TOKEN}`), and it no longer
  suggests a `${VAR:-default}` fallback for `admin_token`, which deliberately
  has none.
- Comments in `alertloop.example.yaml`, `.env.example` and
  `docker-compose.yml` are shorter; the remaining settings and their defaults
  are unchanged.
- `README.md` and `OPERATIONS.md` are shorter and describe the current
  version only. The upgrade notes for past versions live in this changelog;
  `OPERATIONS.md` keeps one upgrade procedure for every version.

### Removed

- `cors_origins`. The admin console is served on the same origin as the API
  and needs no CORS. A config file that still contains the key no longer
  starts, like any other unknown key: delete the line. A browser app of your
  own on another origin can reach the API through a reverse proxy that serves
  both on one origin.
- `log.max_size_mb` and `log.max_files`: AlertLoop no longer rotates its log
  file. A config file that still contains either key no longer starts, like any
  other unknown key: delete both lines. If you set `log.file`, rotate it with
  logrotate and `copytruncate`; the rule for the binary and for `./logs` under
  Compose is in `OPERATIONS.md`, "Reading AlertLoop's own logs". `log.file`
  itself is unchanged.
- The startup warning about undelivered attempts whose channel is not in the
  config. The worker already fails each such attempt with
  `no channel configured with name ...`, and it ends in the dead-letter queue
  once its retries run out.
- The built-in pages `/events`, `/events/{id}` and `/deliveries`. The admin
  console at `/admin` shows the same events, event details and delivery
  attempts, with replay. With the pages go the `?token=` query parameter and the
  `X-Admin-Token` header; the admin token now travels only in the
  `Authorization: Bearer` or `X-API-Key` header, never in a URL. Old links and
  bookmarks to the pages answer 404.
- The container entrypoint script. The image runs `alertloop` directly, so
  `docker run` without `ALERTLOOP_ADMIN_TOKEN` stops with the loader's own
  error: the variable is unset, at `admin_token` on line 2 of
  `/etc/alertloop/alertloop.yaml`. Arguments after the image name reach
  `alertloop` as before.

### Fixed

- A recovery notice no longer arrives before its alert. If the alert to a
  channel was waiting for a retry when the incident closed, the recovery went
  out first and "resolved" was followed by the alert. The recovery now waits
  until that alert is sent; if the alert dead-letters, the recovery waits for
  it to be replayed. A channel whose alert had already dead-lettered when the
  incident closed now gets its recovery too, after the replayed alert; before,
  a replay delivered the alert and no recovery ever followed.
- A `${VAR}` reference whose variable holds the text `null`, `Null`, `NULL` or
  `~` erased the field instead of filling it: YAML reads those four as "no
  value". `admin_token: ${ALERTLOOP_ADMIN_TOKEN}` with such a value started
  AlertLoop with an *empty* admin token — the API open — and the same applied
  to any other field. It is easy to arrive there without noticing: `jq -r` and
  most template engines print `null` for a key they cannot find. Such a value
  now reaches string fields as text, and in non-string fields it stops the
  start with a type error instead of silently leaving the built-in default.
  This applies to the value that came **from the variable**: a default written
  in the file (`log.file: ${VAR:-~}`, `routing: ${VAR:-null}`) still means YAML
  null, as it always did.
- A `${VAR:-default}` whose variable is present but **empty** now logs a warning
  naming the field and the variable, because the value from the file is what
  runs. A blanked `ALERTLOOP_DB_DSN` used to start a healthy-looking process on
  a local SQLite file while its operator was certain it was on PostgreSQL. The
  warning gives the field, the variable and the line, never the value — a
  Telegram proxy URL and a webhook URL both carry credentials — and a variable
  that is simply absent says nothing.
- `alertloop check-db` names the version that answered
  (`ok: alertloop v0.6.0, the database answered`). It is the pre-flight check of
  an upgrade, and run from a directory still pointing at the old image it
  answered `ok` about a config the new version refuses, with nothing in the
  output to say which version had spoken.

## 0.5.3 - 2026-09-11

Corrections to the example, deployment and integration files, the README and
`OPERATIONS.md`. Nothing in AlertLoop itself changed.

**Check your `.env`:** if `ALERTLOOP_ADMIN_TOKEN` reads
`$(openssl rand -hex 32)`, that text is your admin token, and it is published
in this repository. Put the output of `openssl rand -hex 32` in its place, run
`docker compose up -d --wait --wait-timeout 120`, and give the new token to
everyone who uses it. The same goes for a `key:` in `alertloop.yaml` with that
text, which the Monit installer suggested: replace it, restart AlertLoop, and
put the new key in `/etc/alertloop/monit.env`.

### Fixed

- `alertloop.example.yaml` shipped `channels: {}` above the commented-out
  channels, so uncommenting one failed to parse ("did not find expected key").
  It is now `channels:`. If your `alertloop.yaml` was copied from it, drop the
  `{}` there too before enabling a channel.
- `alertloop.example.yaml` advised keeping channel and API-key secrets in `.env`.
  Under Compose only the variables `docker-compose.yml` passes reach the
  containers, so such a `${VAR}` stopped startup as unset. The comments now say
  where variables come from: the systemd `EnvironmentFile`, or, under Compose,
  the value written into the file.
- `rate_limit.trusted_proxies` behind a proxy under Docker must list the Compose
  network gateway, not `127.0.0.1`; `alertloop.example.yaml`, `nginx.conf` and
  `apache.conf` now say so and show how to find it. If yours lists `127.0.0.1`
  under Compose, as every earlier example advised, replace it with the gateway
  address: until then every client behind the proxy shares one rate-limit
  bucket.
- `deploy/proxy/nginx.conf` and `apache.conf` put the certificate step first:
  both reference it, so `nginx -t` / `configtest` fail until it exists.
- `README.md` said AlertLoop's rate limiter could be turned off when the proxy
  already rate limits; the proxy examples call both necessary, and the README
  now agrees.
- `.env.example`, `docker-compose.yml` and `README.md` said `--profile`
  combines with `COMPOSE_PROFILES`; it replaces it for that command. `.env` is
  read from the directory of `docker-compose.yml`, not from where you run the
  command. A missing `alertloop.yaml` fails with Docker's own mount error, now
  quoted.
- `OPERATIONS.md`: the restore checklist looked for a `migrations applied` log
  line AlertLoop does not write, and its backup and runbook commands passed
  `--profile postgres`, which the Compose files tell you not to. Its backup
  check used port 8080, where the production process would answer the `curl`,
  and trusted `/health/ready`, which an empty database passes; it now uses a
  port of its own and compares `/v1/stats`. Under Docker a second clone is the same
  Compose project unless `COMPOSE_PROJECT_NAME` differs, and the check now says
  so before anything is started.
- `deploy/systemd/install.sh` was committed without the executable bit, so
  `sudo ./deploy/systemd/install.sh` failed with "Permission denied" in a fresh
  checkout. `sudo bash deploy/systemd/install.sh` works with any version.
- `README.md` showed `ALERTLOOP_ADMIN_TOKEN=$(openssl rand -hex 32)` as a line
  to write into `.env`, from 0.4.0 on. Compose does not run commands there, so
  the admin token became that literal text. The same line for the database
  password failed loudly; once that was fixed, nothing warned about the token.
  It now shows a placeholder.
- `README.md` created the PostgreSQL database without an owner; on PostgreSQL 15
  and later AlertLoop's user then cannot create its tables. The command now sets
  the owner; for a database owned by another application it shows the `GRANT`
  instead.
- `integrations/monit/install.sh` printed the example key as
  `key: "$(openssl rand -hex 32)"`; pasted as shown, that literal became a
  working key known to everyone. It now prints a placeholder.
- `integrations/monit/README.md` showed `monit.env` with comments after the
  values; the adapter reads them as part of the value. `alertloop.env.example`
  said `ALERTLOOP_HOST` sets the host in the `dedupe_key`; it is only the host
  label on events you send with `alertloop-send` yourself without `--host`.
  Events from the Monit rules take the host Monit reports. If your `monit.env`
  has a comment after a value, move it to a line of its own.

## 0.5.2 - 2026-09-11

One fix, in the example config: the documented way to pin the image named a tag
that does not exist. Nothing in AlertLoop itself changed.

### Fixed

- `.env.example` showed the image pin as `ghcr.io/golovanov-dev/alertloop:v0.5.1`.
  Images are published without the `v` (`:0.5.1`); the `v` form has never
  existed, so following the example failed with "manifest unknown". If you
  pinned the image that way, drop the `v`.

## 0.5.1 - 2026-09-11

A database password with `/` in it no longer takes the Compose deployment down,
and the documented way to start the stack now fails when AlertLoop does not come
up.

**Upgrading a Compose deployment:** the postgres profile now hands the password
to AlertLoop exactly as written in `.env`. If you percent-encoded
`POSTGRES_PASSWORD` to get the old URL working (`%2F` for `/`), write the
decoded password there before upgrading, or authentication fails;
`grep '^POSTGRES_PASSWORD=.*%' .env` finds the case. A password that begins
with a single quote no longer parses: AlertLoop refuses to start and says why;
change the password as described in "Changing the database password" in
`OPERATIONS.md`.

### Added

- `alertloop check-db`: loads the config, pings the database, exits non-zero if
  it does not answer. It is the worker container's health check.
- `ALERTLOOP_PORT` in `.env` sets the host port both Compose profiles publish
  on `127.0.0.1` (default 8080).
- When PostgreSQL refuses a password that contains `%XX`, the error says the
  keyword/value DSN sends it as written and points to the upgrade notes.

### Changed

- Before 1.0.0 only the latest minor line is supported: 0.4.x no longer
  receives fixes. See `SECURITY.md`.
- **The Compose postgres profile passes the database password in a
  keyword/value DSN instead of a `postgres://` URL**, which a password
  containing `/`, `?` or `#` broke. A password that worked before keeps
  working, except one percent-encoded for the URL or one beginning with a
  single quote — see the upgrade note above.
- `docker compose up -d --wait --wait-timeout 120` is the documented way to
  start: it fails when a container does not become healthy, where plain `up -d`
  reports success as soon as the containers exist.
- The api and demo containers run their `/health/ready` check every 10 seconds
  instead of 30, declared in `docker-compose.yml`.
- A URL DSN with an unencoded `@` in the database name, the fragment, or a query
  parameter name is refused at startup — it is what a misplaced password looks
  like. Write it as `%40`.
- The docs and `.env.example` generate the database password with
  `openssl rand -hex 32` instead of `-base64`.

### Fixed

- A `POSTGRES_PASSWORD` containing `/` — which the previously documented
  `openssl rand -base64 32` produces about half the time — made the api and
  the worker restart in a loop while `docker compose up -d` reported success.
- An unparsable PostgreSQL DSN now stops startup with the likely cause and the
  keyword/value alternative, instead of failing later inside the migrations.
- Under Compose the worker was permanently `unhealthy`: it inherited the image's
  HTTP health check and serves no HTTP.
- `/health/live` and `/health/ready` were written to the access log at info on
  every probe; like `/health` and `/ready`, they are logged at debug now.
- Lists in the admin console and the delivery table on `/events/{id}` showed the
  time of day without the date. They show `YYYY-MM-DD HH:MM:SS` now, in the zone
  their column header names.
- `.env.example` suggested pinning the image to `v0.4.0`, a version with a known
  vulnerability; it names the current release now.

### Security

- A DSN error no longer carries part of the password. The driver's parse error
  quoted the text around the break — for a URL password containing `/`, the part
  before it — and a URL that parsed wrongly put the rest of the password into the
  connection error as the database name. AlertLoop now reports DSN problems
  without quoting the DSN.

## 0.5.0 - 2026-09-09

What the first real installation turned up: logs you can actually read, and an
example `.env` that no longer hands out a working admin token.

**Upgrading a Compose deployment:** to use the new per-service log files, add
`file: ${ALERTLOOP_LOG_FILE:-}` under `log:` in your own `alertloop.yaml`. A
config copied from an older example does not reference the variable, and the
environment configures nothing the file does not ask for (0.3.0) — AlertLoop
says so at startup rather than ignoring it. Details in `OPERATIONS.md`,
"Docker: plain log files on the host".

### Added

- `log.max_size_mb` (default 50) and `log.max_files` (default 5): the log file
  is rotated by size, oldest generations dropped. Set `max_size_mb: 0` to leave
  rotation to logrotate.
- The directory for `log.file` is created if it does not exist.
- Per-service log files on the host under Compose, from `ALERTLOOP_LOG_FILE_API`
  and `ALERTLOOP_LOG_FILE_WORKER` into the mounted `./logs`. Off unless set.
- `LogsDirectory=alertloop` in the systemd unit, so `log.file` under
  `/var/log/alertloop` works with no extra setup.
- A warning when `status: firing` arrives on a `business_event` or an `audit`
  event: repeats of that `dedupe_key` refresh the open event and deliver
  nothing, so only the first report ever notifies.
- `OPERATIONS.md`: "Reading AlertLoop's own logs" — how to get at the logs in
  each deployment, and the ownership traps under Docker and systemd.

### Changed

- **`log.file` now writes to the file *and* stdout, instead of the file only.**
  If you have it set, stdout starts carrying the same lines again — which is
  what makes `docker compose logs`, the journal, and log shippers work while a
  file is configured.
- Container logs are capped at 10 MB x 3 files per service in
  `docker-compose.yml`; nothing bounded them before.
- A rotation that fails no longer stops logging: the reason is printed once on
  stderr and writing continues to the open file.

### Security

- **`.env.example` ships `ALERTLOOP_ADMIN_TOKEN=` and `POSTGRES_PASSWORD=`
  empty.** The documented `cp .env.example .env` used to supply a placeholder
  published in this repository, which satisfied the "refuses to start without a
  token" guard and left production running on a known credential. **If you
  copied `.env.example` before this release, generate real values now**
  (`openssl rand -hex 32`). The loopback-only demo profile is unaffected.
- The example config no longer ships a working `api_keys` entry; the block is
  commented out.

### Fixed

- Documented that event state is not delivery state: an event delivered to every
  channel stays `new` until someone acts on it, and for `business_event` and
  `audit` that is a normal resting state. In the README and in the OpenAPI
  descriptions of the event state, `status` and `dedupe_key`.

## 0.4.3 - 2026-08-24

One fix: an example Monit rule did not parse. Nothing in AlertLoop itself
changed.

### Fixed

- **`integrations/monit/conf.d/filesystem.conf` was rejected by `monit -t`.**
  It used `if read-only then ...`, which is not Monit syntax. Monit reports a
  filesystem being remounted read-only through `if changed fsflags`, which
  detects a *change* of mount flags rather than a state — and therefore has no
  `else if succeeded` counterpart. Anyone who enabled this example could not
  reload Monit at all until they fixed the file themselves, because the
  installer correctly refuses to reload on a configuration Monit rejects.

  The corrected rule is documented in place, including the consequence: this is
  the one incident in the integration that does not close by itself. Closing it
  is a console action, or an `alertloop-send --status resolved` from whatever
  fixes the mount. A `check program` writability probe is named as the
  alternative for anyone who wants it to auto-resolve.

  All eleven example rules now pass `monit -t` in CI — a step that had never
  actually run before this release: the job died earlier, first on `shellcheck`
  and then on the missing executable bit.

## 0.4.2 - 2026-08-24

One fix: the integration's scripts were shipped without the executable bit.
Nothing else changed. If you installed 0.4.1 through `install.sh` it already
set the mode on copy, so a working installation stays working; this matters if
you run anything from the repository checkout.

### Fixed

- **`integrations/monit/*` and `scripts/*` were committed as non-executable.**
  They were authored on a machine with `core.filemode=false`, where `chmod +x`
  never reaches git's index, so the recorded mode stayed `100644` and a Linux
  checkout produced `Permission denied` for every one of them. In practice:
  `sudo ./install.sh` failed, and so did running `alertloop-send` from a clone.
  CI now asserts the recorded mode rather than the mode on disk, because git's
  mode is what a clone actually gets and it is checkable from any platform.

## 0.4.1 - 2026-08-24

Fixes found by CI on the 0.4.0 tag, plus the dependency vulnerability those
checks exist to catch. Upgrade from 0.4.0; nothing about your configuration or
your stored events changes.

### Fixed

- **A vulnerable `golang.org/x/text` was compiled into the 0.4.0 binaries and
  image** (GO-2026-5970, infinite loop on invalid input, reachable through the
  database driver). Updated to v0.39.0. `govulncheck` on the release toolchain
  now reports nothing.
- **The Monit adapter refused `http://[::1]:8080`** as an insecure URL. In a
  shell `case`, `[::1]` is a bracket expression matching one character out of
  `:` and `1`, not the literal text — so an IPv6 loopback address never matched
  the localhost arm and was treated as a remote plain-HTTP endpoint. An
  installation reaching AlertLoop over IPv6 loopback could not send anything.
  Found by `shellcheck`, which had never actually run.
- **Three CI checks could not pass on 0.4.0**, so 0.4.0 shipped with a red
  pipeline:
  - `golangci-lint` used action v6, which runs golangci-lint v1 only, while
    `.golangci.yml` is a v2 config. The action's major version and the linter's
    are coupled; both are pinned now, with a comment saying so.
  - `shellcheck -S style` reported five findings, one of them the IPv6 defect
    above and the rest genuine (`A && B || C` read as if-then-else, and a jq
    program fragment that needed an explicit exemption rather than a shrug).
  - `govulncheck` failed on the `x/text` advisory above.

  All three now run locally against the same toolchain versions CI uses, which
  is how they should have been checked before 0.4.0 was tagged.

## 0.4.0 - 2026-08-24

AlertLoop learns the full incident lifecycle - a problem opens an incident,
keeps it updated while it lasts, and closes it with a notice saying how long it
went on - and ships its first integration with a monitoring system, Monit. The
Docker delivery path becomes a published image instead of a build from source.

**Upgrading the binary or the image needs nothing**: two migrations run
automatically at startup, in a transaction, and backfill every existing row.
Nothing about your stored events changes.

**On a large PostgreSQL installation, plan for the migration to take time.**
Migration 0003 rewrites every row in `events` to fill `last_seen_at`, and
rebuilds the `dedupe_key` index — inside one transaction, so the table is locked
against writes for the duration and the WAL grows by roughly the size of the
table. Check what you are in for before upgrading:

```sql
SELECT count(*) FROM events;
```

Up to a few hundred thousand rows this is seconds. In the millions, do it in a
maintenance window, and make sure event sources will retry — ingestion returns
5xx while the table is locked. SQLite has the same work to do but no
concurrency to block. Both are covered by the upgrade test in CI, which runs a
real v0.1.0 database forward on both engines.

**Upgrading a Docker Compose deployment needs three deliberate steps**, because
the Compose layout changed. Read "How to upgrade a Compose deployment" below
before pulling — one of them, if skipped, makes a working PostgreSQL
installation come up with an empty database.

### Added

- **Incident lifecycle on ingestion.** `POST /v1/events` accepts `status`:
  - `firing` — the problem is happening now. A repeat for the same `dedupe_key`
    **updates the open incident** with the newest severity, message, and payload
    and moves `last_seen_at` forward. It creates no new delivery attempts, so a
    check that fails every minute does not notify anyone every minute. An
    incident an operator has acknowledged stays acknowledged.
  - `resolved` — the problem is over. Closes the open incident carrying that
    `dedupe_key` and records `resolved_at`. It needs only `dedupe_key`. A repeat
    is idempotent, and a recovery for a key nothing was ever stored under
    returns `204` rather than an error: the source may be reporting a recovery
    after retention removed the incident, or after restarting.

  Requests **without** `status` behave exactly as before — `dedupe_key` stays a
  plain idempotency key and a repeat returns the stored event untouched. This is
  covered by a regression test.
- **Recovery notifications.** Closing an incident tells the channels that were
  told about it:

  ```
  [RESOLVED] postgresql: Connection failed
    Started:  2026-08-22 03:14:07 UTC
    Resolved: 2026-08-22 03:26:37 UTC
    Duration: 12m30s
  ```

  It goes to the channels that received the alert **and to nobody else** —
  resolving is not re-routed, because routing already ran at ingestion and the
  rules may have changed since. A channel whose alert dead-lettered is skipped:
  it never learned there was a problem. A channel whose alert is still queued is
  included: it will be delivered, and its recipient would otherwise be left with
  a problem that never ended.

  Both paths notify — an ingested `status: resolved` and the manual resolve
  action. **This changes what the resolve action does:** in 0.3.x it was silent.
  Repeating a recovery does not notify twice. Turn it off with
  `notify_on_resolve: false`.
- **Delivery attempts record what they announce** (`kind`: `alert` or
  `recovery`), in the API, the built-in `/deliveries` page, and the admin
  console, and as a `?kind=` filter. Webhook payloads carry it as a top-level
  `kind` field, so a receiver can close its own ticket instead of opening a
  second one. Attempts created before 0.4.0 read as `alert`.
- **The Monit integration**, under `integrations/monit/` — the first inbound
  integration, and **Community**, not paid:

  - `alertloop-send`, a shell adapter with bounded retries, timeouts, a
    documented exit-code contract, `--dry-run`, and no secret in its process
    arguments or its logs;
  - `alertloop-monit`, the glue that reads Monit's own `MONIT_*` variables so
    the example rules stay one line long;
  - eleven example rules: system resources, filesystem and inodes, PostgreSQL,
    PHP-FPM, HTTP health, worker process, worker heartbeat, cron exit code, cron
    freshness, Docker, and AlertLoop itself;
  - `cron-wrapper.sh` and `check-worker-heartbeat.sh`, which cover the two
    failure modes process checks cannot see: a job that failed, and a job that
    never ran;
  - `install.sh` / `uninstall.sh` that never overwrite an existing
    configuration, install the examples **disabled**, and refuse to reload Monit
    on a configuration it rejects;
  - 85 tests and `shellcheck -S style` in CI, plus `monit -t` against every
    shipped rule.

  It runs on the host, never inside AlertLoop's container, and its README is
  explicit about what it cannot do — including that a local Monit cannot report
  through an AlertLoop that is itself down.
- **`notify_on_resolve`** in the config file, default true. It is read as a
  pointer, so a config file written before 0.4.0 gets recovery notices without
  being edited.
- **`rate_limit.trusted_proxies`** — the addresses whose `X-Forwarded-For` may
  be believed when identifying a client for the per-IP limit. Empty by default,
  which is right for a directly exposed instance and wrong behind a proxy: set
  it to your proxy's address, or the limiter counts every request as coming
  from the same client. `deploy/proxy/nginx.conf` now ships with `limit_req`
  configured as well; the two are complementary.
- **Health probes are exempt from the per-IP limit**, in the app and in the
  shipped nginx configuration. A Docker HEALTHCHECK, an orchestrator probe, and
  an external uptime check all come from one address, and throttling them would
  make "is it up?" stop answering during an incident — which is when it is
  asked.
- **Release artifacts and the container image are signed with cosign**
  (keyless, verifiable against this repository), and the image carries an SBOM
  and provenance attestation.
- **The systemd unit is hardened to match the container**: no capabilities, no
  device access, no kernel tuning or module loading, a restricted syscall set,
  and `RestrictAddressFamilies` limited to what a network service needs.
- **`last_seen_at` and `resolved_at` on the event**, in the API, the built-in
  event page, and the admin console. Existing rows are backfilled: `last_seen_at`
  from `created_at`, and `resolved_at` from `updated_at` on events that were
  already resolved.
- **`/health/live` and `/health/ready`** as aliases of `/health` and `/ready`,
  which keep working. Monitoring tools are overwhelmingly configured against the
  sub-path form.
- **A published container image at `ghcr.io/golovanov-dev/alertloop`**, built for
  `linux/amd64` and `linux/arm64` on every version tag. `latest` follows stable
  releases only; a pre-release tag does not move it.
- **`SECURITY.md`** — supported versions, how to report a vulnerability
  privately, what is in scope, and the security properties a break in which is a
  vulnerability.
- **`OPERATIONS.md`** — backup and restore for both databases, the upgrade and
  downgrade policy, how to monitor AlertLoop itself, and runbooks for a channel
  that has been down for a day, a full disk, an unreachable database, and events
  that arrive but are never delivered.
- **PostgreSQL integration tests.** Until now every test ran on SQLite while
  PostgreSQL was the production database. CI now runs the storage suite against a
  real PostgreSQL — including a concurrency test that proves two workers never
  claim the same delivery, which is the `FOR UPDATE SKIP LOCKED` path SQLite
  never executes — and fails if those tests are skipped rather than run.
- **An upgrade test from v0.1.0** (`scripts/upgrade-test.sh`), run in CI on both
  SQLite and PostgreSQL. It builds the real v0.1.0 binary from its tag, writes a
  database with it, starts the current build against that same database, and
  checks the events, their payloads, and the backfilled columns.
- **golangci-lint and govulncheck in CI**, plus validation of the Compose file
  for both profiles.

### How to upgrade a Compose deployment

Do this before `git pull`, or at least before `docker compose up`.

**1. Write down your current Compose project name and volumes.**

```bash
docker volume ls | grep -i alertloop
docker compose ls
```

This matters because the compose file now sets `name: alertloop` explicitly,
while before it inherited the project name from **the directory the compose
file was in**. For the PostgreSQL profile that directory was `deploy/docker`,
so the project was called `docker` and the volume `docker_pgdata`. After the
upgrade AlertLoop looks for `alertloop_pgdata`, finds nothing, and initialises
an empty database. The old volume is still there and still intact — but a
service that starts empty and healthy is the worst way to find out.

Two ways through it. **Back up and restore** (safe, and you wanted a backup
anyway):

```bash
# with the OLD stack still running
docker compose -f deploy/docker/docker-compose.postgres.yml \
  exec -T postgres pg_dump -U alertloop -Fc alertloop > alertloop-pre-0.4.0.dump
docker compose -f deploy/docker/docker-compose.postgres.yml down

# ... upgrade, then with the NEW stack up ...
docker compose exec -T postgres \
  pg_restore -U alertloop -d alertloop --clean --if-exists < alertloop-pre-0.4.0.dump
```

Or **keep the existing volume** by declaring it external, in a
`docker-compose.override.yml` next to the compose file:

```yaml
volumes:
  pgdata:
    external: true
    name: docker_pgdata      # whatever `docker volume ls` actually showed
```

The SQLite demo profile is usually unaffected: its project name was already
derived from a directory called `alertloop`. Check `docker volume ls` anyway.

**2. Select a profile.** Services now carry profiles, so `docker compose up -d`
with nothing selected starts **nothing at all** and says so quietly.

```bash
cp .env.example .env      # then set COMPOSE_PROFILES=demo or =postgres
docker compose up -d
```

Do not set the variable *and* pass `--profile`: Compose combines them, and both
deployments would start and collide on port 8080.

**3. Point at an image.** The compose file pulls
`ghcr.io/golovanov-dev/alertloop:latest` and no longer builds from source. That
image exists once v0.4.0 is tagged and the release workflow has run. To deploy
before that — or to run your own build — build it and name it:

```bash
make docker VERSION=v0.4.0-rc
echo 'ALERTLOOP_IMAGE=alertloop:v0.4.0-rc' >> .env
```

In production, pin `ALERTLOOP_IMAGE` to a version tag. `latest` moves under you
on the next `docker compose pull`, which is the last thing you want from an
alerting service.

### Changed

- **Closing an incident now notifies.** In 0.3.x the manual resolve action was
  silent. It now sends a recovery notice to the channels that received the
  alert, as does an ingested `status: resolved`. If you resolve events in bulk
  from the console, expect one message per incident per channel that was
  alerted. `notify_on_resolve: false` restores the old silence.
- **One Compose file.** `docker-compose.yml` at the repository root now carries
  both deployments as profiles — `demo` (all-in-one, SQLite) and `postgres`
  (separate api and worker containers, PostgreSQL) — with an explicit project
  `name: alertloop`. `deploy/docker/docker-compose.postgres.yml` is gone.
  See "How to upgrade a Compose deployment" above: the profile selection and
  the project name are both breaking for an existing install.
- **The Compose file pulls the published image and builds nothing.** Running
  AlertLoop with Docker no longer begins with cloning the repository. Set
  `ALERTLOOP_IMAGE` to run a build of your own, and pin it to a version tag in
  production.
- **One example config.** `alertloop.example.yaml` serves the binary install and
  both Compose profiles; it reads the database driver and DSN from the
  environment with SQLite as the default. `deploy/docker/alertloop.yaml.example`
  is gone.
- **`alertloop.example.yaml` no longer has a fallback admin token.** It
  references `${ALERTLOOP_ADMIN_TOKEN}` with no default, so an installation that
  does not supply one stops at startup naming the variable, instead of running
  on a placeholder published in this repository. **What to do:** set
  `ALERTLOOP_ADMIN_TOKEN` — in `.env` under Compose, or in the
  `EnvironmentFile` under systemd. `deploy/systemd/install.sh` now generates one
  into `/etc/alertloop/alertloop.env` and the unit reads that file, so a fresh
  systemd install needs nothing extra. A test asserts the example config refuses
  to load without a token.

### Fixed

Everything below except the last two items was found by a pre-release audit of
the whole repository on 2026-08-24, before 0.4.0 was tagged. Three of them were
defects in the incident lifecycle this release introduces — they broke exactly
the scenario the release exists for.

- **Retention deleted incidents that were still firing.** An event's age was its
  creation time, so once a repeated `firing` started moving `last_seen_at`
  without moving `created_at`, the first incident retention deleted was the one
  that had been burning longest — while it was still burning. Everything after
  that went wrong quietly: the incident vanished from the API and the console
  mid-outage, the eventual `status: resolved` found nothing to close and
  notified nobody, and the next report opened a fresh incident and woke everyone
  again. An event now ages from when it stopped mattering: `resolved_at` if it
  is closed, `last_seen_at` if it is open. An incident reported a minute ago is
  kept however old it is; one nobody has reported for the whole retention window
  is swept. Behaviour for events that carry no `status` is unchanged, because
  for them `last_seen_at` IS `created_at`.
- **A delivery error containing non-Latin text could wedge a delivery forever
  on PostgreSQL.** `last_error` was truncated by bytes, so a cut through a
  multi-byte character produced invalid UTF-8. SQLite stored it; PostgreSQL
  rejected the whole statement, which left the attempt in `sending`, and five
  minutes later the reaper requeued it — and the same thing happened again,
  indefinitely, re-sending to the recipient each time and never reaching
  dead-letter. Telegram error descriptions and SMTP replies are routinely
  non-ASCII. Truncation is now by runes everywhere text is stored or sent,
  including email subjects.
- **A late delivery result could overwrite a job already requeued.**
  `MarkResult` now writes only while the attempt is still `sending`. A worker
  whose save failed once would otherwise stamp a stale outcome over a row the
  reaper had already returned to the queue.
- **The `kind` column was missing or misplaced on the built-in pages.** The
  0.4.0 notes said delivery kind was visible there; on `/deliveries` there was
  no such column and no `?kind=` filter, and on the event page the header row
  and the cells were in different orders, so the error text printed under
  "Kind" and "alert"/"recovery" printed under "Last error". Both fixed, both now
  covered by a test that compares header positions to cell positions.
- **A repeated `firing` could be dropped entirely.** If the incident was
  resolved in the narrow window between the deduplication lookup and the
  refresh, the report — which says the problem is still happening — produced
  nothing at all: no update, no new incident, no notification, until the next
  check cycle. It now opens a new incident, which is what the source is
  reporting.
- **Resolving an incident could close the wrong one.** `ResolveByDedupe` closed
  the open incident and then read it back in a second statement — but closing
  frees the key, so a new `firing` arriving in between was returned instead. The
  recovery notice would then have gone to that new incident's channels,
  announcing the end of a problem that had just started, with a duration
  computed from the wrong start time. The write and the read are now one
  statement.
- **Resolving a muted incident sent a recovery notice.** Mute means "stop
  telling me about this"; the end of a story a channel was deliberately not told
  the beginning of is not an exception to that.
- **The startup warning about an open API cried wolf.** It fired whenever no API
  keys were configured, but the API is only open when there is neither a key nor
  an admin token — which is to say it warned about the default image
  configuration and about every systemd install. An operator who learns to
  ignore it will ignore the real one.
- **The per-IP rate limiter did nothing behind a reverse proxy** — the
  documented production setup. Every request arrived from the proxy's address,
  so the whole internet shared one bucket: guessing at the admin token was
  effectively unlimited, and a single noisy client could exhaust the bucket and
  get every legitimate event source a 429. See `rate_limit.trusted_proxies`
  under "Added", and the rate limiting now shipped in `deploy/proxy/nginx.conf`.
- **API keys were compared with a plain map lookup**, while the admin token in
  the same file used a constant-time comparison. Keys are now compared as
  SHA-256 digests in constant time.
- **`foreign_keys` was silently switched off on SQLite** for anyone who put a
  `?` in their DSN — for instance to raise `busy_timeout`. The pragma string was
  appended only when the DSN had no query at all, so tuning one setting dropped
  the other, and with it `ON DELETE CASCADE`: deleting an event left its
  delivery attempts behind to retry five times against an event that no longer
  existed. Pragmas are now merged into whatever the DSN already carries, and
  part of the storage suite runs against a real file rather than `:memory:`,
  where foreign keys are off and this was invisible.
- **The published image could come up with a completely open API.** With no
  `ALERTLOOP_ADMIN_TOKEN` and no API keys, `docker run` gave an unauthenticated
  service that could create, read, and modify events and replay deliveries.
  0.4.0 makes the image the primary Docker path, so it now refuses to start and
  explains what to set.
- **The delivery queue moved two attempts at a time.** A tick claimed exactly
  `Concurrency` rows and waited for all of them, so the semaphore bounded
  nothing and one slow channel stalled everything behind it: with a 30-second
  send timeout, the whole product delivered two notifications per half-minute
  while a healthy Telegram sat idle. A tick now claims ten times the
  concurrency and starts a new attempt as each slot frees, so a dead SMTP server
  no longer delays alerts going somewhere that works.
- **Retention monopolised SQLite's single connection.** The sweep looped without
  pausing, so every API request and every delivery queued behind garbage
  collection. It now yields between batches and stops on shutdown.
- **`RunAll` could close the database during graceful shutdown.** It returned as
  soon as either the server or the worker finished, and `main`'s deferred
  `Close` then pulled the database out from under the HTTP server's ten-second
  drain — failing exactly the requests graceful shutdown exists to protect.
- **Emails had no `Date` or `Message-ID` header.** RFC 5322 requires both, and
  Gmail and Microsoft 365 treat their absence as a spam signal. For a product
  whose entire job is putting a notification in front of a person, the spam
  folder is total failure — and it looks like "email is broken", not like a
  missing header. `Auto-Submitted: auto-generated` was added too, so
  out-of-office responders do not reply to alerts. An alert and its recovery get
  distinct message ids; retries of the same notification reuse theirs.
- **`alpine:3.20` in the runtime image** stopped receiving security updates in
  spring 2026. Now 3.22 — which matters because 0.4.0 is the first release that
  publishes an image rather than expecting a local build.
- **`npm install` in the Dockerfile** was allowed to update the lock file, so
  the published image could carry dependency versions CI never tested. Now
  `npm ci`, as in CI.
- **The release workflow would have published notes headed "unreleased".** The
  guard compared only the version number, so a heading of
  `## 0.4.0 - unreleased` matched. It now requires a dated heading.
- **The `tee` in the cron wrapper could lose a failing job's output.** A process
  substitution is not waited on, so the output could still be unflushed when the
  alert was built - and the output is the most useful thing in that alert.
  Found while writing the integration; never shipped.
- **A resolved incident no longer blocks its own recurrence.** `dedupe_key` was
  unique across every event in every state, so a service that failed, was fixed,
  and failed again produced no second event — the second outage was silently
  swallowed as a duplicate of the closed one. Uniqueness is now scoped to *open*
  incidents. Present since 0.1.0.
- **The migration runner no longer splits a statement on a semicolon inside a
  comment or a string literal.** A `;` in an explanatory comment cut a migration
  in half and handed the database the English prose as SQL, failing the upgrade
  with a syntax error pointing at a comment. Migrations are the one thing that
  runs against a customer's data on upgrade, so this is not a matter of how a
  comment is punctuated. A chunk containing no SQL at all is no longer submitted
  as a statement either.
- **The manual `resolve` action records `resolved_at`**, exactly as an ingested
  recovery does. The two paths would otherwise disagree about when the same
  incident ended.

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
