# Operating AlertLoop

Backup, restore, upgrade, and what to do when something is broken.

AlertLoop is the service that tells you your systems are down. When *it* is the
thing that is down, nothing tells you — so this document leads with how to
monitor AlertLoop itself, and every runbook below says explicitly what you lose
while the failure lasts.

- [Monitoring AlertLoop itself](#monitoring-alertloop-itself)
- [Reading AlertLoop's own logs](#reading-alertloops-own-logs)
- [Backup and restore](#backup-and-restore)
- [Upgrades and downgrades](#upgrades-and-downgrades)
- [Runbooks](#runbooks)
  - [A channel has been down for a day](#a-channel-has-been-down-for-a-day)
  - [The disk filled up](#the-disk-filled-up)
  - [The database is unreachable](#the-database-is-unreachable)
  - [Events arrive but nothing is delivered](#events-arrive-but-nothing-is-delivered)
  - [Alerts arrive but recoveries do not](#alerts-arrive-but-recoveries-do-not)

---

## Monitoring AlertLoop itself

### Endpoints

| Endpoint | Auth | Meaning |
|---|---|---|
| `GET /health`, `GET /health/live` | none | The process is up and serving HTTP. No dependencies are checked. |
| `GET /ready`, `GET /health/ready` | none | The database answered. Returns `503` when it did not. |
| `GET /v1/stats` | `read` | Counts by state, and four absolute signals for alerting (below). |

`/health` and `/health/live` are the same check under two names, as are `/ready`
and `/health/ready`. Point your monitoring at whichever your tooling prefers.

### Container health checks

Under Compose every AlertLoop container has a Docker health check, run every
10 seconds. The api (and the demo container) probe `/health/ready`. The worker
serves no HTTP, so it runs `alertloop check-db`. It exits 0 only if `server`
and `all` would start with this config: the config is valid (`log.level`,
`log.format`, `trusted_proxies` included), `admin_token` or `api_keys` is set,
the database answers and has no migrations newer than the binary, and
`log.file`, if set, can be opened for writing. It migrates nothing, changes
nothing in the database and writes nothing to the log file; when `log.file`
does not exist yet, it creates and deletes a temporary file in that directory.
It prints the version that answered. Because it is the worker's health check, a
config used only by the worker needs a credential too.

```bash
docker compose ps                                  # (healthy) / (unhealthy) per service
docker compose up -d --wait --wait-timeout 120     # fails if one does not become healthy
docker compose run --rm --no-deps api --config /etc/alertloop/alertloop.yaml check-db
```

Under systemd, run it as the service does — its user, environment file, and
working directory:

```bash
sudo -u alertloop sh -c 'cd /var/lib/alertloop && set -a &&
  . /etc/alertloop/alertloop.env &&
  exec /usr/local/bin/alertloop --config /etc/alertloop/alertloop.yaml check-db'
```

Use `--wait` for installs and upgrades. Plain `up -d` returns as soon as the
containers exist, so an AlertLoop that fails at startup and restarts in a loop
looks exactly like one that is running.

The worker does not wait for the api to be healthy, only for its container to
start: a dependency on health would make a plain `up -d` hang on an api that
restarts in a loop. Both processes run the migrations, and that is safe: each
migration is one transaction recorded under a primary key, so whichever
process loses a race rolls back and finds the migration applied on its next
start.

What the worker's check does **not** prove is that deliveries are moving: a
worker whose database answers can still have a queue that never drains. That is
what `worker_last_tick_at` and `oldest_due_delivery_age_seconds` below are for.

### What to alert on

Every signal below is an absolute value: one reading is enough, and the
monitor needs no memory of the previous one.

```bash
curl -s -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/stats
```

```json
{
  "events":     {"new": 12, "acknowledged": 3, "resolved": 400, "escalated": 1},
  "deliveries": {"pending": 2, "sending": 1, "sent": 900, "failed": 4, "dead_letter": 7, "cancelled": 3},
  "open_incidents": 9,
  "oldest_due_delivery_age_seconds": 3,
  "dead_letter_last_24h": 1,
  "worker_last_tick_at": "2026-09-22T12:00:07Z"
}
```

| Alert when | Means |
|---|---|
| `worker_last_tick_at` is `null` or more than 120 s old | No worker is polling the queue. A running worker writes it at most every 15 s, so it is at most 15 s plus `worker.poll_interval` old. `null` until a 0.7.0 worker has run against the database. |
| `oldest_due_delivery_age_seconds` above 300 | Deliveries are due and nobody takes them: no worker, or one channel that hangs keeps all the slots it may use busy. `null` means nothing is due. |
| `dead_letter_last_24h` above 0 | Deliveries exhausted their retries in the last 24 hours. Each one is a notification nobody saw. List them with `/v1/delivery-attempts?state=dead_letter`. |

If you raised `worker.poll_interval` (default 2 s), keep both thresholds well
above it: a healthy tick is up to 15 s plus one `poll_interval` old, and a due
delivery waits up to one `poll_interval` before a worker looks. The
thresholds are the numbers 120 and 300 in the script below; change them there.

#### A check script

The script below turns these three rules into one exit code: **0** when all is
well, **2** when a rule fails *or* `/v1/stats` did not answer (AlertLoop down,
wrong key, a proxy error, a timeout, an answer that is not JSON). There is no
other code, so "not 0" is the whole test. Those are the Nagios plugin codes for
OK and CRITICAL. It prints `OK`, or one `CRITICAL:` line that names every rule
that failed and the values behind it, so the alert says what broke.

**Where to run it.** Either host works; they catch different things.

| Run on | URL argument | Catches | Misses |
|---|---|---|---|
| The AlertLoop host | none (default `http://127.0.0.1:8080`; if you changed the port — `addr`, or `ALERTLOOP_PORT` under Compose — pass `http://127.0.0.1:<port>`) | worker, queue, dead letters | the host itself going down — nothing on it runs then |
| Another host (the monitoring host) | `https://alerts.example.com` — your domain, through the reverse proxy | all of the above, and AlertLoop, its proxy or its host being down | only its own host going down; use HTTPS, the key travels in a header |

Running it on another host is the better choice: it also covers [the failure
that AlertLoop cannot report](#the-failure-that-alertloop-cannot-report). Comparing `worker_last_tick_at` with `now` uses the clock of
the host that runs the script: keep both hosts on NTP.

Whichever host runs it, send its alert through **something other than
AlertLoop** (Monit's own mail, cron mail, your monitoring system). A stopped
worker would hold a notification sent through AlertLoop in the same queue this
check is watching.

On that host, as a user with `sudo`. It needs `curl` (7.55 or newer) and `jq`
(`sudo apt-get install -y curl jq`, or `dnf`). First the key: any credential
with scope `read`. It goes into a root-only file, not into the script, the
Monit config or the command line, so it does not show in `ps`:

```bash
sudo install -D -m 0600 -o root -g root /dev/null /etc/alertloop/stats-check.header
sudoedit /etc/alertloop/stats-check.header
```

A monitor that runs checks as its own user (NRPE and Icinga run them as
`nagios`) needs its group to read the file:

```bash
sudo chgrp nagios /etc/alertloop/stats-check.header && sudo chmod 0640 /etc/alertloop/stats-check.header
```

One line, the key written literally after the colon and a space:

```text
X-API-Key: <your read key>
```

Then the script:

```bash
sudo tee /usr/local/bin/alertloop-stats-check >/dev/null <<'EOF'
#!/bin/sh
# Exit 0 when AlertLoop's delivery queue is healthy, 2 otherwise.
# Usage: alertloop-stats-check [BASE_URL]    (default http://127.0.0.1:8080)
url="${1:-http://127.0.0.1:8080}"
url="${url%/}/v1/stats"
stats=$(curl -fsS --max-time 10 -H @/etc/alertloop/stats-check.header "$url" 2>&1) || {
  echo "CRITICAL: $url did not answer: $stats"; exit 2; }
[ -n "$stats" ] || { echo "CRITICAL: $url answered with an empty body"; exit 2; }
problems=$(printf '%s\n' "$stats" | jq -r '[
  if .worker_last_tick_at == null then "no worker has polled the queue"
  elif now - (.worker_last_tick_at | fromdateiso8601) >= 120
    then "the worker last polled at \(.worker_last_tick_at)" else empty end,
  if (.oldest_due_delivery_age_seconds // 0) >= 300
    then "a delivery has been due for \(.oldest_due_delivery_age_seconds) s" else empty end,
  if .dead_letter_last_24h > 0
    then "\(.dead_letter_last_24h) dead letter(s) in the last 24 h" else empty end
  ] | join("; ")' 2>&1) || { echo "CRITICAL: unexpected answer from $url: $problems"; exit 2; }
if [ -n "$problems" ]; then echo "CRITICAL: $problems"; exit 2; fi
echo "OK"
EOF
sudo chmod 755 /usr/local/bin/alertloop-stats-check
sudo /usr/local/bin/alertloop-stats-check https://alerts.example.com; echo "exit $?"
```

Run it with `sudo`: the key file is readable by root only. A new install with
no worker running yet prints `CRITICAL: no worker has polled the queue`; a wrong
key prints `... did not answer: curl: (22) ... 401`.

**Monit** runs programs as root, without a shell and without your environment;
the script needs neither. Put this into `/etc/monit/conf.d/alertloop-stats`
(`/etc/monit.d/` on the RHEL family), then `sudo monit reload`:

```text
check program alertloop-stats
    with path "/usr/local/bin/alertloop-stats-check https://alerts.example.com"
    with timeout 20 seconds
  if status != 0 for 2 cycles then alert
```

`alert` goes to Monit's own mail (`set mailserver` and `set alert` in
`monitrc`). Do not add `exec alertloop-monit` here, for the reason above. On the
AlertLoop host itself, drop the URL from `path`.

**cron** mails whatever a job prints and ignores its exit code, so keep the
output only on failure. The command writes `/etc/cron.d/alertloop-stats`
owned by root, mode 0644 and ending in a newline, as cron requires; the
sixth field is the user:

```bash
printf '%s\n' 'MAILTO=ops@example.com' \
  '*/5 * * * * root out=$(/usr/local/bin/alertloop-stats-check https://alerts.example.com) || echo "$out"' |
  sudo tee /etc/cron.d/alertloop-stats >/dev/null
```

cron mails nothing while the check passes, and one line every five minutes
while it fails. cron hands the output to the host's mail transfer agent, and
many minimal servers have none: make it fail once on purpose (a URL with a
closed port, such as `http://127.0.0.1:9`) and confirm the mail arrives.

#### When `dead_letter_last_24h` stays above 0

The count covers attempts that dead-lettered in the last 24 hours and are still
`dead_letter`. It drops only when the attempt is replayed and leaves
`dead_letter`, when 24 hours pass, or when retention deletes its event. There is no way to acknowledge a dead letter.
So:

- **You decided not to replay** (the channel is gone, the loss is accepted):
  the check stays red for up to 24 hours after the last one. Silence it in your
  monitor for that time (`sudo monit unmonitor alertloop-stats`, and
  `sudo monit monitor alertloop-stats` the next day; both need `set httpd` in
  `monitrc` — see the Monit README), and write down why.
- **The reverse:** a dead letter nobody replays drops out of the count after 24
  hours and the check turns green, although the notification is still lost.
  `deliveries.dead_letter` and `/v1/delivery-attempts?state=dead_letter` show
  every one of them until retention deletes it.
- **Right after upgrading from 0.6.x** the check can be red at once: attempts
  that the old version dead-lettered in the last 24 hours count too.

Not every `pending` row waits for the worker: a recovery waits for its alert,
and one whose alert dead-lettered (or whose channel was removed from the
config) stays `pending` until that alert is replayed and sent, or until
retention removes the incident. Such a recovery is not due, so it does not
raise `oldest_due_delivery_age_seconds`. List them with
`/v1/delivery-attempts?kind=recovery&state=pending` and check the alert of the
same `event_id` and `channel_name` (`?kind=alert&event_id=<id>`).

`open_incidents` counts incidents that are not resolved. It is a signal about
your team, not about AlertLoop: it grows when nobody resolves anything.

Event sources getting 429: the per-IP limit (`rate_limit.per_ip_*`) counts
IPv6 clients by /64, so sources that share one /64 share one bucket. Behind
the shipped `nginx.conf` the proxy answers 429 itself, with a zone equal to
`rate_limit.per_ip_*`: those requests are in nginx's access log, not in
AlertLoop's.

### The failure that AlertLoop cannot report

If the AlertLoop process, its database, or its host is down, it cannot notify
you about itself. That is not a gap to be fixed with configuration; it is
inherent, and pretending otherwise is how outages get missed.

Cover it from **outside** the AlertLoop host:

- an external uptime check against `/health/ready`, run by something that is not
  AlertLoop;
- a second, independent notification path for that one check — a different
  provider, a phone, a person;
- `Restart=on-failure` in the systemd unit and `restart: unless-stopped` in
  Compose, both already configured, which cover a crash but not a wedged host.

---

## Reading AlertLoop's own logs

When AlertLoop is the thing behaving strangely, its log is the evidence. This is
how to get at it in each deployment, and how to get **ordinary files** under
Docker, where the container log is a JSON file named after a container hash.

AlertLoop always writes to stdout. `log.file` adds a copy on disk; it never
replaces stdout, so `docker compose logs` and any log shipper keep working
alongside it.

```yaml
log:
  level: "info"
  format: "text"     # json if a collector reads it
  file: ""           # empty = stdout only
```

AlertLoop does not rotate that file; logrotate does (below). The file is opened
for appending, so logrotate's `copytruncate` works without restarting the
process.

### Binary and systemd

```bash
journalctl -u alertloop -f                       # stdout goes to the journal
sudo tail -f /var/log/alertloop/alertloop.log    # if log.file is set
```

`sudo` is not decoration. The unit runs with `UMask=0077`, so the log file ends
up owned by the `alertloop` user and readable by nobody else — which is the
point of that hardening, since log lines carry event messages. Read it as root,
or add yourself to a group that can, rather than loosening the mode.

The shipped unit has `LogsDirectory=alertloop`, so systemd creates
`/var/log/alertloop` owned by the service user and makes it writable despite
`ProtectSystem=strict`. Nothing else is needed to log to
`/var/log/alertloop/alertloop.log` (the `file:` line under `log:` in your
config). On a unit of your own, either add `LogsDirectory=alertloop` or create
the directory yourself:

```bash
sudo install -d -o alertloop -g alertloop -m 750 /var/log/alertloop
```

AlertLoop creates a missing log directory when it can; an unwritable path
stops the process at startup, naming the path.

Rotation, on the host, as a user with `sudo`:

```bash
sudo tee /etc/logrotate.d/alertloop >/dev/null <<'EOF'
/var/log/alertloop/*.log {
    daily
    rotate 7
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
}
EOF
sudo logrotate -d /etc/logrotate.d/alertloop    # dry run: prints what it would do
```

`copytruncate` copies the file and empties it in place; a line written in
between those two steps can be lost.

### Docker: what you get by default

```bash
docker compose logs -f worker        # follow one service
docker compose logs --since 1h       # everything, last hour
docker compose logs --no-color > alertloop.log   # to a file for grep/less
```

The container log itself is a JSON-wrapped file whose path contains the
container id:

```bash
docker inspect --format '{{.LogPath}}' alertloop-worker-1
```

It is readable, but it is not what you want to work with: the path changes
whenever the container is recreated, reading it needs root, and every line is
wrapped in JSON. The Compose file caps it at 10 MB × 3 per container
(`x-logging`), so it cannot fill the disk.

How far back that goes depends on traffic. The api container writes one line of
about 220 bytes per API request, `/health/*` excluded; the health checks add
nothing. 20–30 MB is therefore about 100,000 requests: 1,000 events and a
once-a-minute `/v1/stats` check a day keep about five weeks, 10,000 events a day
about a week, 100,000 a day one day. For more, raise `max-size` or `max-file` in
the `x-logging` anchor and recreate the containers
(`docker compose up -d --force-recreate --wait --wait-timeout 120`), or write
plain log files (below). The worker writes one line of about 350 bytes (more
with a long error) per
delivery attempt, sent or failed, so its 20–30 MB hold roughly 70,000
attempts — per event, one per channel it is routed to: after a long
outage with a backlog the oldest worker lines are the first to go. Save them
while you investigate (`docker compose logs --no-color worker > worker.log`).
Point the driver elsewhere — journald, or a
collector — by editing that anchor; `max-size`/`max-file` apply to the
`json-file` and `local` drivers only.

### Docker: plain log files on the host

The postgres profile mounts `./logs` into both containers and passes a
per-service log path. Three steps:

```bash
mkdir -p logs                        # already there, owned by root, if the stack has run
sudo chown 10001:10001 logs          # the container runs as uid 10001
sudo chmod 755 logs                  # logrotate skips a group-writable directory
```

```dotenv
# .env
ALERTLOOP_LOG_FILE_API=/var/log/alertloop/api.log
ALERTLOOP_LOG_FILE_WORKER=/var/log/alertloop/worker.log
```

```bash
docker compose up -d --force-recreate --wait --wait-timeout 120 api worker
sudo tail -f logs/worker.log        # the files belong to uid 10001
```

Then `tail`, `grep` and `less` work on `logs/api.log` and `logs/worker.log` as
on any other file owned by the service — the files belong to uid 10001, so
reading them from the host means `sudo` unless your user happens to be that uid.
`docker compose logs` still shows the same lines.

Rotation of `./logs`, on the host, as a user with `sudo`, from the directory with
`docker-compose.yml` (`$PWD` is written into the file):

```bash
sudo tee /etc/logrotate.d/alertloop-compose >/dev/null <<EOF
$PWD/logs/*.log {
    daily
    rotate 7
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
}
EOF
sudo logrotate -d /etc/logrotate.d/alertloop-compose
```

The files stay owned by uid 10001 and the containers keep writing to them;
nothing needs a restart. If `-d` says `skipping ... insecure permissions`, the
directory is group-writable (umask 002): run `sudo chmod 755 logs`. Only
`copytruncate` works:
AlertLoop never reopens the file, so a rule that renames it leaves the process
writing to the renamed copy. On a fresh install `-d` prints `log does not need
rotating`, which is normal; `sudo logrotate -f /etc/logrotate.d/alertloop-compose`
rotates now, and a new `api.log.1` next to `api.log` shows it worked.

Things that bite here, all of them real:

- **Ownership.** Compose creates a missing bind-mount source as `root`, and the
  container runs as uid 10001 with no capabilities. Without the `chown` the
  container stops at startup with `permission denied` on the log file. Run the
  `chown` above, then the `up -d --force-recreate` command above.
- **`read_only: true` stays.** The root filesystem remains read-only; the bind
  mount is the writable exception. Do not point `log.file` anywhere else — a
  path outside `/var/log/alertloop` (or `/data`, or `/tmp`) fails with
  `read-only file system`, which is the container doing its job.
- **One file per service.** `api` and `worker` share one config file on purpose,
  so the path comes from the environment rather than from the file. Do not give
  them the same file: their lines would interleave with nothing saying which
  process wrote which. Most of what an incident needs — deliveries,
  retries, dead-letter, retention — is written by the **worker**.
- **The demo profile is stdout-only.** It runs the config baked into the image,
  which has no `log:` section and does not read `ALERTLOOP_LOG_FILE`. Use
  `docker compose logs`, or mount your own config file to get a log file — and
  if you do, give it a path on a writable mount: the demo container has
  `read_only: true` as well, so only `/data` (the named volume) and `/tmp`
  accept writes.
- **`./logs` is gitignored**, along with `*.log` and the rotated `*.log.N`.
  Nothing from it is committed.

### What to grep for

The patterns depend on `log.format`: `text` writes `key=value`, `json` writes
`"key":"value"`. A `text` pattern finds nothing in a `json` log, and grep says
nothing about it, so check which format your config has first.

| Looking for | `format: text` | `format: json` |
|---|---|---|
| A delivery that never arrived | `grep "delivery " logs/worker.log` | the same |
| Exhausted retries | `grep "dead-lettered" logs/worker.log` | the same |
| Deliveries to one channel | `grep "channel_name=ops-hook" logs/worker.log` | `grep '"channel_name":"ops-hook"' logs/worker.log` |
| Recovery notices queued at a resolve (`channels=0`: no channel had the alert) | `grep "recovery notices queued" logs/api.log` | the same |
| Events stored but delivered nowhere | `grep "matched no routing rule" logs/api.log` | the same |
| Misuse of the incident lifecycle | `grep "status=firing on a non-incident" logs/api.log` | the same |
| Startup configuration decisions | first ~20 lines after a restart | the same |
| Requests from one client | `grep -w "client_ip=203.0.113.7" logs/api.log` | `grep '"client_ip":"203.0.113.7"' logs/api.log` |
| Requests made with one API key | `grep "credential=<key id>" logs/api.log` | `grep '"credential":"<key id>"' logs/api.log` |
| Requests made with the admin token | `grep "credential=admin" logs/api.log` | `grep '"credential":"admin"' logs/api.log` |

`logs/api.log` and `logs/worker.log` are the Compose files from the section
above. In other layouts, feed the same pattern from where the lines are:

```bash
sudo journalctl -u alertloop --no-pager | grep "credential=admin"   # systemd, stdout
sudo grep "credential=admin" /var/log/alertloop/alertloop.log         # systemd with log.file: one file, api and worker together
docker compose logs --no-color api | grep "credential=admin"        # Compose without log files (demo profile: service alertloop)
```

Every request line (`msg=http`) carries `client_ip`: the address the per-IP
rate limiter counts, which behind a reverse proxy is the real client only
when `rate_limit.trusted_proxies` lists that proxy. An authenticated request
also carries `credential` and `scope`; a request refused with 403 (wrong scope)
carries them too. A request refused with 401 carries only `client_ip`: which
key was tried is not logged. For an API key, `credential` is its key id, the
first 8 hex digits of its SHA-256; the key itself is never logged. The key id
of a key, on Linux:

```bash
printf %s "$KEY" | sha256sum | cut -c1-8          # macOS: shasum -a 256 instead of sha256sum
```

Use `printf %s`, not `echo`: `echo` adds a newline, and the id comes out wrong.

Set `format: json` when a collector reads the file, and keep `level: info`
unless you are chasing something specific — `debug` logs every routed event.

---

## Backup and restore

**What is in the database:** events, delivery attempts, the worker's last
tick, and the migration ledger. **What is not:** your configuration. `alertloop.yaml` and the secrets it
references are not in the database and must be backed up separately — losing
them costs you your channels, API keys, and admin token.

Restoring a backup does **not** re-send anything. Delivery attempts come back in
the state they were saved in; anything that was `sent` stays sent.

`deploy/backup/alertloop-backup` backs up every production install. It writes
the copy readable by its owner only (umask 077), checks it before giving it its
final name (`PRAGMA integrity_check` for SQLite, `pg_restore --list` for a
dump, and for both that the copy holds AlertLoop's first migration `0001_init`
and its `events` and `delivery_attempts` tables: an empty database, or
someone else's, is refused), prints its path and deletes copies older than
`KEEP_DAYS` days (default 14). A second run in the same second fails rather
than replace the first one's copy; the directory must be on a file system
with hard links (not vfat or SMB). A
failed run exits non-zero and leaves no `alertloop-*` file behind. The
copies sit on the same disk as the database: copy the directory off the host
with the tool you use for everything else.

The Compose `demo` profile has no recipe: it is for trying AlertLoop, not for
keeping data.

**Monit self-check installed** (`integrations/monit`, systemd only): it starts a
stopped AlertLoop two cycles later, in the middle of a restore. The systemd
restore and downgrade commands below therefore stop Monit itself first and start
it again at the end; on a host without Monit those two lines do nothing. Monit
is stopped rather than told `monit unmonitor`: that command needs `set httpd` in
`monitrc`, which Debian and Ubuntu ship commented out, and without it the error
scrolls past while the restore goes on. Monit's other checks on the host pause
for the same few minutes.

### systemd, SQLite

On the server, in the clone checked out at the release you run, as a user with
`sudo`:

```bash
sudo apt-get install -y sqlite3
sudo install -m 0755 deploy/backup/alertloop-backup /usr/local/bin/
sudo install -m 0644 deploy/backup/alertloop-backup.service deploy/backup/alertloop-backup.timer /etc/systemd/system/
sudo install -d -m 0700 -o alertloop -g alertloop /var/backups/alertloop
sudo systemctl daemon-reload
sudo systemctl enable --now alertloop-backup.timer
sudo systemctl start alertloop-backup.service && ls -l /var/backups/alertloop
```

The timer runs daily at 03:15 (up to 15 minutes later) and catches up at boot
after a missed run. It is a timer rather than cron because the backup must run
as `alertloop`: `sqlite3` run as root can leave root-owned `-wal`/`-shm` files
that AlertLoop can no longer open. Failures are in
`journalctl -u alertloop-backup`. To keep 30 days instead of 14:
`echo "KEEP_DAYS='30'" | sudo tee -a /etc/alertloop/backup.env`.

`sqlite3 .backup` copies a consistent snapshot while AlertLoop runs. Copying
`alertloop.db` itself does not: the database runs in WAL mode, and recent writes
are still in `alertloop.db-wal`.

Restore:

```bash
command -v monit >/dev/null && sudo systemctl stop monit
sudo systemctl stop alertloop
sudo install -m 0600 -o alertloop -g alertloop /var/backups/alertloop/alertloop-<stamp>.db /var/lib/alertloop/alertloop.db
sudo rm -f /var/lib/alertloop/alertloop.db-wal /var/lib/alertloop/alertloop.db-shm
sudo systemctl start alertloop
command -v monit >/dev/null && sudo systemctl start monit
```

The `-wal` and `-shm` files left behind belong to the database you replaced.

### systemd, PostgreSQL

Same place, same user. `pg_dump` must be the server's major version or newer:
Ubuntu 24.04's `postgresql-client` is 16, so a PostgreSQL 17 server needs
`postgresql-client-17` from the PostgreSQL apt repository
(<https://www.postgresql.org/download/linux/ubuntu/>) — otherwise every run
fails, and only the journal says so. `pg_dump --version` shows what runs.

`backup.env` holds the connection from `database.dsn`, one `NAME='value'` per
line, **every value in single quotes**, the password decoded (`%40` in a URL
DSN is `@` here). systemd reads this file for the backup and `sh` reads it for
the restore; in single quotes both take `$`, `#`, `\` and spaces as written,
without quotes they do not agree. A password that contains `'` cannot be
written this way: change it in PostgreSQL
(`ALTER ROLE <PGUSER> PASSWORD '<output of openssl rand -hex 32>'`), then in
`database.dsn` and in `backup.env`, and restart AlertLoop. Every password
change touches those three places; a stale `backup.env` shows only in
`journalctl -u alertloop-backup`.

```bash
sudo apt-get install -y postgresql-client
sudo install -m 0755 deploy/backup/alertloop-backup /usr/local/bin/
sudo install -m 0644 deploy/backup/alertloop-backup.service deploy/backup/alertloop-backup.timer /etc/systemd/system/
sudo install -d -m 0700 -o alertloop -g alertloop /var/backups/alertloop
sudo install -m 0600 /dev/null /etc/alertloop/backup.env
sudoedit /etc/alertloop/backup.env    # PGHOST='...'  PGPORT='5432'  PGUSER='...'  PGPASSWORD='...'  PGDATABASE='alertloop', one per line
sudo mkdir -p /etc/systemd/system/alertloop-backup.service.d
printf '[Service]\nExecStart=\nExecStart=/usr/local/bin/alertloop-backup postgres /var/backups/alertloop\n' |
  sudo tee /etc/systemd/system/alertloop-backup.service.d/postgres.conf
sudo systemctl daemon-reload
sudo systemctl enable --now alertloop-backup.timer
sudo systemctl start alertloop-backup.service && ls -l /var/backups/alertloop
```

Schedule, failures and `KEEP_DAYS` are as for SQLite. With a managed
PostgreSQL, the provider's snapshots or point-in-time recovery are better than
a dump on a timer.

Restore:

```bash
command -v monit >/dev/null && sudo systemctl stop monit
sudo systemctl stop alertloop
sudo sh -c 'set -a; . /etc/alertloop/backup.env;
  pg_restore --clean --if-exists -d "$PGDATABASE" /var/backups/alertloop/alertloop-<stamp>.dump'
sudo systemctl start alertloop
command -v monit >/dev/null && sudo systemctl start monit
```

### Docker Compose, PostgreSQL

On the server, as a user in the `docker` group; the clone is `~/alertloop`
(substitute yours). The first run checks the recipe, the second line schedules it.
The script is run through `sh`, so it works whatever file mode the clone gave it:

```bash
mkdir -m 700 ~/alertloop-backups
sh ~/alertloop/deploy/backup/alertloop-backup compose ~/alertloop ~/alertloop-backups
(crontab -l 2>/dev/null; echo '15 3 * * * sh $HOME/alertloop/deploy/backup/alertloop-backup compose $HOME/alertloop $HOME/alertloop-backups >/dev/null') | crontab -
```

Cron rather than a timer: the recipe then needs no root and no unit files, only
the `docker` group. Cron mails a failure to the user if the host has mail;
otherwise look at `ls -lt ~/alertloop-backups`. `KEEP_DAYS=30` in front of the
command in the crontab line keeps 30 days.

Restore, in the clone:

```bash
docker compose stop api worker
docker compose exec -T postgres pg_restore -U alertloop -d alertloop --clean --if-exists < ~/alertloop-backups/alertloop-<stamp>.dump
docker compose up -d --wait --wait-timeout 120
```

### Noticing that backups stopped

A timer or a crontab line that stops running says nothing. One command answers
"is there a copy from the last 25 hours", with exit code 1 and a line when
there is not:

```bash
sudo find /var/backups/alertloop -name 'alertloop-*' -mmin -1500 | grep -q . || { echo 'no AlertLoop backup in 25 hours'; false; }   # systemd
find ~/alertloop-backups -name 'alertloop-*' -mmin -1500 | grep -q . || { echo 'no AlertLoop backup in 25 hours'; false; }             # Compose
```

Run it on the AlertLoop host, where the copies are, with the same alerting on
a non-zero exit as the [check script](#a-check-script).

### After a restore

1. Stop AlertLoop before restoring, as the commands above do: restoring under a
   running worker is restoring under something that writes.
2. Migrations run at startup, so a backup from an older version is migrated
   forward. A backup from a newer version is refused
   ([Upgrades and downgrades](#upgrades-and-downgrades)).
3. `curl /health/ready`, then `/v1/stats` — compare the counts to what you
   expect — and send one test event.

### Verify the backup, not the backup job

A backup nobody has restored is a hypothesis. At least once, restore it next to
production, not over it, and start AlertLoop against the copy on a port of its
own, so the production process cannot answer the check in its place. The counts
in `/v1/stats` are the check: `/health/ready` only pings the database, so an
empty one (a DSN typo, a restore that never ran) answers `ready` too.

**systemd, SQLite** — on the server, as a user with `sudo`, from any directory:

```bash
TOKEN="$(sudo grep '^ALERTLOOP_ADMIN_TOKEN=' /etc/alertloop/alertloop.env | cut -d= -f2-)"
D="$(mktemp -d)"
sudo install -m 0600 -o "$USER" /var/backups/alertloop/alertloop-<stamp>.db "$D/alertloop.db"
cat > "$D/check.yaml" <<EOF
addr: "127.0.0.1:18080"
admin_token: \${ALERTLOOP_ADMIN_TOKEN}
database: {driver: sqlite, dsn: "$D/alertloop.db"}
EOF
ALERTLOOP_ADMIN_TOKEN="$TOKEN" /usr/local/bin/alertloop --config "$D/check.yaml" server &
sleep 2
curl -s -H "X-API-Key: $TOKEN" http://127.0.0.1:18080/v1/stats    # the copy
curl -s -H "X-API-Key: $TOKEN" http://127.0.0.1:8080/v1/stats     # production
kill %1; rm -rf "$D"
```

**systemd, PostgreSQL** — the same, with the dump restored into a scratch
database `alertloop_check` next to production. The connection comes from
`backup.env`, read into this shell; the check config names only the database,
and the driver takes host, user and password from those variables. `PGUSER`
must be allowed to create databases; if it is not, on the database server:
`sudo -u postgres psql -c 'ALTER ROLE <PGUSER> CREATEDB'` before the check and
`... NOCREATEDB'` after it. `createdb` and `dropdb` connect to `$PGDATABASE`,
not to the `postgres` database.

```bash
TOKEN="$(sudo grep '^ALERTLOOP_ADMIN_TOKEN=' /etc/alertloop/alertloop.env | cut -d= -f2-)"
D="$(mktemp -d)"
set -a; eval "$(sudo cat /etc/alertloop/backup.env)"; set +a
dropdb --if-exists --maintenance-db="$PGDATABASE" alertloop_check   # left by an interrupted check
createdb --maintenance-db="$PGDATABASE" alertloop_check
sudo cat /var/backups/alertloop/alertloop-<stamp>.dump | pg_restore --no-owner -d alertloop_check
cat > "$D/check.yaml" <<'EOF'
addr: "127.0.0.1:18080"
admin_token: ${ALERTLOOP_ADMIN_TOKEN}
database: {driver: postgres, dsn: "dbname=alertloop_check"}
EOF
ALERTLOOP_ADMIN_TOKEN="$TOKEN" /usr/local/bin/alertloop --config "$D/check.yaml" server &
sleep 2
curl -s -H "X-API-Key: $TOKEN" http://127.0.0.1:18080/v1/stats    # the copy
curl -s -H "X-API-Key: $TOKEN" http://127.0.0.1:8080/v1/stats     # production
kill %1; wait; dropdb --maintenance-db="$PGDATABASE" alertloop_check; rm -rf "$D"
unset PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE
```

**Docker Compose** — a second clone under a project name of its own. A clone
by itself is not separate: `docker-compose.yml` sets the project name
`alertloop`, and an exported `COMPOSE_PROJECT_NAME` overrides both that and
`.env`, so a plain `docker compose up` in any directory can recreate the
production containers. Every command below therefore passes
`-p alertloop-check`, which overrides all of them: nothing in the block can
reach the production containers or their volumes. In your home directory, as
the same user:

```bash
git clone https://github.com/golovanov-dev/alertloop.git ~/alertloop-check && cd ~/alertloop-check
git checkout "$(git -C ~/alertloop describe --tags)"
cp ~/alertloop/.env ~/alertloop/alertloop.yaml .
sed -i '/^ALERTLOOP_PORT=/d; /^ALERTLOOP_LOG_FILE_/d' .env
echo 'ALERTLOOP_PORT=18080' >> .env
docker compose -p alertloop-check up -d --wait postgres
docker compose -p alertloop-check exec -T postgres pg_restore -U alertloop -d alertloop --clean --if-exists < ~/alertloop-backups/alertloop-<stamp>.dump
docker compose -p alertloop-check up -d --wait --no-deps api    # not the worker: it would deliver the restored queue to your channels
TOKEN="$(grep '^ALERTLOOP_ADMIN_TOKEN=' .env | cut -d= -f2-)"
curl -s -H "X-API-Key: $TOKEN" http://127.0.0.1:18080/v1/stats    # the copy
curl -s -H "X-API-Key: $TOKEN" http://127.0.0.1:8080/v1/stats     # production (its ALERTLOOP_PORT)
docker compose -p alertloop-check down -v && cd && rm -rf ~/alertloop-check
```

---

## Upgrades and downgrades

Read the [CHANGELOG.md](CHANGELOG.md) entries of every version after yours
first: they name each change that needs an action. Skipping versions is fine.

1. Back up ([Backup and restore](#backup-and-restore)). Migrations run at
   startup and are irreversible in place.
2. Put the new version in place without restarting.
   **Compose:** check out the release tag, set `ALERTLOOP_IMAGE` in `.env` to
   it, run `docker compose pull`.
   **systemd:** `sudo bash deploy/systemd/install.sh <new binary>`. It keeps the
   binary and the unit it replaces as `/usr/local/bin/alertloop.prev` and
   `/etc/systemd/system/alertloop.service.prev` (one copy, the one before).
3. Run `check-db` ([Container health checks](#container-health-checks)): it
   loads your config with the new version and names that version. Fix what it
   reports.
4. Restart. **Compose:** `docker compose up -d --wait --wait-timeout 120`.
   **systemd:** `sudo systemctl restart alertloop`.
5. Confirm the version with `GET /v1/info` and the counts with `GET /v1/stats`.

`server` and `worker` run as separate processes must run the same version.

**Downgrading is not supported.** Every role and `check-db` refuse to start on
a database that has migrations the binary does not know, and name them. The way
back is to stop, restore the backup taken before the upgrade, and start the old
version. A version that added no migration (its CHANGELOG entry says when it
does) goes back without the restore. The old version under systemd:

```bash
command -v monit >/dev/null && sudo systemctl stop monit
sudo systemctl stop alertloop
sudo mv /usr/local/bin/alertloop.prev /usr/local/bin/alertloop
sudo mv /etc/systemd/system/alertloop.service.prev /etc/systemd/system/alertloop.service   # if it exists
sudo systemctl daemon-reload && sudo systemctl start alertloop
command -v monit >/dev/null && sudo systemctl start monit
```

Under Compose: check out the old tag, set `ALERTLOOP_IMAGE` back, and
`docker compose up -d --wait --wait-timeout 120`. Going back below 0.7.0 with
an `alertloop.yaml` taken from the 0.7.0 example: images before 0.7.0 do not
set `ALERTLOOP_ADDR`, so `addr: ${ALERTLOOP_ADDR:-127.0.0.1:8080}` makes the api
listen on the container's own loopback. Every container reports healthy, and
the published port does not answer. Put `addr: ":8080"` in `alertloop.yaml`
before the `up`, and check with
`curl http://127.0.0.1:<ALERTLOOP_PORT>/health/ready` from the host, not with
`docker compose ps`.

---

## The delivery worker

- **`worker.concurrency`** (default 2) is how many sends run at once. With
  two or more channels configured, one channel takes at most
  `concurrency - 1` of them (at least 1), so one hung channel still leaves a
  slot for the others. A hung channel does hold its whole share: every slot it
  may take waits out its `timeout`. Two or more channels hanging at the same
  time, each with a backlog, can take every slot at any `concurrency`; the
  channels that work then wait up to a hung channel's `timeout` per attempt
  for as long as the hung channels have attempts due. With a single channel
  configured, it may use every slot. With `concurrency: 1` there is no spare
  slot: each attempt to a hung channel delays every other channel by up to
  that channel's `timeout`. With more than one channel, keep it at 2 or more;
  the worker logs a warning at startup otherwise.
- **Each attempt gets the channel's `timeout`** (default 10s), and nothing
  shorter.
- **An attempt stuck in `sending`** because its worker was killed goes back to
  the queue once it has been there for twice the longest channel `timeout`
  (at least 5 minutes); the reaper looks every half of that time, so it can
  take up to half as long again.
- **An alert of a muted incident** that does not go out becomes `cancelled`
  instead of `failed`, `dead_letter` or `pending`: one that fails, one cut
  short at stop, one taken back from a dead worker. It is not retried. The
  check is the incident's state when that outcome is written: an incident
  acknowledged or resolved while the send was in flight no longer counts as
  muted, the alert is retried, and after a resolve no recovery follows it. An alert
  that went out stays `sent`, also one whose send finished after the mute. A `dead_letter` alert
  is not cancelled by mute; replaying it sends it. Recovery notices are never
  cancelled.
- **On stop** (SIGTERM, `docker stop`) the worker claims nothing new and gives
  sends in flight 5 seconds to finish. Sends still running then, on any
  channel type, are cancelled and return to `pending` with their attempt count
  unchanged. Delivery is at-least-once: a receiver that had already got a
  cancelled send gets it again.
- **The time allowed to stop** is set outside AlertLoop: `stop_grace_period`
  in Compose, `docker stop -t`, `TimeoutStopSec` in systemd. The worker needs
  about 9 seconds at worst: 5 for sends, up to 3 to save their results and up
  to 1 to finish taking attempts. The defaults (10 s for Docker, 90 s for
  systemd) fit it.

---

## Runbooks

### A channel has been down for a day

**Symptom:** `dead_letter_last_24h` in `/v1/stats` is above 0; the admin console's Deliveries
screen shows failures on one channel.

**What you have lost:** every notification routed only to that channel since it
broke. The *events* are all still there — AlertLoop stores the event and the
delivery separately for exactly this reason.

1. **Find them.**

   ```bash
   curl -s -H "X-API-Key: $KEY" \
     "http://127.0.0.1:8080/v1/delivery-attempts?state=dead_letter&limit=100" | jq .
   ```

   Or open the Deliveries screen in `/admin`.

2. **Read `last_error` on one of them.** It is stored verbatim, with secrets
   redacted. It usually names the cause outright: an expired SMTP credential, a
   Telegram bot removed from the chat, a webhook host that no longer resolves.

3. **Fix the channel and restart AlertLoop** so the new configuration is loaded.

4. **Replay.** Dead-lettered attempts do not retry on their own — that is the
   point of the state.

   ```bash
   curl -s -X POST -H "X-API-Key: $KEY" \
     "http://127.0.0.1:8080/v1/delivery-attempts/$ID/replay"
   ```

   A replayed attempt starts over with `attempts` at 0 and gets the full
   `max_attempts` retries again.

   The Deliveries screen in `/admin` has a Replay button. Replay one first and confirm it
   arrives before replaying a hundred.

5. **Mind retention.** Events older than `retention_days` (default 30) are
   deleted along with their delivery attempts. A channel that has been broken
   for longer than that has lost the oldest ones permanently.

**Prevent the repeat:** alert on `dead_letter_last_24h` (see above). A day is
far too long to find out by looking.

### The disk filled up

**Symptom:** SQLite writes fail with `database or disk is full`; ingestion
returns `500`; on PostgreSQL the database may have stopped accepting writes
entirely.

**What you have lost:** events that arrived while writes were failing. Ingestion
is atomic — an event is stored with its delivery jobs or not at all — so there
are no half-written events, but a rejected one is simply gone. Check the source
side for what it tried to send.

1. **Get room back first**, before diagnosing.

   ```bash
   du -sh /var/lib/alertloop/*
   journalctl --vacuum-size=200M        # journald is a frequent culprit
   docker system prune -f               # on a Docker host
   ```

2. **Check what actually grew.** In AlertLoop it is almost always one of:
   - the `-wal` file, if a long-running reader kept it from checkpointing;
   - the events table, if `retention_days` is very high or ingestion is far
     heavier than expected;
   - the log file, if `log.file` is set and no logrotate rule covers it;
   - the container log, on a Docker host older than this Compose file, where
     the `json-file` driver had no `max-size`. `docker system prune -f` and the
     `x-logging` anchor in `docker-compose.yml` deal with it.

3. **Shrink the data if you must.** Lower `retention_days` and restart; the
   cleanup runs on start and then every 6 hours. On SQLite, reclaim the freed
   space afterwards:

   ```bash
   systemctl stop alertloop
   sqlite3 /var/lib/alertloop/alertloop.db "VACUUM;"
   systemctl start alertloop
   ```

4. **Then fix the cause.** Add the logrotate rule from "Reading AlertLoop's own
   logs", set a retention window that matches the disk, and alert on disk usage.

### The database is unreachable

**Symptom:** `/ready` returns `503`; the log repeats connection errors; `/health`
still returns `200` (the process is alive — that is the distinction between the
two endpoints).

**What you have lost:** everything sent during the outage. Ingestion needs the
database, so events are rejected rather than buffered. **AlertLoop has no local
queue in front of its database**, and event sources should treat a `5xx` from
ingestion as something to retry.

1. `curl -s -o /dev/null -w '%{http_code}' localhost:8080/ready` — confirm it is
   readiness and not the whole process.
2. Check the database itself:
   ```bash
   docker compose ps
   docker compose logs --tail=50 postgres
   pg_isready -h <host> -U alertloop            # without Docker
   ```
3. Common causes, in the order they actually occur: the database container was
   not restarted with the rest of the stack; the disk under the database filled
   up; the password in `.env` changed but the volume still holds the old one
   (see "Changing the database password" below); connection limits exhausted by
   another application sharing the server.
4. **Do not delete the volume to "reset" it.** That is your event history.
5. Once the database is back, AlertLoop reconnects on its own — no restart is
   needed, though a restart is harmless. Pending deliveries resume; anything
   left stuck in `sending` by a worker that died is requeued automatically
   (see [The delivery worker](#the-delivery-worker)).

### Changing the database password

The postgres image applies `POSTGRES_PASSWORD` only when it initialises an
empty volume. On an existing database, editing `.env` changes what AlertLoop
sends and not what PostgreSQL expects, and the api and the worker then fail on
authentication. Change it in the database first:

```bash
NEW="$(openssl rand -hex 32)"
docker compose exec postgres \
  psql -U alertloop -c "ALTER USER alertloop WITH PASSWORD '$NEW';"
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$NEW/" .env
docker compose up -d --wait --wait-timeout 120
```

`openssl rand -hex 32` is used on purpose: hex has no character that means
anything to a shell, to `sed`, to SQL, to `.env`, or to a DSN.

Changing `.env` makes the last command recreate the containers that read the
variable — the database container included — so expect a short outage of a few
seconds while PostgreSQL restarts. The data is on the volume and is untouched.

### Events arrive but nothing is delivered

**Symptom:** `/v1/events` shows new events; `deliveries` counts stay at zero.

This is a configuration problem, not a failure, and the log said so at startup.

1. **Are there any channels?** With no `channels:` section AlertLoop stores
   events and delivers nothing. That is a valid way to run, and it is what a
   fresh install does.
2. **Is a worker running?** In `server` mode nothing sends. You need `worker` or
   `all`. In a split deployment, `docker compose ps` must show the worker as
   `(healthy)`; `Restarting` or `(unhealthy)` means it cannot start or cannot
   reach the database, and `docker compose logs worker` says which.
3. **Does the server have the same channel configuration as the worker?** The
   server decides which delivery jobs to create; if *it* sees no channels, no
   jobs exist for the worker to send, however healthy the worker is. Both
   processes must load the same file — the Compose postgres profile mounts one
   file into both for this reason.
4. **Is routing swallowing them?** A rule with `channels: []` suppresses
   deliberately. An event matching no rule with no `default:` is delivered
   nowhere — and is logged at warn level, once per event:

   ```bash
   journalctl -u alertloop | grep "matched no routing rule"
   ```

   Preview what a rule set does to an event without creating anything:

   ```bash
   curl -s -X POST -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
     -d '{"type":"incident","severity":"critical","source":"monit"}' \
     http://127.0.0.1:8080/v1/routing/preview | jq .
   ```
5. **Is the event a repeat?** A repeated `dedupe_key` on an open incident
   updates it and deliberately creates no new deliveries. That is the intended
   behaviour for a check that fails every minute — see the incident lifecycle in
   the README.

### Alerts arrive but recoveries do not

**Symptom:** you are told a service is down, never that it came back.

1. **Is `notify_on_resolve` off?** It is on by default; check the config file.
2. **Is anything closing the incident?** A recovery notice is queued when an
   incident closes and at no other time. A monitoring source that only ever
   sends `status: firing` leaves it open forever. In Monit that is a rule
   missing its `else if succeeded` line.
3. **Did the alert dead-letter?** A channel never receives a recovery before
   its alert: the recovery is queued when the incident closes, whatever state
   the alert is in, and stays `pending` until the alert of the same incident to
   that channel is `sent`. Fix the channel and replay the alert; the recovery
   follows it.
4. **Look for the row.** Recovery notices are ordinary delivery attempts with
   `kind=recovery`:

   ```bash
   curl -s -H "X-API-Key: $KEY"      "http://127.0.0.1:8080/v1/delivery-attempts?kind=recovery&limit=20" | jq .
   ```

   No rows at all means nothing was queued (steps 1-3). Rows in `failed` or
   `dead_letter` mean the channel is broken, not the lifecycle.
