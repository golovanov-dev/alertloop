# Security Policy

AlertLoop is the thing that tells you your systems are broken. A vulnerability
in it is a vulnerability in your ability to find out — so please report one, and
please report it privately first.

## Supported versions

| Version | Supported |
|---|---|
| 0.5.x | ✅ current |
| ≤ 0.4.x | ❌ upgrade |

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
- The admin token in the `?token=` query parameter on the built-in pages. This
  is a **known and consciously accepted** tradeoff, not an oversight: it is what
  lets an event list be opened from a plain browser link. It risks exposure via
  browser history and reverse-proxy access logs; `Referrer-Policy: no-referrer`
  is set to stop it leaking onward via `Referer`. A cookie session is on the
  roadmap. The admin console at `/admin` does not use the query parameter.
- Denial of service from a client you have given a valid API key to. Rate
  limiting is built in and on by default, but a trusted key is trusted.
- Findings from an automated scanner with no demonstrated impact.

## Security properties you can rely on

These are deliberate, tested behaviours. A break in any of them **is** a
vulnerability:

- Secrets never reach logs, stored delivery errors, the API, or either web
  interface. Telegram bot tokens and proxy passwords are redacted at every
  boundary.
- The admin token is compared in constant time.
- Outbound webhooks are HMAC-SHA256 signed.
- TLS verification cannot be disabled. There is no `--insecure`.
- A configuration referencing an unset variable stops startup rather than
  running with an empty setting — an empty `admin_token` would leave the API
  open.
- A config file that names no credential at all — neither `admin_token` nor
  `api_keys` — stops startup in the modes that serve HTTP, instead of serving an
  API that accepts every request with full scope. Running open is possible only
  with no config file at all: a binary started without `--config` and without
  `ALERTLOOP_CONFIG`, which then also listens on every interface. Container
  images always ship a config file, so no container takes that path. Note that
  the Compose `demo` profile falls back to a fixed admin token
  (`change-me-admin`, in `docker-compose.yml`) when `.env` sets none: it is a
  credential and the API refuses requests without it, but it is published in
  this repository, so change it before that container is reachable by anyone
  else.
- The published container runs as a non-root user with no capabilities, on a
  read-only root filesystem.

## Keeping your installation safe

- Put HTTPS in front of it. The admin token is sent on every request.
- Give each event source its own `ingest`-scoped API key, not the admin token.
- Do not publish the database port. The Compose profile does not.
- Pin the container image to a version tag; `latest` moves under you.
- Watch the releases page: this is a pre-1.0 product and fixes ship in patch
  releases.
