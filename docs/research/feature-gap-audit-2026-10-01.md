# spluft feature-gap audit — 2026-10-01

**Project stream:** spluft (pet projects) | **Epic:** t_b7bf6ba0 | **Author:** architect (Hermes)
**Date:** 2026-10-01 (UTC); all live-state claims carry their verification timestamp.
**Method:** every claim below was re-verified against (a) the repo tree at `/opt/git/spluft/*`
and (b) live Portainer state (env 3 "local", https://192.168.1.200:9443) + plain HTTP probes
(no DNS) on 2026-10-01T22:14Z–22:20Z. Claims that could NOT be verified are marked **UNVERIFIED**;
nothing from the PM intake picture is repeated without a check.

**Priority scale:** P0 = data loss / active outage / security exposure; P1 = observability &
security hardening; P2 = process & tech debt; P3 = product/extension.
**Effort:** S ≤ 1d, M ≤ 5d, L > 5d (working days, single engineer).

---

## Re-verification summary (read first)

| Intake claim (2026-10-01) | Re-verified state | Verdict |
|---|---|---|
| goMailClient live on stack 127 | Stack 127 `gomail-client` **INACTIVE**; port 8090 closed (probe 22:17Z) | STALE — not deployed now |
| goYoutube live on stack 128 :8092 | Stack 128 `goyoutube-client` **INACTIVE**; port 8092 closed (probe 22:17Z) | STALE — not deployed now |
| goRecomendarr stack 121 inactive | Stack 121 **INACTIVE** (Portainer 22:15Z); ports 8086/9005/8084 closed | CONFIRMED |
| kidsEdu stack 126 inactive | Stack 126 **INACTIVE** (Portainer 22:15Z) | CONFIRMED |
| gogitclient stack 161 inactive | Stack 161 **INACTIVE** (Portainer 22:15Z) | CONFIRMED |
| tgNtfy live on stack 156 :8091 | Stack 156 **ACTIVE**; /api/health → 200 `{"status":"ok"}` (22:16Z) | CONFIRMED |
| goVpnWork v0.0.6 stack 61 :1234/:1080 | Stack 61 **ACTIVE**; /health → vpn connected, /version v0.0.6 git 1c296f4 (22:16Z) | CONFIRMED |
| gothrottler stack 130 :3010 | Stack 130 **ACTIVE**; gothrottler container healthy, image `gothrottler:e89c203` (22:15Z) | CONFIRMED |
| Loki unhealthy | Container `loki` **running but UNHEALTHY** (22:15Z); logs show normal table ops; :3100 not published | CONFIRMED (health gate red) |
| gomail has NO tgNtfy integration | `docs/integration-tgntfy.md` present + `internal/tgntfy/` tap (digest events, epic t_d81c434d) | **WRONG** — integration exists |
| goYoutube R-05..R-08 partially delivered, finish R-08 | All four **implemented in code**: /metrics (`goytdlp_*`), `internal/logctx`, auth rate-limit (`internal/httpapi/ratelimit.go`), `auth_events` table + handlers | **WRONG** — R-05..R-08 done (epic t_70e118e0) |
| kidsEdu wishlist F-01..F-07 top-3 | **F-01 nudges implemented** (epic t_e67a0bf6, per README 2026-09); F-02..F-07 remain | **PARTIAL** — F-01 closed |
| hermes agents + cron + jenkins hit :1234 unthrottled | **All 4 hermes agents on .200 use `http://192.168.1.200:3010/v1`** (provider `spluft`); zero `:1234` refs in their configs; cron jobs.json has no `:1234`; jenkins/confluence MCP use SOCKS5 :1080 | **NOT VERIFIED for .200 fleet** — see §2 D2 |

---

## Per-repo audit

### 1. goMailClient (gomail) — `goMailClient/`

Current state (verified): repo main at `cccd0e8`; PWA IMAP client with unified inbox, bleve
search (E3 + E5.7 shipped), WS/SSE realtime, offline Dexie cache, tgNtfy integration with
per-mailbox unread digest (epics t_f202c09c, t_d81c434d) and account lifecycle events.
Recent architect work: Exchange autodetection + EWS transport SPEC (t_6d4b3b19, SPEC merged
2026-10-01). **Stack 127 NOT running.**

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| GM-1 | Auto light/dark theme (backlog B/P2) | P3 | S | UI polish; design system already ships theme tokens (`frontend/src/components/design/`); hooks exist per roadmap E5.2 | existing backlog item C-P2 row "Темы: Auto (light/dark) — P2 лёгкая"; status NOT STARTED |
| GM-2 | Keyboard shortcuts (desktop) | P3 | S | Cheap desktop-UX win; roadmap C-backlog P2 | existing backlog item "Keyboard shortcuts (десктоп) — P2 лёгкая"; NOT STARTED |
| GM-3 | Inline reply in thread | P2 | M | Top-requested productivity flow (HEY/Gmail pattern); draft/thread-id plumbing exists per backlog A2 | existing backlog C-P2 "Inline reply в thread"; NOT STARTED |
| GM-4 | Swipe actions archive/flag (mobile) | P1 | S | UX-wow at low cost; SwipeableCard component already shipped (E5.2/E5.4); only wiring remains | existing backlog C-P1 "Swipe-действия archive/flag"; separate card pending |
| GM-5 | Attachments "download all as zip" | P3 | M | Natural "forward this batch to the kids" flow; Fastmail pattern | existing backlog P3 item; NOT STARTED |
| GM-6 | New-mail **push** notification path | P2 | M | tgNtfy digest exists (per-mailbox unread), but a bursty digest ≠ instant new-mail ping; decide digest-only vs per-mail event | NEW (deliberate product decision; do not build without PO ruling; digest epic t_d81c434d is current behavior) |
| GM-7 | Login rate-limiting (E6.3 residual) | P1 | M | QA-log closed E6.3 with explicit LOW/P3 finding: no rate-limit on login; brute-force on a PWA reachable on LAN is realistic; goYoutube already built the identical limiter (t_70e118e0) — port the pattern | NEW (QA-log E6.3 residual; no backlog id) |

### 2. goYoutube — `goYoutube/`

Current state (verified): R-05 (/metrics on METRICS_ADDR, `goytdlp_*` + `goyt_*` names), R-06
(`internal/logctx` request/job correlation), R-07 (auth token-bucket: login 5/15min per
IP+username, register 3/h per IP, refresh 60/min per IP — implemented per
`internal/httpapi/server.go:120-124`), R-08 (`auth_events` table + best-effort records) — all
IMPLEMENTED (epic t_70e118e0; RECOMMENDATIONS.md triage banner 2026-09-13 is stale vs code).
Recent: playlist delete with files + reaper (t_c0e3a8f0), download-path integrity F1–F6
(t_ca950533). **Stack 128 NOT running** — the "prod" narrative in docs is not backed by a live
deploy right now.

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| GO-1 | Deploy check / prod restoration | P0 | S | Stack 128 INACTIVE (22:15Z): docs claim prod active but nothing listens on :8092 — either redeploy or declare the product paused; half-alive prod is the worst state (same class as goRecomendarr) | NEW (ops finding; no backlog id exists) |
| GO-2 | R-10 down-migrations | P2 | M | 28+ forward-only migrations (up to 028); rollback of a bad release is manual SQL today | existing RECOMMENDATIONS R-10; NOT STARTED; effort includes retro-writing down for 001..018 |
| GO-3 | R-13 on-demand upload_date probe | P2 | M | Flat-playlist cards show no date; Jellyfin Season YYYY grouping suffers; epic t_a9c385c6 exists (name-probe rework) | existing RECOMMENDATIONS R-13 (epic t_a9c385c6) — status: SPEC-level/rework; verify card state before starting |
| GO-4 | R-14 Jellyfin-safe filename sanitization | P2 | S–M | `:`, `/`, quotes break Windows shares/SMB; cheap win | existing RECOMMENDATIONS R-14; NOT STARTED |
| GO-5 | R-11 defect-class index | P2 | S | Navigation into the epics/history tree; 0.5d doc work | existing RECOMMENDATIONS R-11; NOT STARTED |
| GO-6 | R-12 frontend test infra (vitest) | P2 | L | ~7K lines TS without a test infra; every PWA regression is manual | existing RECOMMENDATIONS R-12; marked in-progress via t_2fcdcb92 backlog — verify before re-proposing |
| GO-7 | R-15 Extractor abstraction | P3 | L | kills the yt-dlp-only ceiling; Vimeo/Rutube readiness | existing RECOMMENDATIONS R-15; NOT STARTED (new epic required) |
| GO-8 | R-16 pluggable notifications | P3 | M | ntfy/discord/webhook beyond tgNtfy | existing RECOMMENDATIONS R-16; NOT STARTED |
| GO-9 | R-18 API versioning | P3 | M | `/api/v1/*` migration + OpenAPI | existing RECOMMENDATIONS R-18; NOT STARTED |

### 3. goRecomendarr — `goRecomendarr/`

Current state (verified): 8 microservices, NATS JetStream + PG + Qdrant + Redis; master
consolidated (`c88a72d`); active work: metadata/ai pipeline error fixes (t_899f949f,
feature branch present). **Stack 121 INACTIVE** (22:15Z) — ai-service/an take no requests.

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| RE-1 | Reconcile stack 121: restart+verify or decommission | P0 | S | INACTIVE for an extended period (containers absent from live list 22:15Z); docs/DEPLOYMENT assume prod; either prove health or record decommissioning in docs so nobody "fixes" a ghost | NEW (ops finding) |
| RE-2 | Per-user AI token budget + monthly summary via tgNtfy | P2 | M | Recs generation costs real AI tokens (ai-service); 2–5 household users with no per-user quota is an unbounded cost center; tgNtfy is already wired (`docs/integration-tgntfy.md`) | NEW (intake idea; tgNtfy adapter exists) |
| RE-3 | Live multi-provider search in web UI | P2 | L | Catalogue pulse is nice; live search across Sonarr/Radarr is the actual ARR value; Prowlarr connector exists in ingest config but web surface is absent | NEW (Prowlarr enabled=false in stack config, and no web search surface verified) |
| RE-4 | ARR catalogue pulse (status/queue view) | P3 | M | Read-path convenience; Sonarr TTL snapshot cache already SPECed (t_04426f61) — verify its merge state before touching | NEW (epic t_04426f61 SPEC exists — check card state) |
| RE-5 | Ingest webhook auth (security) | P1 | M | README "Security" states webhook relies on Docker network isolation, no JWT — on a flat L2 LAN (infra model) any host can POST fake events | NEW (README-known limitation, no backlog id) |

### 4. goVpnWork — `goVpnWork/`

Current state (verified): v0.0.6 (git 1c296f4, image sha256:2218a4…, build 2026-09-11), stack 61
ACTIVE, container healthy; :1234 OpenAI proxy + :1080 SOCKS5 with allowlist; GATEWAY_API_KEY
set (auth ON); TELEGRAM bot + monitor; TGNTFY_URL :8091 wired. **No rate limiting/admission
anywhere in the gateway** (verified: zero `Limiter`/`rate.`/queue code in `internal/`; the
`gateway_requests_queued` metric exists in the registry but is never set by gateway code —
dead metric). The full merge study (options A/B/C/D, risk register R1–R6, migration matrix)
is the separate deliverable `gothrottler-govpnwork-merge-2026-10-01.md`.

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| VW-1 | 2FA-wait product decision + queue semantics | P0 | S | `VPN_2FA_WAIT_TIMEOUT` = 5m default; during that window + reconnect backoff the lane is half-dead with no explicit client-visible signal; decide fast-fail `503 tunnel_down` vs hold | NEW (decision; see D2 R2) |
| VW-2 | SOCKS5 allowlist audit + per-user routing | P2 | S–M | Allowlist is a comma list in compose; confluence/jenkins/hermes MCP all flow through :1080; no accounting of who uses what | NEW (see D2 migration matrix; ops finding) |
| VW-3 | Gateway request accounting / admission | P1 | M | The metrics gauge exists (`gateway_requests_queued`) but nothing sets it; either implement admission (D2 option B GATEWAY_RPM) or **remove the misleading metric** | NEW (dead-metric finding) |

### 5. kidsEdu — `kidsEdu/`

Current state (verified): backend (Kotlin/Spring) + admin PWA + Android built; **F-01 nudges
IMPLEMENTED** (epic t_e67a0bf6, README 2026-09: 60s scheduler soft reminders); stack 126
INACTIVE (22:15Z). README explicitly: "авто-e2e нет" for the admin PWA.

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| KE-1 | Auto-e2e for kid/admin PWA | P1 | L | README states no automated e2e; every UI change is manual regression; exactly the gap goYoutube R-12 targets | NEW (README-stated gap, no backlog id) |
| KE-2 | F-02 gamification / streaks | P1 | M | Duolingo-proven retention; F-01 discipline base now exists (streak over nudge) | existing roadmap-2026-08 F-02; NOT STARTED |
| KE-3 | F-03 parent dashboard | P1 | M | Parents ask "show progress"; existing admin PWA ProgressPage to package | existing roadmap-2026-08 F-03; NOT STARTED |
| KE-4 | F-06 TTS expansion (questions + listen-repeat) | P1 | M | 3–6-year-olds are audio-first; pronounce (V6) exists to extend | existing roadmap-2026-08 F-06; NOT STARTED (partial exists) |
| KE-5 | F-04 offline mode | P2 | M–L | "doesn't work offline" is a recurring ed-tech complaint; PWA MockApiClient proves the offline UI path | existing roadmap-2026-08 F-04; NOT STARTED |

### 6. goGitClient — `goGitClient/`

Current state (verified): v1 per epic t_7f28f77f; one stateless binary, no DB, worktree-aware
virtual/physical checkout, SSE ops, credential masking; **deliberately NO auth** (README D8:
trusted-network tool "anyone on the network can run git's mutating ops"). Stack 161 INACTIVE
(22:15Z). TGNTFY adapter exists as no-op unless both URL+token set.

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| GG-1 | Auth / read-only mode for LAN exposure | P1 | M | Currently any LAN host can push with force to any registered repo — on a flat L2 network this is a foot-gun; even a minimal access token (like gothrottler REQUIRE_AUTH presence check + fingerprint logs) would raise the bar; documented as out-of-scope for v1 | NEW (README D8 explicit scope decision — this proposes a v1.1 follow-up) |
| GG-2 | Create/remove worktrees from UI | P2 | M | Awareness exists (worktree badges + lock refusal); creation is still CLI-only; natural next step | NEW (README feature surface; no backlog id) |
| GG-3 | Multi-user with per-user git identity | P3 | L | "who pushed" attribution; requires auth story first (GG-1) | NEW |

### 7. goClipboardSync — `goClipboardSync/`

Current state (verified): monorepo hub(Rust)/app(Kotlin)/relay(Go); MVP skeletons per SPEC
t_6e107ec6 (hub stack is Rust — REVISION-README); wire protocol newline-JSON across
LAN/WG/relay. **No live deploy found** (relay port 37918 closed on .200, probe 22:18Z).

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| CS-1 | First real echo-loop + file transfer Lenovo↔Xiaomi | P0 | M | The whole point of the project; echo_loop.sh harness exists (`hub/tests/echo_loop.sh`); static code without a passing pair-test is unproven | NEW (SPEC/MVP milestone; no backlog id) |
| CS-2 | Relay health + metrics | P2 | S | Relay is the only channel when LAN/WG split; currently opaque; tgNtfy adapter pattern is established elsewhere in the stream | NEW |

### 8. gothrottler — `gothrottler/`

Current state (verified): repo = 2 commits (initial `98e59d7` + discovery cache `e89c203`);
deployed image `gothrottler:e89c203`, healthy, REQUIRE_AUTH=1, conc=3/RPM=15/queue=50/
wait≤600s, GEO_DB mmdb mounted, /metrics + /_throttle/stats exposed; discovery cache default
paths `/v1/models,/version,/api/tags,/v1/props` TTL 300s keyed per-credential. R4 evidence
collected for the merge study (metric names vs vpn-gateway: only `go_goroutines` +
`go_memstats_alloc_bytes` collide).

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| TH-1 | Per-key / class-of-service fairness | P2 | M | README states "one global queue — no per-route or per-key fairness"; once the corporate lane is throttled (D2), hermes agents vs humans share one FIFO; CoS is the pre-merge decision | NEW (README limitation; feeds D2 R3) |
| TH-2 | VPN-FSM-aware admission (fast-fail 503 tunnel_down) | P1 | M | Pre-requisite for throttling behind goVpnWork: SSE slot pinning during tunnel flap; `vpn.State` is already exported by goVpnWork (internal/vpn/state.go) | NEW (feeds D2 R1) |
| TH-3 | Documented TLS termination pattern | P3 | S | README says plain HTTP, terminate TLS in front; one compose snippet would remove the foot-gun | NEW |

### 9. tgNtfy — `tgNtfy/`

Current state (verified): v1.1 (lazy topics, optional catalog) shipped; stack 156 ACTIVE;
`GET /api/health` → 200 ok (22:16Z); image ghcr.io/spluft/tgntfy:7a4ca9d. Coalesce ctx bug
fix verified in code (`internal/coalesce/coalesce.go` timer flushes with
`context.Background()` — documented as the fix for the canceled-request-ctx delivery drop).

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| TG-1 | Delivery-health Grafana panel (/status + /undelivered + metrics) | P2 | S | gomail/goyoutube/gorecomendarr/govpn all push events; a "dead gate" currently surfaces only as user silence; Prometheus metrics already exported | NEW (cross-cutting; see §11) |
| TG-2 | Unlink surface at the gate (per-user) | P2 | S | gomail documented that unlink is admin-CLI only; self-serve unlink would close the loop | NEW (gomail E5 documented gap) |

### 10. distillatinPublickLLM — `distillatinPublickLLM/`

Current state (verified): **design phase only** — no code (`README` = design phase; docs/
architecture = M1–M4 roadmap, provider captures, wire format). No live deploy.

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| DI-1 | M1–M4 implementation epic (session+login → provider round-trip → OpenAI API+SSE → hardening) | P1 | L | The docs are complete and the roadmap is cut; the project is currently zero-deliverable and the architecture risks rotting like earlier planning-only docs | existing roadmap M1–M4 (gated, shippable); NOT STARTED |

### 11. Cross-cutting — infra/monitoring

| ID | Feature | Prio | Effort | Why now | Basis |
|---|---|---|---|---|---|
| XC-1 | Loki readiness fix | P1 | M | Container **unhealthy** (22:15Z) while promtail ships; log pipeline half-broken while every repo invests in structured logs (goYoutube R-06, gothrottler GeoIP logs); inspect healthcheck (`wget :3100/ready` — likely the healthcheck binary/endpoint semantics, logs show normal table ops) | NEW (ops finding; same as intake but re-verified) |
| XC-2 | Scrape vpn-gateway /metrics into Prometheus | P2 | S | Verified: Prometheus has **no target** for :1234/metrics (auth-gated); R4 data shows only 2 name collisions if added; the corporate lane is currently invisible to the dashboard | NEW (feeds D2 R4) |
| XC-3 | Service delivery-health panel on tgnfyt /status + /undelivered | P2 | S | One panel answering "did my notifications deliver" across gomail/goyoutube/gorecomendarr/govpn | NEW (cross-service; merges TG-1) |
| XC-4 | Prod-stack inventory drift scan | P2 | S | gomail/goyoutube/gorecomendarr/kidsedu/gogitclient docs say "prod", live state says INACTIVE; a weekly status sweep (Portainer API → doc) prevents the class of stale "prod" claims this audit found | NEW |

---

## Top-10 global ranking (highest value across the whole stream)

| # | Feature | Repo | Why now (one line) |
|---|---|---|---|
| 1 | **D2 merge: throttle the corporate lane (option A stopgap → B code merge)** | gothrottler×goVpnWork | The single unthrottled LLM egress in the fleet; every agent/tool that ever points at :1234 gets a fair queue + per-key logs + metrics (see decision record) |
| 2 | **RE-1 / GO-1: restore or decommission inactive prod stacks (121/127/128/126/161)** | goRecomendarr, goYoutube, gomail, kidsEdu, goGitClient | Half-alive prod is the most dangerous state; docs "prod" vs reality mismatch misleads every future epic |
| 3 | **VW-1: 2FA-wait / reconnect queue semantics decision** | goVpnWork | Blocks the merge (D2 R2); a 5-minute silent lane with no client-visible signal is a correctness bug |
| 4 | **GM-7: login rate limiting** | goMailClient | Known open security finding (E6.3); reference implementation already exists in goYoutube (t_70e118e0) |
| 5 | **XC-1: Loki readiness fix** | infra | The fleet's log pipeline is red while every service invests in structured logs |
| 6 | **GG-1: minimal auth/read-only mode** | goGitClient | Any LAN host can force-push to any registered repo today |
| 7 | **GM-4: swipe archive/flag** | goMailClient | UX-wow at S-effort; component shipped, wiring only |
| 8 | **KE-1: kid/admin PWA auto-e2e** | kidsEdu | Only no-e2e PWA in the stream; manual regression for every UI change |
| 9 | **GO-2: R-10 down-migrations** | goYoutube | 28 forward-only migrations make rollback dangerous |
| 10 | **DI-1: distillatin M1–M4 implementation** | distillatinPublickLLM | Fully designed, zero delivered; the longest unproductive gap in the stream |

---

## Method & evidence notes

- Live probes executed 2026-10-01T22:14Z–22:20Z via plain HTTP to 192.168.1.200
  (`/healthz`, `/_throttle/stats`, `/metrics`, `/health`, `/version`, `/api/health`,
  `/api/status`) and Portainer API (read-only: stacks, containers, stack files, inspect).
- Port numbers checked at 22:18Z: 3010/3005/1234/1080/8091/9092/3006/9093 OPEN;
  8090/8092/8086/9005/8084/8100 closed.
- "INACTIVE" = Portainer stack Status=inactive AND no corresponding container in
  `list_containers` AND (where applicable) LAN port refused.
- Anything the intake claimed that this audit could not confirm is marked and, where it
  matters, carried into the D2 decision record or an open question for the owner.