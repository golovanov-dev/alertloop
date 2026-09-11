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
| `GET /v1/stats` | `read` | Event counts by state and delivery-attempt counts by state. |

`/health` and `/health/live` are the same check under two names, as are `/ready`
and `/health/ready`. Point your monitoring at whichever your tooling prefers.

### Container health checks

Under Compose every AlertLoop container has a Docker health check, run every
10 seconds. The api (and the demo container) probe `/health/ready`. The worker
serves no HTTP, so it runs `alertloop check-db`: load the config file, connect
to the database, exit 0 if it answered. It migrates nothing and changes nothing
in the database. Outside Docker it needs the service's user, environment and
working directory — the README shows the call for a systemd install.

```bash
docker compose ps                                  # (healthy) / (unhealthy) per service
docker compose up -d --wait --wait-timeout 120     # fails if one does not become healthy
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
what `deliveries.pending` below is for.

### What to alert on

```bash
curl -s -H "X-API-Key: $KEY" http://127.0.0.1:8080/v1/stats
```

```json
{
  "events":     {"new": 12, "acknowledged": 3, "resolved": 400, "muted": 0, "escalated": 1},
  "deliveries": {"pending": 2, "sending": 1, "sent": 900, "failed": 4, "dead_letter": 7}
}
```

- **`deliveries.dead_letter` rising** — deliveries have exhausted their retries
  and stopped. Every one of them is a notification that was never seen by a
  human. Alert on any increase; investigate at the first one.
- **`deliveries.pending` growing steadily** — events are arriving faster than
  they are sent, or no worker is running. A queue that never drains is a
  worker that is not there.
- **`deliveries.failed` staying non-zero** — retries are in flight. Occasional
  values are normal; a floor that never returns to zero is a channel that is
  down.
- **`events.new` growing without bound** — nobody is acknowledging or resolving
  anything. That is an operational signal about your team, not about AlertLoop.
  Read it against what you ingest: for business events and audit entries `new`
  is the normal resting state (see "Event state is not delivery state" in the
  README), so a steady climb there is expected and only incidents sitting in
  `new` mean nobody looked.

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
  max_size_mb: 50    # rotate to <file>.1 at this size; 0 = no rotation
  max_files: 5       # rotated files kept besides the active one
```

Rotation is built in because nothing else rotates that file: at `max_size_mb`
the active file becomes `<file>.1`, older generations shift up, and everything
past `max_files` is deleted. Disk use is bounded by
`max_size_mb × (max_files + 1)`.

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
`ProtectSystem=strict`. Nothing else is needed for
`log.file: /var/log/alertloop/alertloop.log`. On a unit of your own, either add
that line or create the directory yourself:

```bash
sudo install -d -o alertloop -g alertloop -m 750 /var/log/alertloop
```

AlertLoop creates a missing log directory when it can, and an unwritable path
stops the process at startup naming the path — deliberately: a service that
silently logs nowhere is worse than one that refuses to start.

A rotation that fails later — a full disk, a directory sitting where
`<file>.1` belongs — does **not** stop logging. AlertLoop prints one line to
stderr (visible in the journal and in `docker compose logs`), keeps writing to
the file it has open, and retries the rotation after the file has grown by
another `max_size_mb`. What is given up is the size limit, not the log; the
stderr line says so, and it is worth alerting on.

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
(`x-logging`), so it cannot fill the disk. Point the driver elsewhere — journald,
or a collector — by editing that anchor; `max-size`/`max-file` apply to the
`json-file` and `local` drivers only.

### Docker: plain log files on the host

The postgres profile mounts `./logs` into both containers and passes a
per-service log path. Three steps:

```bash
mkdir -p logs
sudo chown 10001:10001 logs          # the container runs as uid 10001
```

```dotenv
# .env
ALERTLOOP_LOG_FILE_API=/var/log/alertloop/api.log
ALERTLOOP_LOG_FILE_WORKER=/var/log/alertloop/worker.log
```

```bash
docker compose up -d --wait --wait-timeout 120
sudo tail -f logs/worker.log        # the files belong to uid 10001
```

Then `tail`, `grep` and `less` work on `logs/api.log` and `logs/worker.log` as
on any other file owned by the service — the files belong to uid 10001, so
reading them from the host means `sudo` unless your user happens to be that uid.
`docker compose logs` still shows the same lines.

Things that bite here, all of them real:

- **Ownership.** Compose creates a missing bind-mount source as `root`, and the
  container runs as uid 10001 with no capabilities. Without the `chown` the
  container stops at startup with `permission denied` on the log file. Fix it
  the same way and restart.
- **`read_only: true` stays.** The root filesystem remains read-only; the bind
  mount is the writable exception. Do not point `log.file` anywhere else — a
  path outside `/var/log/alertloop` (or `/data`, or `/tmp`) fails with
  `read-only file system`, which is the container doing its job.
