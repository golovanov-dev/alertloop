# UI stage: build the React admin console so a fresh copy is embedded regardless
# of what is checked in.
FROM node:22-alpine AS ui
WORKDIR /ui
COPY web/admin/package.json web/admin/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY web/admin/ ./
# Vite's outDir points at ../../internal/adminui/dist; recreate that layout so
# the build lands where the Go stage expects to embed it.
RUN mkdir -p /internal/adminui && npm run build

# Build stage: compile a static, CGO-free binary (modernc SQLite is pure Go, so
# the image needs no libc and cross-compiles cleanly).
FROM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Overlay the freshly built admin UI from the UI stage.
COPY --from=ui /internal/adminui/dist ./internal/adminui/dist
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/alertloop ./cmd/alertloop

# Runtime stage: minimal image with CA certs for outbound TLS (SMTP/Telegram).
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 alertloop && \
    mkdir -p /data && chown alertloop:alertloop /data
WORKDIR /data
COPY --from=build /out/alertloop /usr/local/bin/alertloop

# Built-in configuration, so the image runs with no files supplied. Since
# 0.3.0 the config file is the only source of settings; the environment just
# fills the ${VAR} references in it, which is how the admin token gets in
# without being baked into the image. Mount your own file over this path — or
# point ALERTLOOP_CONFIG elsewhere — to replace it entirely.
RUN mkdir -p /etc/alertloop && { \
      echo '# Default configuration shipped inside the AlertLoop image.'; \
      echo 'admin_token: ${ALERTLOOP_ADMIN_TOKEN:-}'; \
      echo 'database:'; \
      echo '  driver: sqlite'; \
      echo '  dsn: /data/alertloop.db'; \
    } > /etc/alertloop/alertloop.yaml

USER alertloop
EXPOSE 8080
ENV ALERTLOOP_CONFIG=/etc/alertloop/alertloop.yaml
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["alertloop"]
CMD ["all"]
