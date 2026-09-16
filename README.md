# goThrottler

A small reverse proxy that turns an upstream's hard rate limit into a fair
waiting line: over-limit requests **queue** and go through as capacity frees up,
instead of bouncing back as instant `429`s.

Single static Go binary, stdlib only, ~15 MB container.

## Why

Built for a concrete problem: a local network full of agents (assistant
daemons, cron jobs, dev experiments) all hammering one account at a remote LLM
provider. The provider's RPM/concurrency caps are hard; hit them and you get
`429`s, and every agent reacts differently — some retry in a tight loop, some
crash, some silently give up mid-task.

Putting the proxy in front of the provider gateway fixes this centrally:

- **Concurrency cap** — at most `MAX_CONCURRENCY` requests in flight. A
  streaming SSE response holds its slot until the stream ends, which is the
  right model for LLM calls that take tens of seconds.
- **Sliding-window RPM** — at most `RPM` request starts per 60 s. Only the
  queue head consumes a slot, so a burst lines up instead of racing.
- **FIFO wait queue** — over the limit means waiting, not failing. `429` is
  returned only when the queue is full (`QUEUE_LIMIT`) or the request's own
  wait budget expired (`MAX_WAIT_SECONDS`).

A slow agent becomes a politely queued agent; the retry storm never leaves the
LAN.

The proxy itself stays out of the way: `Authorization` and all headers are
forwarded untouched, so authentication, billing and quotas remain entirely
with the upstream gateway. goThrottler only schedules.

## Limitations

- A few paths are reserved by the proxy: `/healthz`, `/_throttle/stats`,
  `/metrics` (`STATS_PATH`/`METRICS_PATH` are configurable).
- One global queue — no per-route or per-key fairness. A long stream holds a
  slot and slows everyone behind it; size `MAX_CONCURRENCY` accordingly.
- Plain HTTP listener (no TLS termination, no h2c). Terminate TLS in front of
  it if the upstream is reached over an untrusted network.

## Build and run

### Docker (recommended)

```bash
docker build -f deployments/docker/Dockerfile -t gothrottler .
docker run -d -p 8080:3000 \
  -e UPSTREAM_URL=http://billing-gateway:3000 \
  -e MAX_CONCURRENCY=3 -e RPM=10 \
  gothrottler
```

Compose fragment — clients talk to goThrottler instead of the upstream directly:

```yaml
services:
  gothrottler:
    image: ghcr.io/spluft/gothrottler:latest
    environment:
      UPSTREAM_URL: http://billing-gateway:3000
      MAX_CONCURRENCY: "3"
      RPM: "10"
    ports: ["8080:3000"]
```

### From source

```bash
go build -o gothrottler .   # go.mod is stdlib-only, no deps
go test -race ./...
```

### Verify

```bash
curl localhost:8080/healthz          # ok
curl localhost:8080/_throttle/stats  # live scheduler state (JSON)
curl localhost:8080/metrics          # Prometheus text format
```

## Configuration

All settings are environment variables.

| Variable | Default | Description |
|---|---|---|
| `UPSTREAM_URL` | `http://new-api:3000` | Upstream to proxy to. |
| `LISTEN_ADDR` | `:3000` | Listen address. |
| `MAX_CONCURRENCY` | `3` | In-flight request cap; streams hold their slot for the full response. Clamped to ≥1. |
| `RPM` | `10` | Request starts per sliding 60 s window. Clamped to ≥1. |
| `QUEUE_LIMIT` | `50` | Max queued waiters; a full queue gets an immediate `429`. |
| `MAX_WAIT_SECONDS` | `300` | Per-request queue budget; on expiry the request gets `429` and leaves the queue. |
| `REQUEST_TIMEOUT_SECONDS` | `1800` | Per-request timeout toward upstream. `0` disables. |
| `GEO_DB` | "" | Path to a MaxMind-format `.mmdb` file (e.g. DB-IP City Lite). When set, log lines gain `geo="City, Country"`. A bad path is logged and geolocation is skipped. |
| `STATS_PATH` | `/_throttle/stats` | JSON scheduler stats endpoint. |
| `METRICS_PATH` | `/metrics` | Prometheus endpoint. |

## Observability

`/metrics` exports (no client_golang, plain text exposition):

- `gothrottle_inflight`, `gothrottle_queued`, `gothrottle_rate_used_60s` — gauges
- `gothrottle_rpm_limit`, `gothrottle_concurrency_limit` — config
- `gothrottle_admitted_total`, `gothrottle_rejected_total{reason=...}`,
  `gothrottle_upstream_errors_total` — counters
- `gothrottle_queue_wait_seconds` — histogram of queue time
  (p95: `histogram_quantile(0.95, sum(rate(..._bucket[5m])) by (le))`)

Scrape config:

```yaml
- job_name: gothrottler
  static_configs:
    - targets: ["gothrottler:3000"]
```
