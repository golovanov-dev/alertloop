# Security Policy

AlertLoop is the thing that tells you your systems are broken. A vulnerability
in it is a vulnerability in your ability to find out — so please report one, and
please report it privately first.

## Supported versions

| Version | Supported |
|---|---|
| 0.6.x | ✅ current |
| ≤ 0.5.x | ❌ upgrade |

Before 1.0.0 only the latest minor line is supported. A fix ships as a patch
release of that line and is not backported: upgrading within 0.x is the fix.
The compatibility contract that 1.0.0 publishes will replace this table with a
firmer promise.

## Reporting a vulnerability

**Do not open a public issue.** Use GitHub's
**[Report a vulnerability](../../security/advisories/new)** button on this
repository. It opens a private advisory that only the maintainers can see, and
the fix and the published advisory come out of the same thread.

Please include:

- what you did, in enough detail to reproduce it;
- the version (`alertloop --version`) and how it is deployed (binary, Docker,
  which database);
- what an attacker gains;
- anything you already know about a fix.

You will get an acknowledgement within **3 working days** and an assessment
within **10**. If a report turns out to be a real vulnerability, we will agree a
disclosure date with you, credit you in the advisory and the changelog unless
you would rather stay anonymous, and ship the fix in a patch release for every
supported version.

If you do not hear back within the acknowledgement window, please chase it —
a missed email is far more likely than a decision to ignore you.

## Scope

**In scope:** the AlertLoop server, worker, admin console, API key and admin
token handling, the delivery channels, the configuration loader (including
`${VAR}` substitution), the database migrations, the release artefacts, and the
published container image.

**Out of scope**, because they are your deployment's job and are documented as
such:

- Publishing the API to the internet without TLS in front of it. AlertLoop
  serves plain HTTP by design and expects a reverse proxy — see
  `deploy/proxy/`.
- Denial of service from a client you have given a valid API key to. Rate
  limiting is built in and on by default, but a trusted key is trusted.
- Findings from an automated scanner with no demonstrated impact.

## Security properties you can rely on

These are deliberate, tested behaviours. A break in any of them **is** a
vulnerability:

- Secrets never reach logs, stored delivery errors, the API, or the admin
  console. Telegram bot tokens, proxy passwords and the path and query of
  webhook URLs are redacted at every boundary.
- The admin token is compared in constant time.
- Outbound webhooks are HMAC-SHA256 signed.
- TLS verification cannot be disabled. There is no `--insecure`.
- A configuration referencing an unset variable stops startup rather than
  running with an empty setting.
- With no credential at all — neither `admin_token` nor `api_keys` — the modes
  that serve HTTP do not start, with or without a config file. There is no
  open, unauthenticated mode. Note that the Compose `demo` profile falls back to a fixed admin token
  (`change-me-admin`, in `docker-compose.yml`) when `.env` sets none: it is a
  credential and the API refuses requests without it, but it is published in
  this repository, so change it before that container is reachable by anyone
  else.
- The demo admin token `change-me-admin` is refused with 403 when the request
  carries `X-Forwarded-For`, `Forwarded` or `X-Real-IP` (it came through a
  reverse proxy), or when the connecting address is neither loopback nor
  private (RFC 1918, IPv6 `fc00::/7`, link-local). Other tokens and keys are
  not affected.
- The published container runs as a non-root user with no capabilities, on a
  read-only root filesystem.
- An `ingest` key with `sources` creates, refreshes and resolves only events
  whose `source` is in that list. A request from another source, or one whose
  `dedupe_key` belongs to an event of another source, gets 403
  `source_not_allowed`, changes nothing and is logged as a warning. An
  `ingest` key without `sources` works with every source. An `ingest` key gets
  back only the event's `id`, `state` and `outcome`.

  Limitation: `dedupe_key` is one namespace for the installation. A key can
  open an incident under a `dedupe_key` that another source uses; while that
  incident is open, the other source's reports under that key get 403, and
  its `resolved` reports keep getting 403 after the incident is closed, until
  retention deletes it. Put the host in the key (`host:service:check`). For
  the same reason a limited key can tell that another source has used a
  `dedupe_key` within the retention period: it gets 403 where an unknown key
  gets 201 or 204.

## Keeping your installation safe

- Put HTTPS in front of it. The admin token is sent on every request.
- Give each event source its own `ingest`-scoped API key with `sources`, not
  the admin token. With more than one `ingest` key, each one without `sources`
  is logged as a warning at startup.
- Do not publish the database port. The Compose profile does not.
- Pin the container image to a version tag; `latest` moves under you.
- Watch the releases page: this is a pre-1.0 product and fixes ship in patch
  releases.
