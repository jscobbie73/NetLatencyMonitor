# Working notes for Claude Code on NetLatencyMonitor

This repository tracks the implementation of the NLM v1.3 spec across
numbered phases. Read this file before starting work in any session.

## Workflow rules

### Every phase completion MUST end with a commit + push + README update

When you finish a phase (any phase, not just the big ones):

1. Run `make test` and `make lint` (or `go test -race ./...` and
   `golangci-lint run ./...`) and ensure both are green. Do not commit
   on red.
2. **Update `README.md`**: bump the phase status table, add or revise
   any run-locally / API examples that the new code introduces, add new
   env vars to the relevant section.
3. **Update this `CLAUDE.md`** if you discovered conventions, gotchas,
   or workflow shortcuts that future sessions should know.
4. Commit on branch `claude/review-spec-plan-Dvwo7` with a descriptive
   message that lists the phase and a short bullet summary of what
   shipped. Use the standard `https://claude.ai/code/...` trailer.
5. `git push -u origin claude/review-spec-plan-Dvwo7`. Verify
   `git rev-parse HEAD` matches `git rev-parse origin/<branch>`.
6. Mark the corresponding TODOs complete; never leave a phase in
   "in_progress" once it's actually shipped.

This rule applies even to small partial-phase commits — if the change
is worth keeping, it's worth pushing and recording in the README.

### Branch discipline

- All development happens on `claude/review-spec-plan-Dvwo7`. Never
  push to `main` without explicit direction from the user.
- Never force-push, reset hard, or amend a commit that has already
  been pushed.
- Do not open a pull request unless the user explicitly asks for one.

### Spec source of truth

The v1.3 spec lives in conversation history (the user pasted it once
during planning). Section references in code comments map directly to
that spec — update them if the user provides a newer revision.

## Current state

| Phase | Scope | State |
|-------|-------|-------|
| 0 | Module skeleton, CI, lint, logger, config, migration runner | Done |
| 1 | Agent spool (WAL retention, exp. backoff, drops counter) | Done |
| 2 | Controller HTTP MVP, `POST /api/v1/results` (201/409/422) | Done |
| 3 | Agent end-to-end: spoke + listener, UUIDv7 IDs | Done |
| 4 | `/readyz` + WS ticket auth + Prometheus metrics | Done |
| 5 | Hub mode, controller-managed targets, admin REST API | Done |
| 6 | Ops: systemd, Caddy, Litestream, Terraform, Litestream journal watcher | Done |
| 7 | Web UI (templ + htmx + WebSocket) | Pending |

Always re-confirm against `README.md` if this table looks stale —
README is the user-facing source of truth for status; this file is the
internal one.

## Key dependency pins (Go 1.22 floor)

The modernc.org/sqlite ecosystem moved its Go floor to 1.25 in
mid-2025. We pinned older versions to keep the user's chosen Go 1.22
floor:

- `modernc.org/sqlite@v1.34.5`
- `github.com/golang-migrate/migrate/v4@v4.18.1`
- `github.com/rs/zerolog@v1.33.0`
- `golang.org/x/sys@v0.30.0`

Before bumping, check that the upgrade path still supports Go 1.22 (or
ask the user about raising the floor).

## Architectural decisions (so they don't get re-litigated)

- **Module path**: `github.com/jscobbie73/netlatencymonitor` (lowercase),
  Go 1.22 floor.
- **DB migrations**: golang-migrate with `embed.FS`, one set per logical
  DB (`internal/spool/migrations`, `internal/controller/migrations`).
  Migration files are append-only; bump filenames, never edit existing
  ones.
- **Auth model (Phase 5 placeholder)**: per-node bearer token in
  `Authorization: Bearer` + `X-NLM-Node-Id` header. Admin REST is
  gated by a separate `NLM_ADMIN_TOKEN` bearer. Session-cookie login
  is deferred to spec Phase 2.
- **Targets**: derived from the `nodes` table, not a separate
  assignment table. Active hubs (`role='hub' AND NOT disabled AND
  address IS NOT NULL`) form every spoke's and hub's target set,
  minus self. `controller_meta.targets_version` rides on results POST
  responses for change notification.
- **Idempotency**: `(source_id, probe_run_id, target_id)` is the
  unique key. 201 = first write, 409 = replay (terminal success).
  Agent treats both as "delete the spool row".
- **Readiness fail-closed**: `/readyz` returns 503 when chrony is
  unavailable. This is intentional per spec §3.2.
- **Web UI is API-first**: admin operations are REST endpoints, not
  CLI subcommands. The Phase 7 UI calls these same endpoints.

## Build / test commands

```
make build      # bin/nlm-agent + bin/nlm-controller
make test       # go test -race -count=1 ./...
make lint       # golangci-lint v2 (config in .golangci.yml)
make tidy       # go mod tidy
```

`golangci-lint` v2 config schema differs from v1 — keep `.golangci.yml`
on `version: "2"` syntax.

## Phase 6 ops notes

- **Deploy layout**: all ops artefacts live under `deploy/` (systemd/, caddy/,
  litestream/, terraform/). Never scatter unit files into `cmd/` or `internal/`.
- **Terraform provider**: Hetzner Cloud (`hetznercloud/hcloud ~> 1.49`). The
  `for_each` over `var.agent_locations` makes it easy to add/remove nodes by
  editing the map — no code changes.
- **Litestream watcher**: `internal/litestream.Watcher` polls `systemctl is-active`
  and `journalctl --since` on each tick; it drives the `Tracker` that `/readyz`
  and `/metrics` consume. The `Querier` interface (same pattern as `chrony`) is
  injectable for tests — no real systemd needed.
- **`make install` DESTDIR support**: `DESTDIR` prefix lets you stage the install
  into a temp dir (e.g. for packaging): `make install DESTDIR=/tmp/pkg`.
- **Litestream env file**: `/etc/nlm/litestream.env` must contain
  `LITESTREAM_ACCESS_KEY_ID`, `LITESTREAM_SECRET_ACCESS_KEY`,
  `LITESTREAM_S3_BUCKET`, `LITESTREAM_S3_REGION`. For non-AWS stores also set
  `LITESTREAM_S3_ENDPOINT`.

## Common gotchas

- **`gofmt` failures** show up after editing struct fields. Run
  `gofmt -w <file>` and re-lint.
- **`bodyclose` on httptest** wants every Dial / response body closed
  even when the test only cares about status. Wrap in
  `defer func() { _ = resp.Body.Close() }()`.
- **`sqlclosecheck`** wants `defer rows.Close()`, not explicit close
  paths. Use anonymous-function scope to localize the defer when
  reading rows in a function that doesn't immediately return.
- **modernc sqlite UNIQUE violation detection**: match
  `strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")`
  rather than depending on driver-internal error types.
- **Test isolation**: each Server constructs its own
  `prometheus.Registry` (`internal/metrics.New()`) to avoid the global
  default registry across parallel tests.