- **One file per service.** `api` and `worker` share one config file on purpose,
  so the path comes from the environment rather than from the file. Do not give
  them the same file: both would append to it and both would rotate it, cutting
  each other's history short. Most of what an incident needs — deliveries,
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

| Looking for | Grep |
|---|---|
| A delivery that never arrived | `grep "delivery " logs/worker.log` |
| Exhausted retries | `grep "dead-lettered" logs/worker.log` |
| Events stored but delivered nowhere | `grep "matched no routing rule"` |
| Misuse of the incident lifecycle | `grep "status=firing on a non-incident"` |
| Startup configuration decisions | first ~20 lines after a restart |

Set `format: json` when a collector reads the file, and keep `level: info`
unless you are chasing something specific — `debug` logs every routed event.

---

## Backup and restore

**What is in the database:** events, delivery attempts, and the migration
ledger. **What is not:** your configuration. `alertloop.yaml` and the secrets it
references are not in the database and must be backed up separately — losing
them costs you your channels, API keys, and admin token.

Restoring a backup does **not** re-send anything. Delivery attempts come back in
the state they were saved in; anything that was `sent` stays sent.

### SQLite

The database runs in WAL mode, so it is **not** one file: copying `alertloop.db`
while the service is running gives you a torn backup that is missing everything
in `alertloop.db-wal`. Use one of these instead.

**Online, service running** (preferred — atomic, no downtime):

```bash
sqlite3 /var/lib/alertloop/alertloop.db \
  ".backup '/var/backups/alertloop-$(date -u +%Y%m%dT%H%M%SZ).db'"
```

`VACUUM INTO '/var/backups/…'` works too and additionally compacts the file.

**Offline:**

```bash
systemctl stop alertloop
cp /var/lib/alertloop/alertloop.db* /var/backups/   # .db, .db-wal, .db-shm
systemctl start alertloop
```

**Restore:**

```bash
systemctl stop alertloop
cp /var/backups/alertloop-20260822T090000Z.db /var/lib/alertloop/alertloop.db
rm -f /var/lib/alertloop/alertloop.db-wal /var/lib/alertloop/alertloop.db-shm
chown alertloop:alertloop /var/lib/alertloop/alertloop.db
systemctl start alertloop
```

Deleting the stale `-wal` and `-shm` files matters: left behind, they belong to
the database you just replaced.

### PostgreSQL

```bash
# Backup (custom format, compressed, restorable selectively)
docker compose --profile postgres exec -T postgres \
  pg_dump -U alertloop -Fc alertloop > alertloop-$(date -u +%Y%m%dT%H%M%SZ).dump

# Restore into an empty database
docker compose --profile postgres exec -T postgres \
  pg_restore -U alertloop -d alertloop --clean --if-exists < alertloop-20260822T090000Z.dump
```

Without Docker, the same commands run against your own PostgreSQL host with
`-h`/`-p`. For a real production estate, prefer your provider's snapshot or
point-in-time recovery over a dump on a timer.

**Restore checklist, either engine:**

1. Stop the AlertLoop processes first. Restoring under a running worker means
   restoring under something that is writing.
2. Restore.
3. Start AlertLoop and check the log for `migrations applied` — a backup from an
   older version is migrated forward on start, which is expected and safe.
4. `curl /health/ready` and then `/v1/stats`, and compare the counts to what you
   expect.

### Verify the backup, not the backup job

A backup nobody has restored is a hypothesis. At least once, restore into a
throwaway location and start AlertLoop against it:

```bash
alertloop --config /tmp/restore-check.yaml server   # database.dsn -> the restored copy
curl -s localhost:8080/health/ready
```

---

## Upgrades and downgrades

**Upgrading** is: stop, replace the binary or pull the new image, start.
Migrations run automatically at startup, in a transaction, and are recorded in
`schema_migrations` so they never run twice. CI runs a real v0.1.0 install
forward to the current build on both SQLite and PostgreSQL before every merge
(`scripts/upgrade-test.sh`).

**Back up first anyway.** Automatic migrations are convenient precisely because
they are irreversible in place.

**Downgrading is not supported.** A newer AlertLoop may have added columns and
indexes that an older one does not know about; the older binary will not remove
them, and an older `SELECT` against a newer schema is not something we test.
The supported way back is: stop the new version, restore the backup taken before
the upgrade, start the old version. This is why the upgrade procedure begins
with a backup.

Split deployments (`server` and `worker` as separate processes) must run the
**same version**. Upgrade them together.

### Upgrading to 0.5.1: the Compose profile passes the database password differently

The postgres profile used to build a URL from `POSTGRES_PASSWORD`, and a
password containing `/`, `?` or `#` broke it: the api and the worker restarted
in a loop with a parse error. It now builds a keyword/value DSN
(`host=postgres ... password=...`) with the password exactly as written in
`.env`. A password that worked before works unchanged, with two exceptions:

