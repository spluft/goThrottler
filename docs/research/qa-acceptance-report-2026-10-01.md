# QA acceptance report — research docs epic t_b7bf6ba0 (2026-10-01)

**QA worker:** qa (Hermes) | **Card:** t_26bf1be2 | **Board:** spluft | **Tenant:** gothrottler
**Docs under test:**
- `docs/research/feature-gap-audit-2026-10-01.md` (doc1, 220 lines)
- `docs/research/gothrottler-govpnwork-merge-2026-10-01.md` (doc2, 439 lines)

Both docs were present in the QA worktree (cut from `main` @ `45b9443`, the architect's
docs commit) — no fetch recovery needed. This report only; the two research docs were
NOT modified.

## Verdict: PASS

All epic acceptance bullets pass. 4 minor findings are reported (non-blocking; §4).

---

## 1. Markdown structure check — PASS

Method: file-based Python checker (no network) over both files; checks: H1 count,
heading-hierarchy jumps (code fences excluded), relative/in-doc link resolution,
TODO/TBD/FIXME/`[ ]` placeholders outside code fences, per-row table column counts
(pipe counts computed outside backticked spans).

| Check | doc1 | doc2 |
|---|---|---|
| Exactly one H1 | PASS (1) | PASS (1) |
| Heading hierarchy (no Hn→Hn+2 jumps) | PASS | PASS |
| Relative file links resolve | PASS (0 file links) | PASS (0 file links) |
| In-doc `#anchor` links resolve | PASS (0 anchor links) | PASS (0 anchor links) |
| No TODO/TBD/FIXME/`[ ]` placeholders | PASS | PASS |
| Tables well-formed (consistent column counts) | PASS (all 13 tables) | PASS (all 5 tables) |

Notes on checker output (no action needed):
- "Empty section" hits — doc1 "Per-repo audit" (line 36); doc2 "1. Live topology"
  (12), "3. Options" (158), "5. Risk register" (225), "6. Recommendation" (317) — are
  container headings whose content lives entirely in immediately-following subsections.
  False positives of the heuristic, not real emptiness.
- Neither doc uses markdown link syntax at all; cross-references are prose
  ("see §11", "see D2 R2"). Prose refs audited manually — see findings F1–F3 in §4.

## 2. Acceptance-bullet coverage — PASS

### D1 (doc1: feature-gap audit)

| Bullet | Verdict | Evidence |
|---|---|---|
| A section for EVERY repo (10 required) | PASS | doc1 §1 goMailClient (L38), §2 goYoutube (L56), §3 goRecomendarr (L79), §4 goVpnWork (L93), §5 kidsEdu (L109), §6 goGitClient (L123), §7 goClipboardSync (L136), §8 gothrottler (L147), §9 tgNtfy (L162), §10 distillatinPublickLLM (L174); plus §11 cross-cutting (L183) |
| Priority P0–P3 per feature | PASS | every feature table has a `Prio` column; all 44 feature rows carry P0/P1/P2/P3 (verified: 0 rows missing) — values used: P0 (GO-1, RE-1, VW-1, CS-1), P1, P2, P3 |
| Effort S/M/L per feature | PASS (note) | `Effort` column complete; 3 rows use ranges "S–M" (GO-4, VW-2) and "M–L" (KE-5) instead of a single bucket — finding F2 |
| One-line "why now" per feature | PASS | `Why now` column populated on all rows |
| Cited to backlog with status, or marked NEW | PASS | cited rows carry an explicit status (e.g. GM-1 L48 "status NOT STARTED", GO-2 L70 "R-10; NOT STARTED", KE-2 L118 "roadmap-2026-08 F-02; NOT STARTED", DI-1 L181 "M1–M4 … NOT STARTED"); all remaining rows begin Basis with "NEW" + reason (e.g. GO-1 L69, RE-1 L87, VW-3 L107, TH-1 L158, XC-1 L187) |
| Top-10 global ranking at the end | PASS | doc1 "Top-10 global ranking" (L194), exactly 10 rows, immediately before the method notes |

### D2 (doc2: merge decision record)

| Bullet | Verdict | Evidence |
|---|---|---|
| Options A/B/C each with cost/risk/benefit | PASS | A (L160–172), B (L174–185), C (L187–196) each have explicit **Cost/Risk/Benefit/Verdict**; bonus Option D "do nothing" (L198–207) |
| Risks R1–R6 each with a mitigation | PASS | §5: R1 (L227, Mitigation L235), R2 (L242, M L249), R3 (L255, M L262), R4 (L269, M L276), R5 (L284, M L292), R6 (L299, M L309) — every risk carries a concrete mitigation |
| Exactly one option explicitly RECOMMENDED | PASS | Option B labeled "(RECOMMENDED)" in its heading (L174), "**Verdict:** RECOMMENDED (this document, §6)" (L185), §6 titled "Recommendation (exactly one)" (L317). C=REJECTED, D="parked risk, not a decision", A=retained only as first deployment step — no competing recommendation |
| Written rationale + step-by-step adoption plan | PASS | §6 rationale points 1–5 (L322–335); adoption plan Phase 0–4, steps 1–11 (L342–383) |
| Open product decisions listed for the owner | PASS | §9 (L416–429): Q1–Q6, addressed to the owner |
| Client re-point migration matrix present | PASS | §4 (L211–221): 7 client classes × lanes today × A × B × C + notes |

### Live-state claims: timestamps + honest marking

