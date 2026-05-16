# =============================================================================
# mqConnector — multi-stage production Dockerfile
# =============================================================================
# Build (default, no IBM MQ, no CGO):
#   docker build -t mqconnector .
# Build (with IBM MQ support — requires CGO + ibmmq build tag):
#   docker build --build-arg BUILD_TAGS=ibmmq -f Dockerfile.ibmmq -t mqconnector:ibmmq .
# Run:
#   docker run -p 8443:8443 -v $PWD/config.yaml:/etc/mqconnector/config.yaml:ro \
#              -v mqc-data:/var/lib/mqconnector mqconnector
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: build the SvelteKit frontend
# -----------------------------------------------------------------------------
FROM node:20-alpine AS web

WORKDIR /src/web

# Lockfile-driven install for reproducibility — copy manifests first to keep
# the layer cache warm across source-only changes.
COPY web/package.json web/package-lock.json* ./
RUN if [ -f package-lock.json ]; then npm ci --no-audit --no-fund; \
    else npm install --no-audit --no-fund; fi

COPY web/ ./
RUN npm run build

# -----------------------------------------------------------------------------
# Stage 2: build the Go binary
# -----------------------------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# The SimpleAuth Go SDK is consumed via a `replace` directive that points at
# a sibling checkout (`../SimpleAuth/sdk/go`). For container builds we bring
# that path in through Docker BuildKit's named contexts (declared in
# docker-compose.yml under `additional_contexts.simpleauth-sdk`). If the
# extra context isn't supplied (plain `docker build`), the COPY falls back
# to nothing and the `replace` line is stripped so the module is resolved
# from the proxy instead.
COPY --from=simpleauth-sdk . /sa-sdk

# Cache modules before bringing in the full source tree.
COPY go.mod go.sum ./
COPY . .

# Rewrite the relative replace path to the in-image SDK location.
RUN if [ -d /sa-sdk ] && [ -f /sa-sdk/go.mod ]; then \
      sed -i 's|=> ../SimpleAuth/sdk/go|=> /sa-sdk|' go.mod; \
    else \
      echo "WARNING: SimpleAuth SDK not in build context; relying on module proxy"; \
      sed -i '/^replace.*simpleauth-go/d' go.mod || true; \
    fi && \
    go mod download

# Pull in the freshly-built frontend (overwrites internal/web/dist/.gitkeep).
COPY --from=web /src/internal/web/dist /src/internal/web/dist

ARG VERSION=docker
ARG BUILD_TAGS=""
ARG CGO_ENABLED_VALUE=0

RUN CGO_ENABLED=${CGO_ENABLED_VALUE} go build \
      -trimpath \
      ${BUILD_TAGS:+-tags ${BUILD_TAGS}} \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/mqconnector \
      ./cmd/mqconnector

# -----------------------------------------------------------------------------
# Stage 3: minimal runtime image
# -----------------------------------------------------------------------------
FROM alpine:3.20

LABEL org.opencontainers.image.title="mqConnector" \
      org.opencontainers.image.description="Production message-queue bridge: IBM MQ / RabbitMQ / Kafka with pipeline, DLQ, and admin UI" \
      org.opencontainers.image.vendor="Department"

# ca-certificates: outbound TLS to SimpleAuth and to MQ brokers.
# tzdata: log timestamps with the operator's local timezone if TZ is set.
RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S mqconnector && \
    adduser -S -G mqconnector -h /var/lib/mqconnector -s /sbin/nologin mqconnector

# Persistent paths: data (SQLite) and config + TLS cert mounts.
RUN mkdir -p /var/lib/mqconnector /etc/mqconnector && \
    chown -R mqconnector:mqconnector /var/lib/mqconnector

COPY --from=build /out/mqconnector /usr/local/bin/mqconnector
COPY --chown=mqconnector:mqconnector config.example.yaml /etc/mqconnector/config.example.yaml

EXPOSE 8443

USER mqconnector
VOLUME /var/lib/mqconnector
WORKDIR /var/lib/mqconnector

# Healthcheck: hits the public /api/health endpoint. Works over HTTPS via
# `wget --no-check-certificate` since dev/internal certs may be self-signed.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --no-check-certificate --spider -q https://localhost:8443/api/health \
   || wget --spider -q http://localhost:8443/api/health \
   || exit 1

ENTRYPOINT ["mqconnector"]
CMD ["-config", "/etc/mqconnector/config.yaml"]
