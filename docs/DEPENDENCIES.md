# Dependency analysis

Every external package mqConnector depends on, why it's there, what license it ships under, and what the cost of removing it would be.

The header rule for this codebase (per `CLAUDE.md`): **do not add a third MQ library, do not add a frontend component framework, do not replace SimpleAuth.** Adding a new direct dependency requires user approval.

---

## Backend (Go)

`go.mod` declares **12 direct dependencies**. Everything else in the build graph is a transitive of one of these.

### Direct, by purpose

| Package | Version | Purpose | Why this one | License |
|---|---|---|---|---|
| `github.com/go-chi/chi/v5` | v5.2.5 | HTTP router | Stdlib-shaped API, mature, mux-only (no surprise behaviour). | MIT |
| `github.com/google/uuid` | v1.6.0 | UUID generation | The de-facto Go uuid. Used for ids on every storage row. | BSD-3-Clause |
| `gopkg.in/yaml.v3` | v3.0.1 | YAML config parsing | Stdlib-quality, strict mode. Used only by `internal/config`. | Apache-2.0 / MIT |
| `modernc.org/sqlite` | v1.32.0 | SQLite driver | **Pure Go** — no CGO required. The default build stays cross-compilable. | BSD-3-Clause |
| `github.com/bodaay/simpleauth-go` | local replace | Auth | First-party. Air-gap friendly, no external IdP. Replaced via `go.mod` for now; pin to a tag when SDK stabilises. | (department) |
| `github.com/IBM/sarama` | v1.43.2 | Kafka driver | Reference Go Kafka client. Mature consumer-group support. | MIT |
| `github.com/rabbitmq/amqp091-go` | v1.10.0 | RabbitMQ driver | Official RabbitMQ AMQP 0.9.1 client. | BSD-2-Clause |
| `github.com/ibm-messaging/mq-golang/v5` | v5.6.0 | IBM MQ driver | Official client. CGO + glibc-linked — gated behind the `ibmmq` build tag. | Apache-2.0 |
| `github.com/clbanning/mxj/v2` | v2.7.0 | XML ↔ JSON conversion | The pipeline's translate stage. Battle-tested attribute / namespace handling. | MIT |
| `github.com/beevik/etree` | v1.6.0 | XPath / XML traversal | Used by the XML filter stage (XPath 1.0). | BSD-2-Clause |
| `github.com/eclipse/paho.mqtt.golang` | v1.5.1 | MQTT driver | Reference Eclipse client. v3/v3.1.1/v5 support. | EPL-2.0 |
| `github.com/Azure/go-amqp` | v1.6.0 | AMQP 1.0 driver | Microsoft-maintained, used in their Service Bus SDK; works against Artemis, ActiveMQ, Solace. | MIT |

Plus *indirect* (transitive):

- `google.golang.org/protobuf` — pulled by sarama + reflectively used by the Protobuf format stage. Apache-2.0.
- `nats-io/nats.go` — pulled by the NATS connector. Apache-2.0.
- `eclipse/paho.golang` (v5) — pulled by paho.mqtt.golang.
- `xeipuuv/gojsonschema` — JSON Schema validation (Apache-2.0).
- `lestrrat-go/libxml2` — XSD validation (MIT; CGO behind `xsd` build tag).

### What we deliberately don't depend on

- **No ORM.** Hand-written SQL in `internal/storage` against typed repositories. Migrations are raw `CREATE TABLE IF NOT EXISTS` strings.
- **No DI container.** Server is constructed in `cmd/mqconnector/main.go` with plain constructor calls.
- **No service-discovery / config-reload framework** like Viper. Plain `os.Setenv` overrides on top of `gopkg.in/yaml.v3`.
- **No third-party logging.** `slog` only. JSON output is stdlib.
- **No metrics SDK.** The Prometheus exposition is hand-rolled — a small `MetricsStore` singleton + a text/plain emitter. Avoids a SDK that ships its own protobuf transitive.

### Build-tag–gated dependencies

| Tag | Pulls | Cost |
|---|---|---|
| `ibmmq` | `mq-golang/v5` + the IBM Redistributable Client (~80 MB shared libs) | CGO required; image jumps from alpine to debian-slim because the IBM client is glibc-linked. |
| `xsd` | `libxml2` (CGO) | XSD validation. Off by default; JSON Schema covers most cases. |
| `integration` | (nothing) | Just enables the real-broker tests in `internal/pipeline/integration_*_test.go`. |

### Dependency footprint

```
$ go list -deps ./... | wc -l          # ~800 packages
$ go build ./cmd/mqconnector && ls -lh dist/
-rwxr-xr-x  1 ...  25M  ...  mqconnector       # stripped, default build
-rwxr-xr-x  1 ...  108M ...  mqconnector-ibmmq # CGO + glibc
```

Removing the IBM MQ build path drops the binary to 25 MB. The MQTT/NATS/AMQP-1.0 additions added ~3 MB combined.

### Vulnerability posture

CI bumps trusted upstreams on every release. Last clean scan:

- Backend: no open Dependabot / govulncheck advisories.
- Frontend: 9 open advisories, all Svelte 4 SSR-family — not applicable to `adapter-static`. Closed by the Svelte 5 migration (separate effort).

---

## Frontend (npm)

The `web/package.json` keeps the surface tiny — **no component library**. Tailwind + brand tokens give us layout and color; Lucide gives us icons; everything else is bespoke under `web/src/lib/components/`.

### Runtime dependencies

| Package | Version | Purpose |
|---|---|---|
| `lucide-svelte` | ^1.0.1 | Single icon set. SVG icons via Svelte components — tree-shakable. |

That's it. One runtime dependency.

### Dev / build dependencies

