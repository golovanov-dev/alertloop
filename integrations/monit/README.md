# AlertLoop + Monit

Monit watches the server. AlertLoop turns what it finds into incidents that
open, update, and close on their own, and delivers them to Telegram, email, or
a webhook.

This integration is the seam between the two. It does not monitor anything
itself, and AlertLoop does not either — that division is deliberate. Monit is
mature, tiny, and already knows how to watch processes, ports, filesystems, and
load. AlertLoop's job starts once something is wrong.

It is part of AlertLoop **Community**, and it runs on the host, never inside
AlertLoop's container.

## Contents

- [What you get](#what-you-get)
- [How it fits together](#how-it-fits-together)
- [Requirements](#requirements)
- [Install](#install)
- [Create the API key](#create-the-api-key)
- [Check it works](#check-it-works)
- [Enable the checks you want](#enable-the-checks-you-want)
- [What each example covers](#what-each-example-covers)
- [Testing a check](#testing-a-check)
- [Cron jobs](#cron-jobs)
- [Workers](#workers)
- [Writing your own check](#writing-your-own-check)
- [dedupe_key: the one thing to get right](#dedupe_key-the-one-thing-to-get-right)
- [Diagnosing problems](#diagnosing-problems)
- [Exit codes](#exit-codes)
- [Limitations](#limitations)
- [Watching AlertLoop itself](#watching-alertloop-itself)
- [Rotating the API key](#rotating-the-api-key)
- [Uninstall](#uninstall)
- [Supported versions](#supported-versions)

## What you get

When PostgreSQL stops answering, you get one message. Not one every 30 seconds
while Monit keeps noticing — one, when it breaks, and one more when it comes
back, with how long it was down.

```
[CRITICAL/incident] postgresql: Connection failed
  Key:      server-01:postgresql:availability
  Started:  2026-08-22 03:14:07 UTC

[RESOLVED] postgresql: Connection failed
  Started:  2026-08-22 03:14:07 UTC
  Resolved: 2026-08-22 03:26:37 UTC
  Duration: 12m30s
```

Between those two, Monit keeps reporting and AlertLoop keeps updating the same
incident — newest severity, newest message, `last_seen_at` moving forward, and
nobody notified again.

## How it fits together

```text
Linux host
|
+-- Monit                        watches processes, ports, filesystems, load
|     |
|     +-- exec on state change
|           |
|           v
|     alertloop-monit            reads Monit's MONIT_* variables
|           |
|           v
|     alertloop-send             builds one JSON document, POSTs it
|           |
|           v
+-- AlertLoop (host or Docker)   lifecycle, dedupe, routing, retries, delivery
                                  |
                                  v
                            Telegram / email / webhook
```

Two scripts, not one, for a practical reason. Monit's `exec` takes a single
string and splits it on whitespace, so passing a human-readable title through it
means escaping quotes inside quotes inside a config file. Monit already exports
what happened as environment variables, so `alertloop-monit` reads those and a
rule stays readable:

```
if failed port 5432 protocol pgsql for 3 cycles then
  exec "/usr/local/bin/alertloop-monit firing critical protocol"
```

`alertloop-send` is the general adapter underneath. Use it directly from your
own scripts, cron jobs, or anything that is not Monit.

### Why Monit runs on the host

Not in a container, and not as part of AlertLoop:

- it needs the host's processes and systemd units;
- it needs real filesystems, real memory, real load — a container sees its own;
- it has to be able to notice that **Docker itself** died, which it cannot do
  from inside Docker;
- doing it any other way means a privileged container with the host's PID
  namespace mounted in, which is a larger hole than the problem it solves.

## Requirements

- Linux with `bash`, `curl`, and `jq`
- Monit 5.x
- an AlertLoop 0.4.0 or later instance this host can reach

```bash
# Debian / Ubuntu
sudo apt-get install -y monit curl jq

# AlmaLinux / Rocky / RHEL  (monit is in EPEL)
sudo dnf install -y epel-release
sudo dnf install -y monit curl jq
```

AlertLoop **0.4.0 is the minimum**. The incident lifecycle this integration
depends on — `status: firing` / `status: resolved`, and recovery notifications —
does not exist in 0.3.x, which would store every Monit report as a separate
event and never close any of them.

## Install

```bash
git clone https://github.com/golovanov-dev/alertloop.git
cd alertloop/integrations/monit
sudo ./install.sh --with-examples
```

That installs the scripts into `/usr/local/bin`, creates
`/etc/alertloop/monit.env` (mode 0600, owned by root), and copies the example
rules into Monit's `conf.d` **disabled**, as `alertloop-*.conf.disabled`.

Nothing starts monitoring anything yet. That is on purpose: every example names
a pidfile, a port, or a threshold that belongs to your machine. An installer
that switched them all on would page you about a PostgreSQL you do not run.

## Create the API key

Give this server its own key, scoped to ingestion only, in **AlertLoop's** config
file:

```yaml
api_keys:
  - key: "paste-the-output-of-openssl-rand-hex-32"
    scope: ingest       # may only create events; cannot read them or resolve them
```

Restart AlertLoop, then put the key on this host:

```bash
sudo nano /etc/alertloop/monit.env
```

```bash
ALERTLOOP_URL=http://127.0.0.1:8080     # or https://alerts.example.com
ALERTLOOP_API_KEY=the-key-you-just-made
ALERTLOOP_HOST=server-01                # optional; defaults to `hostname -s`
```

One key per server, so a compromised host can be cut off on its own. `ingest` is
deliberately the whole scope: Monit needs to report events and nothing else.

Over the network, **HTTPS is required** — the adapter refuses plain HTTP to
anything that is not localhost, and there is no flag to skip certificate
verification. If AlertLoop runs on this same host, use `127.0.0.1` and do not
publish its port at all.

## Check it works

Build the payload without sending it:

```bash
sudo alertloop-send --status firing --severity info --event-type test \
  --dedupe-key "$(hostname -s):install:test" \
  --title "Install check" --message "from the README" --dry-run
```

Then send one for real and look for it in AlertLoop's console:

```bash
sudo alertloop-send --status firing --severity info --event-type test \
  --dedupe-key "$(hostname -s):install:test" \
  --title "Install check" --message "from the README"

sudo alertloop-send --status resolved --dedupe-key "$(hostname -s):install:test"
```

The first should notify your channels; the second should close the incident and
send the recovery. If you get a notification for one and not the other, check
`notify_on_resolve` in AlertLoop's config — it is on by default.

## Enable the checks you want

Rename the ones that apply to this machine, edit them, validate, reload:

```bash
cd /etc/monit/conf.d          # or /etc/monit.d on the RHEL family
sudo mv alertloop-postgresql.conf.disabled alertloop-postgresql.conf
sudo nano alertloop-postgresql.conf        # fix the pidfile path and the port

sudo monit -t                              # validate BEFORE reloading
sudo monit reload
```

Always run `monit -t` first. A failed reload can leave monitoring switched off,
and monitoring that is off is worse than monitoring that is noisy, because
nothing tells you.

Watch the first one land:

```bash
sudo systemctl stop postgresql
sudo tail -f /var/log/monit.log            # Monit noticing
# ... your Telegram or inbox ...
sudo systemctl start postgresql            # then the recovery
```

## What each example covers

| File | What it watches | The trap it avoids |
|---|---|---|
| `system.conf` | CPU, memory, swap, load | The load threshold is per machine: load 4 is idle on 16 cores and a crisis on one. |
| `filesystem.conf` | Space, inodes, mount flags | A filesystem with free space and no inodes fails identically to a full one — and reads as a mystery if you only watch space. The mount-flags check catches a read-only remount, and is the one enabled rule whose incident does not close by itself. |
| `postgresql.conf` | Process, port, protocol, memory | A wedged PostgreSQL keeps its PID. Only the protocol check proves it is usable. |
| `mysql.conf` | Process, port, protocol, memory | Monit authenticates with `mysql_native_password`, which MySQL 8.4 does not load by default — so the credentialed form of this test fails against a healthy server. |
| `php-fpm.conf` | Master process, memory, CPU | PHP-FPM hangs with the master perfectly alive. Pair it with the HTTP check. |
| `pm2.conf` | PM2 status and restart counter | A crash loop faster than an HTTP check's window is invisible to it, and `max_memory_restart` produces no error at all. Both show up only in PM2's restart counter. |
| `nginx.conf` | Master process, port 80, certificate expiry per domain | A renewal hook that stopped working reports nothing anywhere — you find out from a visitor. |
| `nodejs.conf` | systemd state, HTTP health, memory | A Node app usually has no pidfile, and a blocked event loop keeps the process, the port, and the unit state all green. Only a request finds it. |
| `http-health.conf` | The app's own endpoint | Catches "running, listening, returning 500", which no process check can see. |
| `worker-process.conf` | The worker exists | Necessary, not sufficient — see the next row. |
| `worker-heartbeat.conf` | The worker is doing work | A deadlocked worker keeps its PID forever. |
| `cron-exit-code.conf` | A cron job failed | Documentation only: the wrapper reports this, Monit cannot. |
| `cron-freshness.conf` | A cron job never ran | The one people leave out, and the one that matters. |
| `docker.conf` | The Docker daemon | `container is running` is not a health check. |
| `alertloop.conf` | AlertLoop itself | Read its header before using it — see [Limitations](#limitations). |

## Testing a check

A check you have never seen fire is a check you do not have. Every rule here has
four places to fail — the Monit test, the `exec` line, the adapter, and
AlertLoop's routing — and only one of them is visible in `monit status`. Fire
each rule once, deliberately, and watch the message arrive.

### Three tools, first

```bash
sudo monit -t                    # validate the configuration. ALWAYS before reload.
sudo monit reload                # apply it
sudo monit status                # what Monit currently thinks of every check
sudo monit validate              # run every check NOW instead of waiting for the cycle
sudo tail -f /var/log/monit.log  # Monit noticing, and every exec it runs
```

`monit status` needs Monit's HTTP interface enabled in `monitrc` — uncomment the
`set httpd port 2812` block, keep `use address localhost`, and set a real
password. Do not expose it.

While testing, a shorter cycle saves a lot of waiting. In `monitrc`:

```
set daemon 10        # instead of 30. PUT IT BACK when you are done.
```

At 30 seconds a rule with `for 5 cycles` takes two and a half minutes to fire.
That delay is a feature in production and an obstacle now.

### The method that works for every rule: move the threshold

Do not break a production service to see whether an alert works. Invert it —
make the rule true instead:

```bash
sudo nano /etc/monit/conf.d/alertloop-system.conf
#   if cpu usage (user) > 90% for 5 cycles   ->   > 1% for 1 cycle
sudo monit -t && sudo monit reload
# ... the alert arrives ...
# put the threshold back
sudo monit -t && sudo monit reload
# ... the recovery arrives ...
```

This exercises the whole chain — Monit's test, the `exec`, `alertloop-monit`,
`alertloop-send`, AlertLoop's dedupe, your routing rules, your channel — and
touches nothing that serves traffic. Restoring the threshold fires the
`else if succeeded` branch, so you get to see the incident close too.

Use the same trick on paths: point a `check file` or a pidfile at
`/nonexistent`, reload, watch it fire, point it back.

**Do this on one rule at a time.** Two rules firing at once and one message
arriving is a result you cannot interpret.

### Testing the delivery leg on its own

If nothing arrives, find out which half is broken before touching Monit again.
Send the event the rule would have sent, by hand, with the same `dedupe_key`
(`host:service:check`):

```bash
sudo alertloop-send --status firing --severity critical \
  --event-type service_unavailable \
  --dedupe-key "$(hostname -s):mysql:availability" \
  --title "mysql: test" --message "manual check"

sudo alertloop-send --status resolved --dedupe-key "$(hostname -s):mysql:availability"
```

Arrives → Monit is not running your `exec`; look in `/var/log/monit.log`.
Does not arrive → it is the key, the URL, the scope, or the routing. See
[Diagnosing problems](#diagnosing-problems).

### Per file

| File | How to fire it | Notes |
|---|---|---|
| `system.conf` | Lower a threshold, or `stress-ng --cpu $(nproc) --timeout 200s` | Load average is a 5-minute average — generating load tests it slowly and badly. Lower the threshold. Never test the memory or swap rules with real pressure on a machine that is serving traffic. |
| `filesystem.conf` (space, inode) | Lower the threshold below current usage | `fallocate -l 5G /var/tmp/fill` works and is a genuinely bad idea on a filesystem you need. If you do it, `rm` it the moment the alert lands. |
| `filesystem.conf` (fsflags) | `mount -o remount,ro /somewhere-expendable` | Never on `/`. **This incident does not close by itself** — after the test, close it by hand: `alertloop-send --status resolved --dedupe-key "$(hostname -s):filesystem-root:fsflags"` |
| `postgresql.conf`, `mysql.conf` (availability) | `systemctl stop mysql` … `systemctl start mysql` | The examples alert, they do not restart — a service you stop stays stopped until you start it. Stopping also fires the protocol check, as two separate incidents. |
| `postgresql.conf`, `mysql.conf` (protocol) | Block the port while the process lives: `iptables -I INPUT -i lo -p tcp --dport 3306 -j REJECT`, then `iptables -D INPUT -i lo -p tcp --dport 3306 -j REJECT` | This is the real failure the check exists for: process up, database unreachable. Worth seeing once. |
| `php-fpm.conf` | `systemctl stop php8.3-fpm` … `start` | Check first that the pidfile in the rule is the one your version actually writes: `ls /run/php/`. A wrong path alerts immediately and permanently. |
| `nodejs.conf` (service) | `systemctl stop my-node-app` … `start` | Under PM2 or a Docker restart policy the supervisor restarts it faster than Monit looks — that is the point of the HTTP block. |
| `nodejs.conf`, `http-health.conf` | Change `request "/health"` to `request "/nope"`, reload, change it back | The best test in this set: it breaks nothing, exercises the full path, and both edges fire. |
| `nodejs.conf`, `http-health.conf` (slow) | Lower `with timeout 2 seconds` to `1 second` and point it at a known-slow endpoint | Or move the threshold. There is no clean way to make a fast endpoint slow on demand. |
| `pm2.conf` | `pm2 restart <app>` as the app's user, twice | The second restart is what the check sees — the first only seeds the counter if this is the first run. Run the script by hand first: `check-pm2.sh root my-api; echo $?` |
| `nginx.conf` (process, port) | `systemctl stop nginx` … `start` | |
| `nginx.conf` (certificate) | Raise the threshold above the certificate's actual remaining life — `valid > 21 days` → `valid > 3650 days` | Never test this by installing a short-lived certificate. Check what you have now: `echo \| openssl s_client -connect example.com:443 -servername example.com 2>/dev/null \| openssl x509 -noout -enddate` |
| `docker.conf` | Point the socket test at `/nonexistent`, reload, point it back | Stopping the Docker daemon to test an alert stops everything in Docker, including AlertLoop if it lives there. |
| `worker-heartbeat.conf` | `touch -d '1 hour ago' /var/lib/app-heartbeats/payment-worker` … then `touch` it | Exact, instant, harmless. Run the script by hand first: `check-worker-heartbeat.sh /var/lib/app-heartbeats/payment-worker 300; echo $?` |
| `cron-freshness.conf` | `touch -d '30 hours ago' /var/lib/app-heartbeats/daily-import.ok` … then `touch` it | `rm` it instead to test the missing-file branch, which is a different alert. |
| `cron-exit-code.conf` | `cron-wrapper.sh test-job /bin/false` then `cron-wrapper.sh test-job /bin/true` | Nothing in Monit to test — the wrapper reports this. The first fires, the second resolves. |
| `worker-process.conf` | `systemctl stop payment-worker` … `start` | |
| `alertloop.conf` | Stop AlertLoop, wait, start it | **Expect no alert.** Nothing can report that the notification service is down through the notification service. What you are testing is that the recovery arrives once it is back — and that Monit's own log recorded the outage. See [Watching AlertLoop itself](#watching-alertloop-itself). |

### What a successful test looks like

Two messages, not one:

```
[CRITICAL/incident] mysql: Connection failed        <- the stop
[RESOLVED]          mysql: Connection failed        <- the start, with a duration
```

One message and no recovery means the `else if succeeded` branch is missing,
or its `<check>` argument does not match the firing one — the two must produce
the same `dedupe_key` or AlertLoop has nothing to close. See
[dedupe_key](#dedupe_key-the-one-thing-to-get-right).

Many identical messages means a rule is flapping: raise its `for N cycles`.
That is the check telling you its threshold is wrong, and it is worth fixing
now rather than after you have learned to ignore it.

## Cron jobs

A cron job has two failure modes and they need two different checks.

**It ran and failed.** Wrap it:

```cron
30 3 * * * app /usr/local/bin/cron-wrapper.sh daily-import /opt/app/bin/import.sh
```

The wrapper runs your command, reports `firing` with the last lines of its
output if it fails, reports `resolved` on the next success, touches a freshness
file, and exits with the job's own exit code so nothing else in your setup
changes.

**It never ran at all.** Nothing reports that, because nothing ran. Watch the
freshness file instead — `cron-freshness.conf`. This catches the cron daemon
being down, a crontab entry someone deleted, a job that hung, and a machine that
was off at the scheduled time. None of those produce an error message anywhere.

Give the window generous slack. A daily job that takes 20 minutes gets 26 hours,
not 24: a run that starts a few minutes late should not page you every morning,
and an alert that fires every morning is one nobody reads.

## Workers

Same shape, same reason. `worker-process.conf` checks the process exists;
`worker-heartbeat.conf` checks it is still finishing work. Have the worker touch
a file after each completed unit:

```bash
touch /var/lib/app-heartbeats/payment-worker
```

Do it **after** the work succeeds, not at the top of the loop — a worker that
touches the file and then hangs looks perfectly healthy.

## Writing your own check

The pattern is always the same:

```
check <something> <name>
  if <condition> for N cycles then
    exec "/usr/local/bin/alertloop-monit firing <severity> <check>"
  else if succeeded then
    exec "/usr/local/bin/alertloop-monit resolved <check>"
```

- `<severity>` is `info`, `warning`, or `critical`.
- `<check>` is the aspect being checked — `availability`, `memory`, `usage`,
  `heartbeat`. It becomes the last segment of the `dedupe_key`, which is what
  keeps "postgresql is down" and "postgresql is using too much memory" as two
  separate incidents for the same service.
- `for N cycles` is not optional in practice. Without it, a service restarting
  during a deploy pages somebody.
- The `else if succeeded` line is what closes the incident. Leave it out and the
  incident stays open forever.

## dedupe_key: the one thing to get right

The `dedupe_key` is the identity of an incident. AlertLoop uses it to decide
whether a report is a new problem or the same one continuing.

`alertloop-monit` builds it as `host:service:check` and you will rarely need to
think about it. If you call `alertloop-send` yourself, the rules are:

**Stable for one logical check.** `server-01:postgresql:availability` today,
tomorrow, and during the outage.

**Never** put any of these in it:

- a timestamp — every report becomes a new incident, and none of them ever close
- a random id — same
- the measured value (`cpu-93`) — the key changes as the number moves
- error text that varies between cycles

A resolved incident frees its key, so a service that fails, is fixed, and fails
again correctly opens a **second** incident and notifies again.

## Diagnosing problems

The adapter writes one structured line to stderr per run, and Monit puts stderr
in its own log:

```
level=info  integration=monit status=sent dedupe_key=server-01:postgresql:availability http_status=201 attempt=1
level=error integration=monit status=failed dedupe_key=server-01:postgresql:availability error=connection_refused
```

```bash
sudo tail -f /var/log/monit.log
```

Run the exact command a rule runs, by hand, to see the whole story:

```bash
sudo MONIT_SERVICE=postgresql MONIT_EVENT="Connection failed" \
     MONIT_DESCRIPTION="test" alertloop-monit firing critical availability
```

| What you see | What it usually is |
|---|---|
| `config_not_found` | Monit runs as root; the file is at `/etc/alertloop/monit.env`. Check `--config` if a rule overrides it. |
| `insecure_url` | Plain HTTP to a remote host. Use `https://`, or `127.0.0.1` if AlertLoop is local. |
| `http_status=401` | The key is wrong, or AlertLoop was not restarted after it was added. |
| `http_status=403` | The key exists but its scope is not `ingest`. |
| `http_status=429` | Rate limited. Raise `rate_limit` in AlertLoop, or alert on fewer cycles. |
| `connection_refused` | AlertLoop is down, or the port is not published where you think. |
| `error=timeout` | AlertLoop is up and slow — often its database. Check `/health/ready`. |
| Alerts arrive, recoveries do not | The rule has no `else if succeeded` line. |
| Two incidents for one problem | The `dedupe_key` is not stable — see above. |
| Alerts every cycle | The rule is missing `for N cycles`, or the key contains the measured value. |

## Exit codes

`alertloop-send` and `alertloop-monit` share these. They are a contract: cron
wrappers and your own scripts can depend on them.

| Code | Meaning | Retry? |
|---|---|---|
| 0 | AlertLoop accepted the event | — |
| 2 | Bad arguments or unusable configuration | No — fix it |
| 3 | Could not build the payload | No |
| 4 | AlertLoop returned 4xx | No — the request is wrong |
| 5 | AlertLoop returned 5xx after retries | Later |
| 6 | Could not reach AlertLoop | Later |
| 7 | Retries exhausted | Later |
| 8 | A bug in the adapter | Report it |

## Limitations

Read these before you rely on this in production.

**No local queue.** If AlertLoop is unreachable, the adapter retries (twice by
default, a second apart) and then gives up. The event is **lost** — the log line
is the only trace. A guaranteed-delivery queue on disk is not part of this
version. In practice this matters when AlertLoop is down for longer than a few
seconds, which is exactly when you most want the message.

**A local Monit cannot report that AlertLoop is down**, because the report would
go through AlertLoop. Nothing on this host can work around that. See
[Watching AlertLoop itself](#watching-alertloop-itself).

**Nothing reports that this whole server died.** Monit dies with it. That needs
an external watchdog, by definition.

**Monit runs the adapter synchronously.** A slow AlertLoop delays a Monit check
by up to `ALERTLOOP_TIMEOUT_SECONDS`. The defaults (2s connect, 5s total, 2
retries) bound this at roughly 9 seconds in the worst case; keep them small.

**One-way.** Acknowledging or muting an incident in AlertLoop does not tell
Monit anything, and does not stop Monit from restarting a service.

**Not included in this version:** Netdata, Prometheus Alertmanager, Grafana,
Zabbix, a generic webhook mapper, a resident agent, or a UI for managing Monit.
The directory layout leaves room for `integrations/netdata`,
`integrations/alertmanager`, and so on.

## Watching AlertLoop itself

`alertloop.conf` restarts AlertLoop and logs the failure locally, which fixes
the common case. It cannot notify you through the service that is down.

What actually covers it, and this is not optional for a production install:

- an uptime check against `/health/ready` from a **different machine**,
  notifying through a **different** provider;
- Monit's own mail configuration in `monitrc`, so it can shout about this one
  case without AlertLoop:

  ```
  set mailserver smtp.example.com
  set alert ops@example.com
  ```

The repository's `OPERATIONS.md` covers monitoring AlertLoop in more detail,
including what to alert on from `/v1/stats`.

## Rotating the API key

Nothing is cached, so this is a file edit and no restart:

1. Add a **second** key with scope `ingest` to AlertLoop's config and restart it.
   Both keys now work.
2. On each host: `sudo nano /etc/alertloop/monit.env`, replace the value.
3. Verify with a `--dry-run`, then a real test event.
4. Remove the old key from AlertLoop's config and restart it.

Do it in that order. Removing the old key first means every host stops reporting
until you have visited all of them.

## Uninstall

```bash
sudo ./uninstall.sh            # scripts and this integration's unmodified rules
sudo ./uninstall.sh --purge    # also /etc/alertloop/monit.env
```

It will not remove Monit, will not remove a rule file you edited without asking,
and will not reload Monit on a configuration that does not validate.

Removing the config file does not invalidate the key. Delete it from AlertLoop's
own config too.

## Supported versions

| | |
|---|---|
| AlertLoop | 0.4.0 or later (the incident lifecycle is required) |
| Monit | 5.x |
| OS | Debian 11+, Ubuntu 20.04+, AlmaLinux/Rocky 8+ |
| Shell | bash 4.2+ |

## Tests

```bash
integrations/monit/test/run-tests.sh
```

Argument validation, payload construction and JSON escaping, truncation, the
exit-code contract, the retry policy, and the property that the API key never
appears in any output. Needs `curl`, `jq`, and `python3` (for a mock AlertLoop);
no test framework to install first. CI runs it on every push, along with
`shellcheck`.