- **A percent-encoded password.** If you wrote `%2F` for `/` (or any `%XX`) in
  `POSTGRES_PASSWORD` to make the URL parse, the URL decoded it and the new DSN
  does not: AlertLoop would send `%2F` literally and PostgreSQL would refuse it.
  Put the decoded password — the one PostgreSQL has — into `.env` before you
  upgrade. `grep '^POSTGRES_PASSWORD=.*%' .env` finds the case. If you miss
  it, the refused login in the log carries a note pointing here.
- **A password that begins with a single quote** no longer parses. AlertLoop
  refuses to start and says why; change the password as described in
  "Changing the database password" below.

A DSN you write yourself is unaffected, with one exception: a URL with an
unencoded `@` in the path (the database name), in the fragment, or in the name
of a query parameter is now refused, because that is what a misplaced password
looks like. Write it as `%40`. An `@` in a parameter's value
(`?user=name@domain`) is fine.

Also new, none of it needing action: a health check of its own for the worker
(it used to inherit the image's HTTP check and show as `unhealthy` for good),
checks every 10 seconds instead of 30, `docker compose up -d --wait
--wait-timeout 120` as the way to start (see "Container health checks"), and
`ALERTLOOP_PORT` in `.env` for the host port.

### Upgrading to 0.5.0: `ALERTLOOP_LOG_FILE` needs a line in your config

The Compose file now passes `ALERTLOOP_LOG_FILE` to the api and the worker, and
`alertloop.example.yaml` reads it as `file: ${ALERTLOOP_LOG_FILE:-}`. **Your
existing `alertloop.yaml` does not**, because it was copied from an older
example — so setting `ALERTLOOP_LOG_FILE_API` / `ALERTLOOP_LOG_FILE_WORKER` in
`.env` would do nothing on its own.

The environment is not a second configuration layer (0.3.0), and no exception is
made here: a variable only takes effect where the config file asks for it by
name. Add one line to your `alertloop.yaml`:

```yaml
log:
  file: ${ALERTLOOP_LOG_FILE:-}
  max_size_mb: 50
  max_files: 5
```

Until you do, one of two things happens, and neither is silent:

- your file **sets `log.file` itself** (even to `""`) — startup warns that
  `ALERTLOOP_LOG_FILE` is set but configures nothing, and the value in the file
  is what runs;
- your file **does not mention `log.file`** — startup is refused, naming the
  variable and printing the line to write. That refusal is deliberate (0.3.0): a
  variable the operator believes is in effect must never be quietly ignored.

Nothing else about the upgrade needs attention: `max_size_mb` and `max_files`
default to 50 and 5 for configs that never heard of them, and logging to stdout
is unchanged.

---

## Runbooks

### A channel has been down for a day

**Symptom:** `deliveries.dead_letter` is climbing; the admin console's Deliveries
screen shows failures on one channel.

**What you have lost:** every notification routed only to that channel since it
broke. The *events* are all still there — AlertLoop stores the event and the
delivery separately for exactly this reason.

1. **Find them.**

   ```bash
   curl -s -H "X-API-Key: $KEY" \
     "http://127.0.0.1:8080/v1/delivery-attempts?state=dead_letter&limit=100" | jq .
   ```

   Or open `/deliveries` (built-in page) or the Deliveries screen in `/admin`.

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

   Both web interfaces have a Replay button. Replay one first and confirm it
   arrives before replaying a hundred.

5. **Mind retention.** Events older than `retention_days` (default 30) are
   deleted along with their delivery attempts. A channel that has been broken
   for longer than that has lost the oldest ones permanently.

**Prevent the repeat:** alert on `deliveries.dead_letter` (see above). A day is
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
   - the log file, if `log.file` is set with `max_size_mb: 0` and nothing else
     rotates it (with the default 50 MB × 5 it cannot grow past ~300 MB);
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

4. **Then fix the cause.** Leave `log.max_size_mb` at a non-zero value (or hand
   the file to logrotate), set a retention window that matches the disk, and
   alert on disk usage.

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
   docker compose --profile postgres ps
   docker compose --profile postgres logs --tail=50 postgres
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
   within five minutes by the reaper.

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
3. **Did the alert dead-letter?** A channel whose alert never arrived is
   deliberately skipped: a bare "resolved" would be the only message it ever
   received. Fix the channel and replay the alert.
4. **Look for the row.** Recovery notices are ordinary delivery attempts with
   `kind=recovery`:

   ```bash
   curl -s -H "X-API-Key: $KEY"      "http://127.0.0.1:8080/v1/delivery-attempts?kind=recovery&limit=20" | jq .
   ```

   No rows at all means nothing was queued (steps 1-3). Rows in `failed` or
   `dead_letter` mean the channel is broken, not the lifecycle.
