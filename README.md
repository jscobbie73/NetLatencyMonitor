# NetLatencyMonitor

Ping Matrix / Latency monitoring & reporting tool — see spec v1.3.

## Status

Phase 0 (foundations) and Phase 1 (agent spool) implemented. Controller HTTP
surface, agent probe modes, ops bundle, and UI land in subsequent phases.

## Build

```
make build      # produces bin/nlm-agent and bin/nlm-controller
make test       # go test -race ./...
make lint       # requires golangci-lint v2
```

Requires Go 1.22+.

