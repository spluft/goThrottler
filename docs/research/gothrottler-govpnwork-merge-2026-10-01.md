# goThrottler × goVpnWork merge study — decision record (2026-10-01)

**Epic:** t_b7bf6ba0 | **Author:** architect (Hermes) | **Date:** 2026-10-01 (UTC)
**Scope:** analysis + decision record only. No code, no compose changes, no deploy.
**Verification window:** all live-state claims below were checked against Portainer env 3
("local", https://192.168.1.200:9443) and plain-LAN HTTP probes on 2026-10-01T22:14Z–22:20Z.
Claims that could not be verified from this vantage point are marked **UNVERIFIED** and
listed in §9 as open questions for the owner.

---

## 1. Live topology (re-verified 2026-10-01T22:14Z–22:20Z)

### 1.1 Lanes as they stand today

```
LANE 1 (public/LAN, throttled)          LANE 2 (corporate, UNTHROTTLED)
client ─▶ gothrottler :3010              client ─▶ vpn-gateway :1234
            queue conc=3, RPM=15,                      OpenAI /v1/* + SSE proxy
            queue=50, wait≤600s                        (NO rate limiting, verified)
            REQUIRE_AUTH=1                             GATEWAY_API_KEY set (auth ON)
            GeoIP + key-fingerprint logs               VPN client (SSTP + 2FA)
            discovery cache TTL 300s                   SOCKS5 :1080 (allowlist)
            │                                          │
            ▼                                          ▼
         new-api :3000 (billing)               api.ai.gnivc.ru (via SSTP tunnel,
         calciumion/new-api:v1.0.0-rc.40                TARGET qwen3.5-122b family;
         GLOBAL_API_RATE_LIMIT=180/180s                 extra_hosts pin 10.255.169.49)
```

**Both lanes verified healthy at 22:15Z–22:16Z:** gothrottler container `Up 4 hours
(healthy)` (image `gothrottler:e89c203`); vpn-gateway `Up 4 hours (healthy)`
(`ghcr.io/spluft/govpnwork/vpn-gateway:v0.0.6@sha256:2218a4…`); `GET :1234/health` →
`{"data":{"status":"healthy","vpn":"connected"},"success":"true"}`; `GET :1234/version` →
`{"data":{"version":"v0.0.6","git":"1c296f4de480c7535f5293b1ade3de2154a75720",…}}`.

### 1.2 Throttler config (live container env, stack 130 file, 22:15Z)

| Env | Live value |
|---|---|
| UPSTREAM_URL | `http://new-api:3000` |
| MAX_CONCURRENCY | `3` |
| RPM | `15` |
| QUEUE_LIMIT | `50` |
| MAX_WAIT_SECONDS | `600` |
| REQUEST_TIMEOUT_SECONDS | `1800` |
| REQUIRE_AUTH | `1` (any `Authorization`/`X-API-Key` header; value NOT validated — upstream does billing) |
| GEO_DB | `/geo/dbip-city-lite.mmdb` (RO bind `/opt/monitoring/geoip`) |
| DISCOVERY_CACHE_TTL_SECONDS | unset → default 300s; paths `/v1/models,/version,/api/tags,/v1/props`, keyed per credential |
| image | `gothrottler:e89c203` (= repo HEAD `e89c203` "Add discovery response cache") |

Live `/healthz` → 200 `ok` (22:16Z). Live `/_throttle/stats` (22:16Z):
`{"admitted_total":2065,"conc_limit":3,"inflight":0,"queued":0,"rate_used_60s":15,
"rejected_full":0,"rejected_wait":0,"rpm_limit":15}` — i.e. the queue has been exercised
2065 admissions, zero full-queue/wait rejections at probe time. `/health` and `/version`
behind the throttler return `401 {"error":{"message":"goThrottle: missing
credential"}}` when probed without a header (REQUIRE_AUTH), `/healthz`, `/_throttle/stats`,
`/metrics` are open.

### 1.3 vpn-gateway config (live container env, stack 61 file, 22:15Z)

| Env | Live value |
|---|---|
| image | `v0.0.6@sha256:2218a4021cf9d62f6e36d17af3d0b693d2820525822faa92ec0de4431e0e737b` |
| HTTP_PORT → host | `1234` (8080 in container) |
| SOCKS5 → host | `1080` |
| VPN_TARGET_URL / API key | `https://api.ai.gnivc.ru/` + `VPN_TARGET_API_KEY` set |
| GATEWAY_API_KEY | **set** (auth ON; `/v1/*` and `/metrics` → 401 without it, verified 22:16Z) |
| PROXY_TIMEOUT / PROXY_RESPONSE_HEADER_TIMEOUT | `60s` / `10m` |
| VPN_2FA_WAIT_TIMEOUT | unset → default `5m` |
| VPN_AUTO_RECONNECT / delays | unset → default `true`, 30s → x2.0 → 5m, retries 0 (unlimited) |
| VPN_TARGET_NETWORK / IP | `10.0.0.0/8` / `10.250.28.252` |
| SOCKS5_USER/PASS + SOCKS5_ALLOW_DEST | spluft + allowlist: mail, bitbucket, jira, jks, nex, dev, uat, confluence + 172.16.93.81/.94.9/subnets |
| TELEGRAM_BOT_TOKEN / CHAT_ID | set / 36525961; TELEGRAM_PROXY_URL `socks5://192.168.1.200:9000` |
| TGNTFY_URL/TGNTFY_TOKEN | `http://192.168.1.200:8091` + token |
| caps / devices | NET_ADMIN, SYS_ADMIN, NET_RAW + `/dev/ppp` |

**No admission control in code (verified):** `grep` over `internal/gateway`,
`internal/metrics`, `internal/vpn` shows zero `rate.Limiter`, zero semaphore, zero
admission queue. `gateway_requests_queued` exists as a Prometheus gauge in
`internal/metrics/metrics.go:95` but is **never set** by gateway code (only in
metrics tests) — a dead metric today (see VW-3 in the audit). The only "queuing" is
Go's HTTP server itself.

### 1.4 Who points at which lane today (verified client map)

| Client | Lane / endpoint | Evidence |
|---|---|---|
| hermes-work (container) | **:3010** (provider `spluft`, base_url `http://192.168.1.200:3010/v1`, default model qwen3.8-27b) | config.yaml read from container archive (22:17Z); auxiliary compression provider also :3010 |
| hermes-spluft | **:3010** (20 refs, incl. every custom provider) | config archive 22:17Z |
| hermes-wifu | **:3010** (22 refs) | config archive 22:17Z |
| hermes-family | **:3010** (13 refs; custom:spluft) | config archive 22:17Z |
| goRecomendarr ai-service | **:1234** in stack 121 compose (`OPENAI_URL=http://192.168.1.200:1234/v1`) — stack INACTIVE | stack121 yaml (historical), stack 121 inactive 22:15Z |
| confluence MCP, jenkins MCP (hermes host tools) | **SOCKS5 :1080** (socks5h://…@192.168.1.200:1080) | /opt/data/config.yaml mcp_servers (22:18Z) |
| goYoutube yt-dlp, gorecomendarr metadata, vpn-gateway telegram | **SOCKS5 :9000** (mihomo/router proxy, not the vpn-gateway :1080) | stack 128 file; stack121 files; vpn-gateway env (22:15–18Z) |
| cron jobs (this host) | neither `:1234` nor `:3010` referenced in jobs.json | grep 22:18Z |

**Key re-verification result:** the PM intake's premise "hermes agents + cron + jenkins hit
the 122B model through :1234 unthrottled" is **NOT confirmed for the .200 fleet**: every
hermes agent on this host is configured against :3010 (throttled), cron jobs on this host
reference neither port, and the two MCP tools use the SOCKS5 allowlist (L4, not the LLM
lane). The only verified `:1234` consumer is goRecomendarr's ai-service in an **inactive**
stack, plus a stale context-length cache entry (`qwen3.8-flash@http://192.168.1.200:1234/v1`
in `/opt/data/context_length_cache.yaml` — evidence that *some* hermes instance probed
:1234 at some point). Consumers **outside this container's vantage** (gnivc-side agents,
jenkins-xray inside the corporate network, laptop tools) could not be enumerated →
**UNVERIFIED** (open question Q1). The weak spot is therefore **structural** ("the lane
exists unthrottled and nothing prevents a tool from pointing at it") rather than an
observed active storm today.

### 1.5 R4 evidence — actual metric names (fetched 22:16Z)

`GET :3010/metrics` (open) and `GET :1234/metrics` (with the gateway key, locally only —
never in this document) returned the following distinct top-level names:

- **gothrottler (:3010):** `go_goroutines`, `go_memstats_alloc_bytes`,
  `gothrottle_admitted_total`, `gothrottle_client_aborts_total`,
  `gothrottle_concurrency_limit`, `gothrottle_discovery_cache_hits_total`,
  `gothrottle_discovery_cache_misses_total`, `gothrottle_inflight`,
  `gothrottle_queue_wait_seconds_{bucket,count,sum}`, `gothrottle_queued`,
  `gothrottle_rate_used_60s`, `gothrottle_rejected_total`,
  `gothrottle_request_bytes_total`, `gothrottle_request_duration_seconds_{bucket,count,sum}`,
  `gothrottle_response_bytes_total`, `gothrottle_responses_total`, `gothrottle_rpm_limit`,
  `gothrottle_upstream_errors_total` (22 names).
- **vpn-gateway (:1234):** `bot_errors_total`, `bot_messages_sent_total`,
  `gateway_proxy_errors_total`, `gateway_request_duration_seconds_{bucket,count,sum}`,
  `gateway_requests_queued`, `gateway_requests_total`, full Go runtime default registry
  (`go_gc_*`, `go_goroutines`, `go_info`, `go_memstats_*`, `go_sched_*`, `go_threads`,
  `process_*`), `monitor_failures_total`, `monitor_last_check_timestamp_seconds`,
  `monitor_successes_total`, `vpn_connected_timestamp_seconds`,
  `vpn_reconnect_attempts_total`, `vpn_state`, `vpn_state_changes_total` (55 names).

**Exact name collisions:** exactly **2** — `go_goroutines`, `go_memstats_alloc_bytes`
(both are generic Go-runtime exports; gothrottler hand-renders these two, vpn-gateway
carries the full client_golang default registry). No `gothrottle_*` vs `gateway_*`/
`vpn_*`/`monitor_*`/`bot_*` overlap. **And vpn-gateway is not scraped by Prometheus at all
today** (target list 22:16Z has `gothrottle 192.168.1.200:3010` but no `:1234` target;
`gateway_requests_total` returns 0 series in the live PromQL query). So under the current
single scrape job there is **no live collision**; the risk exists only if a second job
scrapes :1234 (2 colliding runtime names, both harmless baselines) or if someone uses a
*merged* namespace with identical `instance` labels — which the standard
target-per-instance scrape pattern already avoids.

---

## 2. Decision context: what the merge is actually for

The **actual weak spot** is not "two codebases overlap"; it is that the *corporate LLM
egress* (api.ai.gnivc.ru via SSTP) is the one place in the fleet where a tool pointed at
it gets **no queuing, no per-key accounting, no RPM protection, and no dashboard presence**.
goThrottler was built one year earlier for exactly this storm class on the new-api side.
The merge question is: how do we give the corporate lane the same protection without
dragging the two very different lifecycles together (shaper: nobody-user, no caps;
tunnel: NET_ADMIN + /dev/ppp + human 2FA)?

---

## 3. Options

### Option A — Operational merge (zero code): gothrottler instance in stack 61 in front of vpn-gateway:1234

**Cost:** S (compose + env + one republished port). No code. ~1h to deploy.
**Risk:** low deployment risk, but **mid product risk** — the throttler cannot see VPN
state, so R1/R2 (tunnel flap, 2FA wait) cannot be handled by it (it would queue into a
dead tunnel); throughput cut behind it would be *seen* as "the gateway is slow" with no
way to distinguish tunnel-down from load; client re-point changes the failure mode of the
corporate lane for everyone using it.
**Benefit:** immediate fair queue + discovery cache + GeoIP/key-fingerprint logs for
whatever actually points at the lane; zero code; reverts in one compose edit.
**Verdict:** viable only as a **stopgap with a consumer set that justifies it**, and only
with the gateway-key note from R5 accepted (fingerprint degrades to 1 value while the
gateway uses a single shared key).

### Option B — Code merge: extract `Scheduler` + `discoveryCache` + metrics into a shared package; admission behind `GATEWAY_RPM` / `GATEWAY_MAX_CONCURRENCY` env (0 = off), keep binaries separate (RECOMMENDED)

**Cost:** M (scheduler extraction in gothrottler repo; goVpnWork internal/gateway wiring +
env + tests + CI gate; one go.work or module-share decision).
**Risk:** medium — touching the VPN gateway's hot path; mitigated by env-default 0 = off
(byte-identical passthrough when disabled), coverage-ratchet CI (goVpnWork has a
coverage gate at 72.5% overall, per-package floors), and a soak period before enabling.
**Benefit:** the *same* scheduler class as new-api lane becomes available to the corporate
lane with VPN-FSM awareness (R1/R2 can be implemented because admission lives next to
`vpn.State`); single maintenance point for queue semantics; per-call `vpn_state` label in
metrics; key-fingerprint logs stay meaningful; option A remains available as a first step.
**Verdict:** RECOMMENDED (this document, §6).

### Option C — Full monolith (one gateway binary owning queue + tunnel + bot + SOCKS5)

**Cost:** L and permanently coupling two lifecycles.
**Risk:** high — NET_ADMIN + /dev/ppp + 2FA human-in-the-loop would sit in the same
failure domain as a fair queue whose purpose is to *protect* an upstream; a VPN
reconnect would then be able to take down the throttled lane in front of new-api too;
violates both projects' stated simplicity goals (goThrottler README "one global queue,
plain HTTP"; goVpnWork README self-contained single service).
**Benefit:** none that B does not give with less coupling.
**Verdict:** REJECTED.

### Option D — Do nothing (keep both lanes as-is)

**Cost:** 0. **Benefit:** no change risk; the corporate lane is currently *de facto* idle
from this vantage (no verified live consumer). **Risk:** structural — the unthrottled
egress remains one `OPENAI_URL=` edit away from reuse by any agent; no metric visibility;
no per-key accounting; the dead `gateway_requests_queued` gauge quietly lies.
**Verdict:** honestly defensible *today* (see Q1), but it is a **parked risk, not a
decision**; the two lines of work above (QA of the lane + scheduler extraction) are cheap
and make D the informed baseline rather than the accidental one. Not recommended as a
standing answer.

---

## 4. Client re-point migration matrix

| Client (today) | Lane today (verified) | Under A | Under B (env off→on) | Under C | Notes |
|---|---|---|---|---|---|
| hermes-work | :3010 throttled | unchanged | unchanged | unchanged (monolith) | B must NOT change the spluft provider default |
| hermes-spluft / wifu / family | :3010 throttled | unchanged | unchanged | unchanged | ditto |
| goRecomendarr ai-service | :1234 (stack INACTIVE) | *would* cross the throttler → then :1234 | :1234 admission-gated (no re-point needed) | same | when stack 121 is ever restarted, this is the first real consumer; re-verify Q1 first |
| gnivc-side agents / jenkins-xray / laptop tools (UNVERIFIED) | :1234 (presumed) | must re-point to :3011 or keep bypass | no re-point; behavior changes only when GATEWAY_RPM>0 | no re-point | enumerate before adopting A (Q1) |
| confluence/jenkins MCP (host) | SOCKS5 :1080 | unchanged | unchanged | unchanged | L4 lane is out of scope for every option |
| yt-dlp / gorecomendarr metadata / vpn telegram | SOCKS5 :9000 (mihomo) | unchanged | unchanged | unchanged | distinct proxy from :1080; do not confuse (R7 in practice) |
| Prometheus scrape | gothrottle :3010 only | add job `vpn-gateway` :1234/metrics with bearer | same + new `gothrottle_*` series appear (2 runtime names collide, harmless) | merged binary → single job | XC-2 in audit: :1234 not scraped today |

---

## 5. Risk register (R1–R6) — analysis + concrete mitigation

### R1 — stream-slot vs tunnel-flap (slot pinning)

**Analysis (verified against code/config):** goThrottler's concurrency slot is held for
the full SSE stream (README, main.go scheduler). vpn-gateway's SSE paths stream with
`ReadTimeout:0/WriteTimeout:0` and `PROXY_TIMEOUT=60s` toward the upstream (env, live).
An SSTP flap (VPN_AUTO_RECONNECT backoff 30s→5m) with an in-flight stream thus pins a
queue slot for minutes; with `MAX_CONCURRENCY=3` a single flap can pin the whole corporate
queue. Under option A the throttler cannot see `vpn.State` at all.
**Mitigation (B):** admission consults `vpn.Manager.State()` before queuing; when state ≠
connected and admission enabled → **fast-fail 503 `tunnel_down`** (never queue);
slot timeout independent of the request timeout (a `MAX_SLOT_SECONDS` or stream-idle
deadline separate from README's REQUEST_TIMEOUT_SECONDS); when the tunnel is healthy the
existing queue semantics are unchanged. Under A-only: no mitigation built into the
throttler; residual risk accepted and documented.

### R2 — 2FA-wait timeout semantics (hold vs fast-fail 503)

**Analysis (verified):** on `/start` (or reconnect after expiry) the SSTP handshake waits
for **human 2FA confirmation**; `VPN_2FA_WAIT_TIMEOUT` default 5m (env unset → 5m). During
that window the lane accepts TCP but upstream calls fail; queued requests would sit for up
to `MAX_WAIT_SECONDS` (600s in the new-api lane) then die with `429` without ever being
served. A whole agent batch could be held hostage by a missed push notification.
**Mitigation (explicit product decision — Q2):** during `2FA_WAIT`/`disconnected`, admission
returns `503 tunnel_down` immediately (my recommendation — fast-fail; agents already have
retry/backoff, and nothing is lost except wasted queue time); alternative is "hold with a
distinct 503-after-2FA-timeout". Implement as a per-state policy in the admission layer
(next to vpn state), not inside the queue. Nothing in option A can do this.

### R3 — per-key vs global fairness (hermes agents vs humans)

**Analysis (verified):** gothrottler's queue is **one global FIFO** (README limitation,
re-verified in main.go). On the corporate lane the keys are: the single GATEWAY_API_KEY
(today) and, eventually, per-agent tokens (R5). A batch agent flood then starves
interactive humans on the same queue. The new-api lane runs REQUIRE_AUTH=1 with per-client
credentials, so per-key fairness there is possible today but not implemented.
**Mitigation (decision — Q3):** keep the queue global but add **per-key admission
budget** to the scheduler: each key gets its own RPM bucket sampled from the global pool
(class-of-service weights), or a lighter "weighted FIFO" where big batch keys get a lower
share. My recommendation for v1: **global queue + per-key RPM cap** (simple, matches the
existing sliding-window machinery, no new data structures beyond an LRU of key→bucket —
gothrottler already has the LRU pattern in discoveryCache).

### R4 — Prometheus metric-name collision under one scrape namespace

**Analysis (verified, §1.5):** exactly **2 exact name collisions** (`go_goroutines`,
`go_memstats_alloc_bytes`) out of 22 + 55 names; no application-level overlap. **And
:1234 is not scraped today**, so there is no live collision. The *real* operational risk is
someone adding a :1234 scrape job and then writing dashboard/alert expressions on the
runtime names without an `instance` filter.
**Mitigation:** add the vpn-gateway scrape as a **separate job** + a dashboard-variable
`job` selector; document the 2 colliding runtime names as "baseline, always filter by
instance"; if a merged namespace is ever required, rename the vpn-gateway runtime export
(`process_*`, `go_*`) via a `process_`/`runtime` prefix config — but that is not needed
with per-job scraping. Concrete: `scrape_configs: - job_name: vpn-gateway, scheme: http,
bearer_token_file` (prometheus needs the GATEWAY_API_KEY; today there is no target, so
this also fixes invisibility — audit XC-2).

### R5 — shared-key auth topology (fingerprint useless behind one gateway key)

**Analysis (verified):** gateway auth = one `GATEWAY_API_KEY` (env, live); goThrottler
`REQUIRE_AUTH=1` only checks header presence (main.go `hasCredential`) and fingerprints the
whole Authorization value (`keyFingerprint` = first 8 hex of sha256 — `main.go:601-615`).
In front of :1234 **every** client would present the same gateway key →
`keyFingerprint` collapses to one value → per-client logs/accounting die (exactly the
intake's point).
**Mitigation (decision — Q4):** extend the gateway to **N accepted keys**:
`GATEWAY_API_KEYS=key-a:label-a,key-b:label-b` (backward compatible: single
`GATEWAY_API_KEY` still honored), and have the throttler/admission layer key its metrics and
logs on the validated label. This is small, testable, and makes B's per-key fairness (R3)
meaningful. Until then, accept "one fingerprint" and rely on `clientIP` logging (vpn-gateway
already injects real client IP, `server.go:256-264`).

### R6 — tunnel wake-on-demand gap

**Analysis (verified):** auto-reconnect exists (env default true, backoff 30s→5m, retries
unlimited), but there is **no wake-on-first-client-request**: a disconnected tunnel stays
disconnected until the reconnect timer fires; there is no hook from the HTTP proxy path into
`vpn.Manager` to trigger an immediate dial (verified: no such call path in
`internal/gateway` / `internal/vpn`). Agent startup pattern: hermes containers are
long-lived daemons (Up 4h+, no per-run connect), so a *cold* start is rare; the realistic
case is a tunnel that died at 03:00 and a batch agent that starts at 03:01 and waits up to
5m.
**Mitigation:** in B, admission on `disconnected` may **optionally** call
`vpn.Manager.SignalWake()` before fast-failing (503 `tunnel_down`, retry-after ~30s) — the
fast-fail already tells the client to back off, and the wake call turns the first request
into "start connecting now"; keep it env-gated (`VPN_WAKE_ON_REQ=true`, default false) to
avoid surprising SSH-storms against the VPN endpoint. Under A-only this is not implementable.

---

## 6. Recommendation (exactly one)

### **Option B — shared scheduler package (`gothrottle/sched`), wired into goVpnWork's gateway behind env (0 = off), binaries kept separate.**

Rationale:
1. **It is the only option that closes the structural gap** (unthrottled corporate egress)
   without changing the corporate lane's client-facing address — adoption is a config
   knob, not a re-point (matrix §4).
2. **It is the only option that can handle R1/R2** (VPN-FSM-aware admission), because the
   admission code lives beside `vpn.State`; option A explicitly cannot see tunnel state.
3. **It keeps lifecycles separate** (shaper = nobody-user/no caps vs tunnel =
   NET_ADMIN/2FA) — C's coupling penalty is avoided.
4. **It is fully backward compatible and reversible**: default config = current behavior
   (byte-identical passthrough), so the goVpnWork CI gate (build/vet/gofmt/test +
   coverage-ratchet) protects the change while nothing actually changes in production
   until the owner turns the knob.
5. **It upgrades the audit trail**: per-key fingerprints, `vpn_state` label, and the
   currently-dead `gateway_requests_queued` become real, while R4 is a two-line
   scrape-config fix.

Option A is **retained as the first deployment step of the adoption plan** (cheap,
immediately reversible) but is **not by itself the recommendation**, because with no
verified live :1234 consumer (Q1) its marginal value today is near zero and it would lock
in the R1/R2/R5 limitations.

### Step-by-step adoption plan

**Phase 0 — decisions (owner):** Q1 (consumer census), Q2 (2FA-wait policy), Q3 (CoS
level), Q4 (multi-key tokens). See §9.

**Phase 1 — extraction (gothrottler repo, own epic):**
1. Extract `Scheduler` (queue/slots/RPM), `discoveryCache`, and the metrics renderer
   (plain-text Prometheus output) from `main.go` into package `gothrottle/sched`
   (`gothrottle/sched/sched.go`, `cache.go`, `metrics.go`); keep stdlib-only + maxminddb.
2. `main.go` becomes a thin binary: config parse → sched → handler wiring. Public surface
   (`/_throttle/stats`, `/metrics`, `/healthz`) must be **byte-identical** (SPEC: the QA
   suite locks the current output).
3. Proof: existing tests (`main_test.go`, `cache_test.go`, discovery tests) still pass
   + gofmt clean. GOAL: no behavior change, no deploy needed yet.

**Phase 2 — monorepo/import decision:** pick module sharing — either a `go.work` linking
`/opt/git/spluft/gothrottler` into goVpnWork's workspace, or a versioned import of
`github.com/spluft/gothrottler/gothrottle/sched` (with `replace` for local dev). Choose
`go.work` + tagged release of the sched package (Q5).

**Phase 3 — goVpnWork wiring (own epic, gated on Phase 1+2):**
4. `internal/config`: new env `GATEWAY_RPM` (default 0 = off), `GATEWAY_MAX_CONCURRENCY`
   (default 0 = off), `GATEWAY_SLOT_TIMEOUT` (default 0 = off), optional
   `GATEWAY_QUEUE_LIMIT`/`GATEWAY_MAX_WAIT_SECONDS` (reuse sched defaults).
5. `internal/gateway/server.go`: on `/v1/*` (and `/metrics`?), when admission enabled →
   `sched.Admit(ctx, key)` before proxy; when disabled → current path, **untouched**.
   Key = `vpn_gateway:<label>` (R5 label or `keyFingerprint`).
6. VPN-FSM gate (R1): `if schedEnabled && vpn.State()!=connected → 503 tunnel_down`
   (policy per Q2); optional `SignalWake()` (R6) behind `VPN_WAKE_ON_REQ`.
7. Metrics: register `gothrottle_*` names in the gateway's *own* registry (separate
   Prometheus job — R4); add `vpn_state` value label source (already exposed).
8. Tests + coverage-ratchet: unit (admission on/off, state gate, wait semantics) +
   integration via stack-61-like httptest; coverage floors must not drop.

**Phase 4 — deploy & rollback (ops):**
9. Release goVpnWork v0.0.7 image; deploy with all knobs = 0 (off) → verify no behavior
   change (health + a smoke completion through :1234).
10. Enable `GATEWAY_RPM`/`GATEWAY_MAX_CONCURRENCY` gradually (e.g. RPM=15 to mirror
    new-api lane) after a soak week; watch `/metrics` (new `gothrottle_*` + existing
    `gateway_*`); rollback = set env to 0 and redeploy (no data migration, no re-point).
11. **Only then** decide the goRecomendarr ai-service re-point (Q1/Z) and whether to keep
    the new-api lane configuration unchanged.

---

## 7. What each option changes for the current clients (summary)

| Client class | A (republish :3011) | B (env-gated admission) | C (monolith) | D (nothing) |
|---|---|---|---|---|
| hermes* (all 4) | none | none (different lane) | none | none |
| goRecomendarr ai-service | would flow throttled if re-pointed | flow unchanged until RPM>0 | same | unchanged |
| gnivc-side tooling | needs re-point to :3011 | no re-point; behavior change only when enabled | same | unchanged |
| Prometheus | new scrape job | new scrape job | one job | no :1234 visibility |
| Ops failure mode | queue into dead tunnel (R1/R2 unhandled) | 503 tunnel_down, fast-fail | VPN restart can kill local fair queue | current (silent) |

---

## 8. What was NOT verified (vantage limits) — marked, not guessed

- **Q1 consumer census of :1234**: any gnivc-side agents, jenkins-xray, or laptop tools
  using the corporate LLM lane could not be enumerated from this container (corporate
  network not reachable from here; MCP/jenkins access goes via SOCKS). **Requires the
  owner** — this is the single most load-bearing unknown for "do nothing" vs "adopt now".
- goRecomendarr stack 121's ai-service is the only verified :1234 consumer, and only in
  the (inactive) stack file, not live.
- Whether any *current* cron on another host targets :1234 (only this host's jobs.json
  checked).
- Prometheus bearer config for :1234 would need the GATEWAY_API_KEY value (exists in the
  stack; do not echo into docs).

Everything else in this document is timestamped live evidence.

---

## 9. Open product decisions for the owner (Nikolay)

- **Q1 — Consumer census:** what currently uses :1234 (or is expected to)? If the answer
  is "nothing right now", adopt B lazily (Phase 1 extraction only) and keep D until a real
  consumer appears. If "gnivc agents will", adopt B fully now.
- **Q2 — 2FA-wait / reconnect queue semantics:** fast-fail `503 tunnel_down` (recommended)
  vs hold-and-503-after-timeout. Applies to the corporate lane regardless of option.
- **Q3 — Class-of-service:** single global queue (today's gothrottler semantics) vs
  per-key RPM caps / weighted shares on the corporate lane.
- **Q4 — Auth topology:** extend gateway to multiple keys with labels (`GATEWAY_API_KEYS`)
  or accept single-key + clientIP accounting.
- **Q5 — Module sharing:** `go.work` vs tagged module import for `gothrottle/sched`.
- **Q6 — Prod-stack reconciliation** (extends the audit): restore or decommission stacks
  121/127/128/126/161; docs currently say "prod" for services that are not running.

---

## 10. One-line TL;DR

The corporate lane (:1234) is the fleet's one unthrottled, invisible LLM egress — but on
this host nothing verified is pointed at it today, so the fix is **structural**: extract
the throttler scheduler into a shared package (B) and gate it behind env (0 = off), with a
503-on-tunnel-down policy, per-key accounting, and a separate Prometheus job; Option A
remains the reversible first step once a consumer is confirmed; C is rejected; D is a
parked risk, not a decision.