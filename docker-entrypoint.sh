#!/bin/sh
#
# AlertLoop container entrypoint.
#
# Its only job is to turn one specific startup failure into an instruction.
#
# The image ships a config that reads the admin token from
# ${ALERTLOOP_ADMIN_TOKEN} with no default. Without it AlertLoop refuses to
# start - correctly, because an empty admin token and no API keys means the API
# accepts everything with full scope, and `docker run` with no environment would
# otherwise hand out an unauthenticated service that can create, read, and
# modify events and replay deliveries.
#
# Refusing is right. Refusing with "config references environment variable(s)
# that are unset" tells a newcomer nothing about what to do, so this says it.
set -eu

if [ -z "${ALERTLOOP_ADMIN_TOKEN:-}" ] && [ "${ALERTLOOP_CONFIG:-/etc/alertloop/alertloop.yaml}" = "/etc/alertloop/alertloop.yaml" ]; then
    # Only when running on the BUILT-IN config. An operator who mounted their
    # own file has said what they want, including api_keys instead of an admin
    # token, and this check has no business second-guessing it.
    if [ ! -s /etc/alertloop/alertloop.yaml ] || grep -q 'shipped inside the AlertLoop image' /etc/alertloop/alertloop.yaml; then
        cat >&2 <<'MSG'
AlertLoop needs an admin token before it will start.

It protects the admin console at /admin and grants full API access. Without it,
and with no API keys configured, the API would accept every request from anyone
who can reach it.

  docker run -e ALERTLOOP_ADMIN_TOKEN="$(openssl rand -hex 32)" \
    -p 127.0.0.1:8080:8080 -v alertloop-data:/data \
    ghcr.io/golovanov-dev/alertloop:latest

Publish the port on 127.0.0.1, not 0.0.0.0: a published Docker port is not
filtered by ufw or firewalld.

To configure channels, routing, and scoped API keys instead, mount your own
file over /etc/alertloop/alertloop.yaml (start from alertloop.example.yaml).
MSG
        exit 1
    fi
fi

exec alertloop "$@"