| Package | Version | Purpose |
|---|---|---|
| `svelte` | ^4.2.19 | UI framework. Static-adapter target — no SSR runtime. |
| `@sveltejs/kit` | ^2.60.1 | Routing + build tooling. |
| `@sveltejs/adapter-static` | ^3.0.5 | Static-site build. Output ships embedded in the Go binary. |
| `@sveltejs/vite-plugin-svelte` | ^3.1.2 | Vite ↔ Svelte glue. |
| `vite` | ^5.4.21 | Bundler. |
| `typescript` | ^5.5.4 | TS toolchain. Strict mode. |
| `tailwindcss` | ^3.4.13 | CSS framework. Custom config + the brand-tokens file. |
| `@tailwindcss/forms` | ^0.5.9 | Tailwind form-element reset. |
| `postcss` | ^8.4.47 | Tailwind's required PostCSS pipeline. |
| `autoprefixer` | ^10.4.20 | Vendor-prefix the brand-token CSS. |
| `tslib` | ^2.7.0 | TS helper runtime. |
| `svelte-check` | ^3.8.6 | Type-check Svelte components. |
| `vitest` | ^1.6.1 | Test runner. |
| `@vitest/ui` | ^1.6.1 | Vitest UI — local dev only. |
| `@testing-library/svelte` | ^4.2.3 | Component test helpers. |
| `@testing-library/jest-dom` | ^6.9.1 | DOM matchers. |
| `@testing-library/user-event` | ^14.6.1 | High-fidelity user-event simulation in tests. |
| `jsdom` | ^24.1.3 | DOM impl for vitest. |

### What we deliberately don't depend on

- **No component framework** (Bootstrap, Material UI, Chakra, shadcn-svelte). The UI uses raw Tailwind + brand tokens + bespoke primitives in `web/src/lib/components/`.
- **No state-management library** (Redux, Zustand, Pinia). Plain Svelte stores under `web/src/lib/stores/`.
- **No date library** (moment, date-fns, dayjs). Stdlib `Intl.DateTimeFormat` + `Date` are enough for the timestamp displays we have.
- **No HTTP client.** The `api.ts` wrapper around `fetch` is ~50 lines and shape-checks responses.
- **No CSS-in-JS library.** Tailwind + scoped Svelte styles + brand-tokens.css.

### Bundle footprint

```
$ npm run build
.svelte-kit/output/server/entries/pages/_page.svelte.js   ~130 KB
.svelte-kit/output/server/entries/pages/_layout.svelte.js ~144 KB
.svelte-kit/output/client/                                ~ 600 KB total
```

After gzip + brotli on the wire, the cold-load is ~180 KB. The overview page itself is ~5 KB of route-specific code; everything else is shared chunks.

### Vulnerability posture

Last `npm audit`:

```
13 vulnerabilities (3 low, 10 moderate)
```

All moderate alerts are in the Svelte 4 SSR family. **mqConnector uses `adapter-static`**, which builds the UI as a SPA — there is no SSR runtime exposed, so the attacker paths these advisories describe don't exist in production. They will close automatically when we migrate to Svelte 5 (queued as a follow-up).

The remaining 3 low alerts are in build-time tools (`esbuild` dev-server, `cookie` from kit). Not exposed at runtime.

---

## Internal coupling

mqConnector keeps its internal packages decoupled — there's a clear dependency direction:

```
cmd/mqconnector
       │
       ├──▶ internal/config
       ├──▶ internal/logging
       ├──▶ internal/storage   ◀── internal/secrets
       ├──▶ internal/mq        ◀── internal/mqcfg ◀── internal/secrets
       │           ▲
       │           │
       │     internal/pipeline ◀── internal/sample
       │           │       ▲
       │           │       └── internal/events
       │           │
       │     internal/dlq
       │     internal/metrics
       │     internal/audit
       │     internal/leadership
       │     internal/webhooks (depends on storage + events + http)
       │     internal/tracing
       │
       └──▶ internal/server    (chi routes, middleware, all handlers)
                   │
                   ├──▶ internal/auth
                   ├──▶ internal/health
                   └──▶ internal/web (embed.go)
```

The arrows go one way. `internal/storage` is the leaf — no upstream package imports anything from `server` or `pipeline`. `internal/server` is the trunk and pulls everything together.

This shape exists because we boot top-down in `cmd/mqconnector/main.go`: config → logger → storage → encryption → MQ pool → pipeline manager → server. If two leaves end up needing each other's types, that's the signal to push the shared type up into a third leaf (usually `storage` or a new neutral package).

---

## Updating a dependency

```sh
# Backend — patch / minor bumps
go get github.com/<pkg>@latest
go mod tidy
go test ./...

# Backend — major bumps require a PR with a coverage / behaviour writeup
# Run integration tests if the bumped dep is in the MQ family:
RABBIT_URL=amqp://mqc:mqc-dev@localhost:5672 \
  go test -tags integration ./internal/pipeline/

# Frontend
cd web
npm update <pkg>
npm run check    # svelte-check must stay 0/0
npm test         # vitest must stay green
npm run build    # build must succeed
```

Don't add a new direct dependency without user approval — see the rule in `CLAUDE.md`.

---

## License compatibility

mqConnector itself is MIT-licensed — free to use, modify, redistribute, commercially or otherwise. All direct dependencies use permissive licenses (MIT, BSD-2/3, Apache-2.0, EPL-2.0) that compose cleanly with MIT. No GPL / LGPL in the dependency graph.

The IBM MQ client is Apache-2.0; the IBM Redistributable Client itself is shipped under the [IBM MQ Redistributable Components Terms](https://www.ibm.com/products/mq/resources/license-terms). That license permits redistribution as part of an application; the build artefacts live under `ibmmq_dist/` in the repo.