| Bullet | Verdict | Evidence |
|---|---|---|
| Every live-state claim carries a verification timestamp | PASS | doc1 header declares the 22:14Z–22:20Z window (L5–8) and every state row/claim repeats an explicit time (e.g. L20 "probe 22:17Z", L25 "22:16Z", L140 "probe 22:18Z"); doc2 §1 title timestamps the re-verification (L12), config tables say "22:15Z" (L37, L60), probes "22:16Z" (L52–55), scrape check "22:16Z" (L137), §8 lists vantage-limited items separately (L399–410) |
| No unverified claims stated as fact | PASS | unverifiable items are explicitly marked **UNVERIFIED** (doc1 L7, doc2 L7–8, L105–108, §8) and routed to owner questions — this satisfies the epic's intent (nothing unverified is presented as verified) |

## 3. Live spot-checks (QA-run, independent of the architect's 22:14–22:20Z window)

Probes run read-only from this container 2026-10-01T22:27:16Z–22:27:43Z (curl-equivalent
GET; raw-IP LAN endpoints per epic scope).

| Claim under test | Doc/source | QA probe (ts) | Result | Match |
|---|---|---|---|---|
| gothrottler :3010 healthy, queue semantics conc=3/RPM=15/queue-limit | doc2 §1.2 (L39–58) | `GET :3010/healthz` 22:27:16Z → 200 `ok` | PASS | live counters `{"conc_limit":3,"rpm_limit":15,"inflight":0,"queued":0}` identical to doc2's 22:16Z shape |
| `admitted_total` was 2065 at 22:16Z | doc2 L52–55 | `GET :3010/_throttle/stats` 22:27:16Z → 200 `admitted_total: 2170`, `rate_used_60s: 12` | PASS | counter monotonic 2065→2170 (+105 in ~11 min) — lane is live and consistent with the documented value |
| REQUIRE_AUTH: `/health` behind throttler → 401 without credential | doc2 L56–58 | `GET :3010/health` 22:27:16Z → 401 `goThrottle: missing credential` | PASS | exact error body matches |
| vpn-gateway :1234 healthy + vpn connected, v0.0.6 git 1c296f4 | doc2 L33–35, doc1 L26 | `GET :1234/health` → 200 `{"data":{"status":"healthy","vpn":"connected"},"success":"true"}`; `GET :1234/version` → 200 `v0.0.6`, git `1c296f4de480c…`, build 2026-09-11 | PASS | byte-equal status payload; full git SHA prefix matches |
| GATEWAY_API_KEY auth ON: `/v1/*` → 401 without key | doc2 L68 | `GET :1234/v1/models` 22:27:16Z → 401 `Unauthorized` | PASS | |
| tgNtfy stack 156 ACTIVE, /api/health 200 ok | doc1 L25 | `GET :8091/api/health` 22:27:16Z → 200 `{"status":"ok"}` | PASS | |
| gomail :8090 / goYoutube :8092 closed (stacks INACTIVE) | doc1 L20–21, L216–217 | TCP connect 22:27:43Z: 8090 refused, 8092 refused | PASS | (control: 3010/1234 OPEN same run) |
| goClipboardSync relay 37918 closed (no live deploy) | doc1 L140 | TCP connect 22:27:43Z: 37918 refused | PASS | |

Spot-check requirement (≥1 live claim per doc) is exceeded: 5 claims on doc2 + 3 on doc1,
all consistent.

## 4. Findings (report-only; nothing fixed)

| # | Severity | Finding |
|---|---|---|
| F1 | minor | doc2 L220 (migration matrix, SOCKS5 :9000 row) says "do not confuse (**R7** in practice)" — the risk register defines only R1–R6; there is no R7. Dangling reference. |
| F2 | minor | doc1 effort ranges "S–M" (GO-4 L72, VW-2 L106) and "M–L" (KE-5 L121) deviate from the strict S/M/L scale declared at doc1 L12 and required by the epic bullet. Judgment call: they convey uncertainty honestly; flagged for the epic owner, not a failure. |
| F3 | minor | doc1 L32 (re-verification table, :1234 row) says "see §2 D2" — doc1 has no §2; the intended target is deliverable doc2 (§1.4). Dangling prose cross-reference (not a markdown link, so the link checker could not catch it). |
| F4 | info | doc2 L382 "decide the goRecomendarr ai-service re-point (**Q1/Z**)" — "Z" is undefined anywhere in the document; presumed stray token (Q1 exists, "Z" does not). |
| F5 | info | Pre-existing, out of epic scope: neither research doc uses any markdown links (`[…](…)`), so anchor/link checking is vacuously green; future edits adding links get no regression protection from link syntax. No action. |

## 5. Reproduce

- Structure checker: pure-Python (stdlib only, no network) script `mdcheck.py`
  (kept in QA scratch: `/opt/data/profiles/qa/cache/scratch/mdcheck.py`); run
  `python3 mdcheck.py` from anywhere — it reads both docs by absolute path.
- Spot-checks: `spotcheck.py` / `portcheck.py` in the same scratch dir (read-only GET +
  TCP connect to 192.168.1.200; raw-IP curl through the shell is blocked by the local
  security scanner, hence Python).

## 6. Card acceptance criteria (t_26bf1be2)

- [x] Both docs pass the markdown structure check; all checker hits and 4 minor findings listed in §1/§4.
- [x] Every epic acceptance bullet mapped to pass/fail with file/line evidence (§2) + independent live spot-checks (§3).
- [x] This report committed at `docs/research/qa-acceptance-report-2026-10-01.md` on the QA worktree branch; tree clean.
