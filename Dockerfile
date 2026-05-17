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

# go mod download invokes git when a `replace` directive can't be
# satisfied locally (the fallback path in CI when no simpleauth-sdk
# build-context is supplied). Alpine ships without git, so install it
# defensively — keeps the layer tiny and means the fetch path works.
RUN apk add --no-cache git

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
# Stage 3a: prep the runtime filesystem
# -----------------------------------------------------------------------------
# Distroless images don't have a shell, useradd, mkdir, or chown — they
# are intentionally minimal. We do all the filesystem prep in an
# intermediate alpine image and then COPY the result into distroless.
FROM alpine:3.20 AS runtime-prep

# ca-certificates + tzdata come from alpine and get copied into the
# distroless final stage via their well-known paths.
RUN apk add --no-cache ca-certificates tzdata

# Create the data dir and config dir with a known UID/GID matching
# the distroless `nonroot` user (65532:65532). Distroless ships with
# that user predefined; we just have to own the directories with the
# matching numeric IDs since distroless has no shell to chown later.
RUN mkdir -p /out/var/lib/mqconnector /out/etc/mqconnector && \
    chown -R 65532:65532 /out/var/lib/mqconnector

# -----------------------------------------------------------------------------
# Stage 3b: final runtime image — distroless static
# -----------------------------------------------------------------------------
# gcr.io/distroless/static-debian12:nonroot ships:
#   - a single static-binary entrypoint slot,
#   - the nonroot user (65532:65532),
#   - ca-certificates (we still bring in alpine's copy for parity),
#   - tzdata,
#   - NO shell, NO package manager, NO wget. Attack surface is the
#     Go binary plus the kernel.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="mqConnector" \
      org.opencontainers.image.description="Production message-queue bridge: IBM MQ / RabbitMQ / Kafka / NATS / MQTT / AMQP 1.0 with pipeline, DLQ, and admin UI" \
      org.opencontainers.image.vendor="Department" \
      org.opencontainers.image.source="https://github.com/alghanim/mqConnector" \
      org.opencontainers.image.documentation="https://github.com/alghanim/mqConnector/blob/main/OPERATIONS.md" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian12:nonroot"

# Pull TLS roots + timezone DB from the alpine prep stage. Distroless
# does include these, but pinning to alpine's matches local-dev
# expectations and makes upgrades visible.
COPY --from=runtime-prep /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-prep /usr/share/zoneinfo /usr/share/zoneinfo

# Persistent paths with the right ownership (65532:65532 → nonroot).
COPY --from=runtime-prep /out/var/lib/mqconnector /var/lib/mqconnector
COPY --from=runtime-prep /out/etc/mqconnector /etc/mqconnector
COPY --chown=65532:65532 config.example.yaml /etc/mqconnector/config.example.yaml

# The static binary. CGO_ENABLED=0 + -ldflags "-s -w" + a Go std
# library — no glibc deps, runs fine on distroless static.
COPY --from=build /out/mqconnector /usr/local/bin/mqconnector

EXPOSE 8443
USER nonroot:nonroot
VOLUME /var/lib/mqconnector
WORKDIR /var/lib/mqconnector

# Healthcheck — uses the binary's own `healthcheck` subcommand
# because distroless has no shell or wget. The probe hits the public
# /api/health endpoint over HTTPS with InsecureSkipVerify=true so
# self-signed dev certs work; production deployments either trust
# the cert or override --insecure=false at the Docker level.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/mqconnector", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/mqconnector"]
CMD ["-config", "/etc/mqconnector/config.yaml"]
