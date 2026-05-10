# NetLatencyMonitor

Ping-matrix / TCP-latency monitoring & reporting tool. See spec v1.3 for the
full design; this README tracks what's been built so far.

## Status

| Phase | Scope | State |
|-------|-------|-------|
| 0 | Module skeleton, CI, lint, logger, config, migration runner | Done |
| 1 | Agent spool (WAL + retention with WAL-inclusive footprint, exp. backoff, drops counter) | Done |
| 2 | Controller HTTP MVP, `POST /api/v1/results` (201/409/422 contract) | Done |
| 3 | Agent end-to-end: spoke + listener, UUIDv7 probe IDs, full HTTP outcome mapping | Done |
| 4 | `/readyz` (chrony fail-closed + Litestream lag), WS ticket auth + per-IP rate limit, Prometheus metrics | Done |
| 5 | Hub mode, controller-managed target list with version-piggyback, admin REST API | Done |
| 6 | Ops: systemd units, Caddy, Litestream config, Terraform (Hetzner), `make install`, Litestream journal watcher | Done |
| 7 | Web UI (templ + htmx + WebSocket) — dashboard, node CRUD, real-time matrix | Done |
| 8 | Spec v1.3 compliance hardening: post-drain spool metadata, drops metric delta tracking, metric label normalization, WS origin policy, Litestream startup seeding | Done |

## Build & test

```
make build      # produces bin/nlm-agent and bin/nlm-controller
make test       # go test -race ./...
make lint       # requires golangci-lint v2
make templ      # regenerate *_templ.go from *.templ sources (requires Go)
```

Requires Go 1.22+.

## Run locally (dev)

### 1. Start the controller

```
NLM_CONTROLLER_DB_PATH=/tmp/nlm.db \
NLM_CONTROLLER_LISTEN=:8080 \
NLM_ADMIN_TOKEN=devadmin \
NLM_CHRONYC_BINARY=/usr/bin/chronyc \
./bin/nlm-controller
```

Health: `GET /healthz`. Readiness: `GET /readyz` (fails closed if chrony
isn't reachable). Metrics: `GET /metrics` (gate with `NLM_METRICS_TOKEN`
in production).

Optional controller env vars:

| Env var | Default | Purpose |
|---------|---------|---------|
| `NLM_WS_ALLOWED_ORIGINS` | _(empty)_ | Comma-separated extra WebSocket origins to allow (same-host + no-origin always allowed) |
| `NLM_LITESTREAM_MAX_LAG_SECONDS` | `300` | `/readyz` lag gate; also used as the startup journal lookback so tracker seeds correctly after restart |

### 2. Provision nodes via the admin API

```
curl -H "Authorization: Bearer devadmin" \
     -H "Content-Type: application/json" \
     -d '{"id":"hub-01","role":"hub","address":"127.0.0.1:8444"}' \
     http://localhost:8080/api/v1/admin/nodes
# → 201 with the minted secret in the response (shown ONCE)

curl -H "Authorization: Bearer devadmin" \
     -H "Content-Type: application/json" \
     -d '{"id":"spoke-01","role":"spoke","secret":"chosen-by-operator"}' \
     http://localhost:8080/api/v1/admin/nodes
```

Patch / delete: `PATCH /api/v1/admin/nodes/{id}`,
`DELETE /api/v1/admin/nodes/{id}`. The web UI (Phase 7) calls these
endpoints; they're API-first by design.

### 3. Run the spoke

```
NLM_NODE_ID=spoke-01 \
NLM_NODE_SECRET=chosen-by-operator \
NLM_CONTROLLER_URL=http://localhost:8080 \
NLM_SPOOL_PATH=/tmp/spoke-spool.db \
./bin/nlm-agent --mode spoke --once
```

The spoke fetches its target list from the controller (every active
hub-role node, minus self). `NLM_PROBE_TARGETS=id=host:port,…` overrides
this for tests / static deployments.

### 4. Run a hub or listener

```
# Hub: long-running daemon, probes every NLM_HUB_PROBE_INTERVAL
NLM_NODE_ID=hub-01 NLM_NODE_SECRET=... NLM_CONTROLLER_URL=http://localhost:8080 \
  ./bin/nlm-agent --mode hub

# Listener: TCP target service for spokes to dial
NLM_LISTENER_LISTEN=:8444 ./bin/nlm-agent --mode listener
```

## Design notes

- **Durability**: agents spool every probe cycle locally (SQLite WAL +
  `synchronous=NORMAL`), drain on every cycle, and tolerate controller
  outages up to retention bounds (24h or 100MB). Retention enforces
  *both* age and size, with footprint inclusive of `.db-wal` / `.db-shm`.
