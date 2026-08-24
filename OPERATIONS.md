# Operating AlertLoop

Backup, restore, upgrade, and what to do when something is broken.

AlertLoop is the service that tells you your systems are down. When *it* is the
thing that is down, nothing tells you — so this document leads with how to
monitor AlertLoop itself, and every runbook below says explicitly what you lose
while the failure lasts.

- [Monitoring AlertLoop itself](#monitoring-alertloop-itself)
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
   - the log file, if `log.file` is set and nothing rotates it.

3. **Shrink the data if you must.** Lower `retention_days` and restart; the
   cleanup runs on start and then every 6 hours. On SQLite, reclaim the freed
   space afterwards:

   ```bash
   systemctl stop alertloop
   sqlite3 /var/lib/alertloop/alertloop.db "VACUUM;"
   systemctl start alertloop
   ```

4. **Then fix the cause.** Rotate the log file, set a retention window that
   matches the disk, and alert on disk usage.

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
   up; the password in `.env` changed but the volume still holds the old one;
   connection limits exhausted by another application sharing the server.
4. **Do not delete the volume to "reset" it.** That is your event history.
5. Once the database is back, AlertLoop reconnects on its own — no restart is
   needed, though a restart is harmless. Pending deliveries resume; anything
   left stuck in `sending` by a worker that died is requeued automatically
   within five minutes by the reaper.

### Events arrive but nothing is delivered

**Symptom:** `/v1/events` shows new events; `deliveries` counts stay at zero.

This is a configuration problem, not a failure, and the log said so at startup.

1. **Are there any channels?** With no `channels:` section AlertLoop stores
   events and delivers nothing. That is a valid way to run, and it is what a
   fresh install does.
2. **Is a worker running?** In `server` mode nothing sends. You need `worker` or
   `all`. In a split deployment, check that the worker container is up.
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
