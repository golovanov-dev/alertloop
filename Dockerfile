# UI stage: build the React admin console so a fresh copy is embedded regardless
# of what is checked in. The output is static files, the same for every
# platform, so it is built once on the build machine: under QEMU emulation for
# arm64, npm crashed with "Illegal instruction" and the image build hung.
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:b6f26b36c8ff49624cfdac716b8ea1138d606df02586a77d364bb5536a634f85 AS ui
WORKDIR /ui
COPY web/admin/package.json web/admin/package-lock.json ./
# `npm ci`, not `npm install`. install is allowed to update the lock file, so
# the published image could contain dependency versions CI never tested - and
# CI uses `npm ci`. For a build that goes straight to users that is a break in
# the supply chain, and it fails silently.
RUN npm ci --no-audit --no-fund
COPY web/admin/ ./
# Vite's outDir points at ../../internal/adminui/dist; recreate that layout so
# the build lands where the Go stage expects to embed it.
RUN mkdir -p /internal/adminui && npm run build

# Build stage: compile a static, CGO-free binary (modernc SQLite is pure Go, so
# the image needs no libc and cross-compiles cleanly).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Overlay the freshly built admin UI from the UI stage.
COPY --from=ui /internal/adminui/dist ./internal/adminui/dist
ARG VERSION=docker
# Cross-compiled for the target platform on the build machine, not emulated.
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/alertloop ./cmd/alertloop

# Runtime stage: minimal image with CA certs for outbound TLS (SMTP/Telegram).
#
# 3.22, not 3.20. Alpine supports a branch for two years, and 3.20 (May 2024)
# stopped receiving security updates in spring 2026 - which stopped being a
# theoretical problem the moment 0.4.0 made the published image the primary
# Docker path instead of a local build. Whatever has been found in busybox,
# musl, or ca-certificates since then would ship to every user.
#
# Base images are pinned by digest (tag kept for reading). Refresh the digests
# before every release, or the image ages past its support window unnoticed.
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 alertloop && \
    mkdir -p /data && chown alertloop:alertloop /data
WORKDIR /data
COPY --from=build /out/alertloop /usr/local/bin/alertloop

# Built-in configuration, so the image runs with no files supplied. The admin
# token has no default: without ALERTLOOP_ADMIN_TOKEN the process refuses to
# start and names the variable. Mount your own file over this path to replace it.
RUN mkdir -p /etc/alertloop && { \
      echo '# Default configuration shipped inside the AlertLoop image.'; \
      echo 'admin_token: ${ALERTLOOP_ADMIN_TOKEN}'; \
      echo 'database:'; \
      echo '  driver: sqlite'; \
      echo '  dsn: /data/alertloop.db'; \
    } > /etc/alertloop/alertloop.yaml

USER alertloop
EXPOSE 8080
ENV ALERTLOOP_CONFIG=/etc/alertloop/alertloop.yaml
# Read by addr: ${ALERTLOOP_ADDR:-...} in alertloop.example.yaml: inside a
# container loopback is unreachable from the published port.
ENV ALERTLOOP_ADDR=:8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/health/ready || exit 1

ENTRYPOINT ["/usr/local/bin/alertloop"]
CMD ["all"]