- **Idempotency**: `POST /api/v1/results` is idempotent on
  `(source_id, probe_run_id, target_id)`. 201 = first write, 409 = replay
  (terminal success), 422 = malformed.
- **Targets**: every spoke and hub probes the active hub set. Adding a
  hub via the admin API automatically routes traffic to it; the
  controller bumps a monotonic `targets_version` and echoes it on every
  results POST so agents refresh their cache without polling.
- **Readiness**: `/readyz` fails closed when chrony is unavailable or
  Litestream lag exceeds the gate.
- **Observability**: every metric named in spec §11.1 is exposed at
  `/metrics`, gated by an optional bearer token.

## Web UI (Phase 7)

Once the controller is running, open `https://<your-domain>/ui/login` and enter
`NLM_ADMIN_TOKEN`. The session cookie is HttpOnly + SameSite=Strict.

| Route | What you get |
|-------|-------------|
| `/ui/` | Live latency matrix — hub × hub, 5-min average, WebSocket real-time updates |
| `/ui/nodes` | Add / disable / delete nodes; one-time secret shown on create |

The dashboard matrix auto-updates via WebSocket on every new probe result
(`POST /api/v1/results` → 201). The matrix also polls every 30 s via htmx as
a fallback.

The static CSS is embedded in the binary (`internal/ui/static/`). htmx is
loaded from the unpkg CDN; self-host it for airgapped deployments by replacing
the `<script>` tag in `internal/ui/layout.templ` and running `make templ`.

## Production deployment (Phase 6)

### AWS deployment (recommended)

See **[DEPLOY.md](DEPLOY.md)** for the full step-by-step walkthrough.

`deploy/aws/` contains Terraform for 7 t3.micro instances across 6 regions:
- Controller (us-east-1) — `nlm-controller` + Caddy + Litestream
- Hubs (us-east-1, us-west-1, eu-central-1) — probed by all spokes
- Spokes (ap-southeast-1, ap-northeast-1, sa-east-1) — outbound probers only

Quick start:

```bash
cd deploy/aws
# 1. Fill in your values
cat > terraform.tfvars <<EOF
ssh_public_key_path  = "~/.ssh/id_ed25519.pub"
admin_token          = "<strong-random-token>"
litestream_s3_bucket = "nlm-backup-<unique-suffix>"
controller_domain    = "nlm.example.com"
acme_email           = "you@example.com"
EOF

# 2. Provision
terraform init && terraform apply

# 3. Point DNS A record for controller_domain → controller_ip output, then:
export NLM_ADMIN_TOKEN="<same-token>"
cd ../..
scripts/deploy.sh     # builds, uploads, registers nodes, starts services
```

### Manual / Hetzner deployment

```
cd deploy/terraform
cp terraform.tfvars.example terraform.tfvars   # fill in hcloud_token etc.
terraform init && terraform apply
```

Terraform provisions a controller node + N agent nodes on Hetzner Cloud and
outputs their IP addresses.

```
# Build locally, then scp binaries, or use CI artefacts.
make install                     # copies bin/* + systemd units
systemctl daemon-reload

# Controller node:
systemctl enable --now nlm-litestream nlm-controller

# Agent nodes:
systemctl enable --now nlm-agent-listener nlm-agent-hub
systemctl enable --now nlm-agent-spoke.timer
```

Env files live in `/etc/nlm/` (controller.env, litestream.env, agent.env).
The Litestream config lives in `/etc/litestream/litestream.yml`
(see `deploy/litestream/litestream.yml`).

The Caddy reverse-proxy config is in `deploy/caddy/Caddyfile` — copy it to
`/etc/caddy/Caddyfile` and reload Caddy.

## Repository layout

```
cmd/
├── nlm-agent/        # spoke / hub / listener modes
└── nlm-controller/   # HTTP API + DB + metrics
deploy/
├── aws/              # AWS Terraform (7 t3.micro across 6 regions) + user_data.tpl
├── caddy/            # Caddyfile (TLS reverse proxy)
├── litestream/       # litestream.yml (S3 replication)
├── systemd/          # *.service + *.timer units
└── terraform/        # Hetzner Cloud infra (main.tf, variables.tf, outputs.tf)
scripts/
└── deploy.sh         # build + push + register nodes for AWS deployment
internal/
├── agent/            # probe loop, HTTP sender, target cache, hub runner
├── chrony/           # `chronyc tracking` parser
├── config/           # env-var configuration loaders
├── controller/       # HTTP handlers, schema, admin API, WS
├── dbmigrate/        # shared golang-migrate runner
├── litestream/       # replication health tracker + journal watcher
├── logging/          # zerolog wrapper
├── metrics/          # Prometheus registry
├── spool/            # agent write-ahead spool
└── ticket/           # WS ticket store + per-IP rate limit
```
