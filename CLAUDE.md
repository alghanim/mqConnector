# CLAUDE.md — agent context for mqConnector

This file gives an AI agent everything it needs to be productive in this repo without re-discovering it from scratch.

## What this project is

A single-binary message-queue bridge: consume from one MQ (IBM MQ / RabbitMQ / Kafka), apply a pipeline (validate → filter → transform → translate → route → script), forward to another MQ. Configuration lives in SQLite. The admin UI is SvelteKit, embedded via `go:embed`.

## Stack — this is fixed, do not change

- **Backend:** Go 1.22+, single binary
- **Router:** `chi`
- **Logging:** `slog` (standard library), JSON output
- **Config:** YAML on disk + env var overrides
- **Storage:** SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- **Auth:** SimpleAuth (https://github.com/bodaay/SimpleAuth) — never OIDC for this app
- **Frontend:** SvelteKit + TypeScript strict + Tailwind, static adapter, embedded into the binary
- **MQ libs:** `github.com/ibm-messaging/mq-golang/v5` (IBM, behind `ibmmq` build tag — CGO required), `github.com/rabbitmq/amqp091-go` (RabbitMQ), `github.com/IBM/sarama` (Kafka)

If you think you need a different choice, **stop and ask**. The standards repo is what made these calls.

## Repository layout

```
cmd/mqconnector/main.go     Entrypoint — wire config → logger → storage → mq.Pool → pipeline.Manager → server.Server, then Run with graceful shutdown
internal/config/            YAML loader + env override + validation. Single Config struct, one Load() function.
internal/logging/           slog wrapper. Returns *slog.Logger. Use logging.FromContext(ctx) in handlers.
internal/storage/           SQLite open + migrations + typed repositories per collection (Connections, Pipelines, DLQ, Scripts).
internal/auth/              SimpleAuth wrapper. Middleware: RequireSession.
internal/mq/                Connector interface + 3 implementations + Pool (sync.Map keyed by id, with health check + eviction).
internal/pipeline/          Stage interface + 6 stage types + Manager (loads from storage, hot-reloads on update, owns goroutines per pipeline).
internal/dlq/               DLQ entries + ListMessages/GetMessage/IncrementRetry/DeleteMessage/Retry (Retry actually re-publishes).
internal/metrics/           MetricsStore singleton + Prometheus exposition.
internal/health/            Check(): assembles HealthStatus from DB ping + metrics snapshot.
internal/server/            Server struct, NewServer(deps), Run(ctx). Routes split across handler files. Embeds web/dist via internal/web/embed.go.
internal/web/embed.go       go:embed for SvelteKit build output in internal/web/dist/.
web/                        SvelteKit source. Builds into internal/web/dist via static adapter.
scripts/                    build.sh, build-dist.sh, dev.sh, version-bump.sh.
```

## Build / run commands

```sh
./scripts/build.sh                 # default: no CGO, no IBM MQ
./scripts/build.sh --ibmmq         # with IBM MQ (needs ibmmq_dist/ + CGO)
./scripts/dev.sh                   # local dev: TLS off-able, debug logs
./scripts/build-dist.sh            # tarball for deployment
./scripts/version-bump.sh patch    # bump VERSION (always confirm with user first)
go test ./...                      # full test suite
cd web && npm run build            # frontend only
```

## Things to always confirm with the user before doing

- Bumping `VERSION`
- Committing or pushing
- Deleting any files
- Installing new dependencies (`go get` or `npm install <new>`)
- Editing `COMPLIANCE.md` or `BRAND-COMPLIANCE.md`
- Running `git reset --hard`, force pushes, or anything else destructive

## Things to always do

- Add structured logs with context — `slog.Info("msg", "key", val)`, no `fmt.Println`
- Add a test when adding a stage type, a connector, or a non-trivial helper
- Update both dark + light themes when touching UI; never check in a one-theme-only component
- Use CSS logical properties in the frontend — `margin-inline-start`, not `margin-left`
- Use only brand tokens for color — never a raw hex outside `web/src/lib/brand-tokens.css`
- Run `go test ./...` and `cd web && npm run check` before declaring work done

## Things to never do

- Add a third MQ library "just in case"
- Replace SimpleAuth with OIDC/OAuth — this is an air-gapped department app
- Add Bootstrap, Material UI, Chakra, or any other component library — Tailwind + brand tokens only
- Use `#000000` for any dark background or `#FFFFFF` for any light background
- Use the brand maroon (`#8B153D`) on anything that isn't a primary CTA, destructive action, or count badge
- Hardcode a hex value in a component — read it from a CSS variable defined in `brand-tokens.css`
- Skip TLS in any non-dev mode

## Where the bodies are buried (state of the rewrite)

This codebase was rewritten from a PocketBase-based prototype. None of the old code remains. If you find a reference to `pocketbase`, `Data.StartDB`, `MQ_FILTERS`, or `models.Node`, you are reading stale memory / docs — flag it.

## Reference documents

- `COMPLIANCE.md` — full coding-standard checklist
- `BRAND-COMPLIANCE.md` — branding-guide compliance
- `README.md` — user-facing build/deploy/run
- `config.example.yaml` — full config reference
