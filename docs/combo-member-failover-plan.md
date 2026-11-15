# Combo Member Failover — Execution Plan

Status: approved, not implemented.
Target release: `v1.52.0`.

## 1. Objective

A virtual combo must fail over to its next member when the member it selected
fails at request time, without exhausting the members of the combo that already
had its turn and without ever corrupting a response that already reached the
client.

Concretely, for

```json
{"name":"a","strategy":"round_robin","models":["a1","a2","a3"]},
{"name":"b","strategy":"round_robin","models":["b1","b2"]},
{"name":"c","strategy":"failover","models":["a","b"]}
```

one client request performs at most one network attempt per top-level member of
`c`: `a1` (or whichever single member `a`'s round-robin cursor picked), then
`b1`. `a2` and `a3` are never tried, because the failure is attributed to `a` as
a whole.

## 2. Decision record

| # | Decision | Rationale |
|---|---|---|
| D1 | Failover is attributed to the **top-level member** of the requested combo, not to the concrete candidate that failed | Rejected the alternative ("exclude only the failed leaf") because the operator explicitly does not want a combo's whole member list consumed by one request. Attribution at the top level is the only rule that makes "one shot per combo" true at every nesting depth. |
| D2 | A failed attempt excludes `Target.ComboMember`; the requested combo is then re-resolved from the top | Re-entering the failed nested combo is pointless by construction (its top-level entry is excluded), so the exclusion set never needs to reach nested levels. |
| D3 | Attempt budget is `min(len(c.Models), 3)`; a direct (non-combo) model always has budget 1 | Bounds one client request to at most 3 top-level members (each of which may itself make up to 3 upstream calls through the adapter's key rotation). A 12-member combo must not become a 36-call amplifier. |
| D4 | The existing free resolve-time skip inside a nested combo is kept | A member whose breaker is open, whose upstream is disabled, whose account is quota-vetoed, or whose keyring is exhausted is skipped **without any network call**. It costs nothing and rescues requests that hit a cooling member. Only network attempts are not repeated. |
| D5 | A retry requires that **no byte reached the client** | Non-negotiable: fallback after a commit corrupts the response. Enforced by the attempt writer, not by the adapter. |
| D6 | The failed attempt's status is read from the **attempt writer**, because the adapter returns `nil` after relaying an upstream error body | `internal/adapter/openai/adapter.go:229-238` writes the 5xx body to `w` and returns `nil`. A gate keyed only on `err != nil` would never fire for the most common case. This also catches synthetic adapter 502s (e.g. `internal/adapter/antigravity/adapter.go:415`) with no per-adapter changes. |
| D7 | Per-attempt TTFB budget of 20 s, enforced by a watchdog that stops once the attempt commits | A `context.WithTimeout` on the attempt would abort a healthy long stream at 20 s. The watchdog cancels only while the attempt is still waiting for its first byte. |
| D8 | The error surfaced to the client when every member fails is the **last** attempt's error, with the whole chain in the log | Freshest information; the chain preserves the original cause. |

## 3. Error matrix (final)

Failover **is** attempted:

| Class | Detail |
|---|---|
| Transport | dial failure, TCP reset, DNS, TLS — status 0 with a non-cancel error |
| TTFB timeout | `ResponseHeaderTimeout` (30 s) or the 20 s per-attempt watchdog |
| 5xx | `500`, `502`, `503`, `504`, `529` |
| Synthetic 5xx | adapter-produced error body (antigravity unparseable 2xx) |
| `ErrCircuitOpen` | member was never dialed |
| `401`, `402`, `403` | credential dead / credit exhausted / region blocked on that provider; the other members are different providers |
| `404` | provider does not serve that model |
| `429` | only when `Retry-After` is absent or longer than the remaining request budget |

Failover is **not** attempted:

| Class | Why |
|---|---|
| `context.Canceled` / 499 | client is gone; another member cannot help |
| any status after a commit | response already partially delivered |
| `400`, `409`, `413`, `422` | the request itself is invalid; every member rejects it identically |
| all keys of the same member exhausted | the adapter already rotated every key (3 attempts) before returning |

## 4. Steps

Each step is independently committable. `go build ./...` must pass at every step
boundary.

### Step 1 — domain: carry the member attribution

`internal/domain/domain.go`

- Add to `Target` (struct at `domain.go:931`):
  ```go
  // ComboMember is the direct member of the requested combo that produced this
  // target (a model or a nested combo name). Empty for a direct model request.
  ComboMember string
  ```
- Add next to `MaxComboDepth` (`domain.go:695`):
  ```go
  // MaxComboFailoverAttempts bounds how many top-level combo members one client
  // request may contact. Separate from MaxComboDepth: depth is a structural
  // limit, this is a cost limit.
  const MaxComboFailoverAttempts = 3
  ```

### Step 2 — domain: exclusion-aware resolution

`internal/domain/snapshot.go`

- Extend `resolveState` (`snapshot.go:367`):
  ```go
  exclude   map[string]bool // top-level member names already attempted
  topMember string          // outermost combo member that produced the target
  ```
- `resolveComboMembers(st, combo, depth, path)`:
  - skip `mName` when `depth == 0 && st.exclude[mName]`
  - on success at `depth == 0`: `st.topMember = mName`
- `resolveLeaf`: set `tgt.ComboMember = st.topMember`
- New entry point, with the existing one delegating to it:
  ```go
  func (s *CatalogSnapshot) ResolveTargetExcluding(
      tenant *Tenant, publicName string, exclude map[string]bool,
      canUseUpstream func(string) bool,
      canUseCandidate func(TargetCandidate) bool,
  ) (*Target, bool, error)

  // unchanged behaviour, kept for callers and tests:
  func (s *CatalogSnapshot) ResolveTargetWithCandidates(
      tenant *Tenant, publicName string,
      canUseUpstream func(string) bool,
      canUseCandidate func(TargetCandidate) bool,
  ) (*Target, bool, error)   // = ResolveTargetExcluding(..., nil, ...)
  ```
- `ResolveError` kind when every top-level member is excluded: reuse
  `ResolveModelNotFound` for a direct model, and add
  `ResolveUpstreamUnavailable` for an exhausted combo (already used for a
  breaker-open direct model).

### Step 3 — domain tests

`internal/domain/snapshot_test.go`

- `TestResolveTargetExcluding_SkipsExcludedTopMember`
- `TestResolveTargetExcluding_DoesNotConsumeNestedMembers` — `a=[a1,a2]`,
  `exclude["a"]`, assert the result is a member of `b` and that `a2` is never
  chosen across 20 iterations (round-robin cursor advances each call).
- `TestResolveTargetExcluding_DirectModelIgnoresExclude`
- `TestResolveTarget_ComboMemberIsTopLevelName` — direct combo member = its own
  name; nested combo member = the outermost name, not the inner leaf.
- `TestResolveTargetExcluding_AllMembersExcluded` — returns
  `ResolveUpstreamUnavailable`.
- `TestResolveTargetExcluding_DepthTwoOnlyTopLevelHonorsExclude` — `c=[a,b]`,
  `a=[inner,other]`, `inner=[x,y]`: excluding `a` jumps to `b`; excluding `x`
  does nothing special at the top level.

### Step 4 — server: attempt writer

New file `internal/server/combo_writer.go`.

```go
// comboAttemptWriter buffers one combo attempt so a failed attempt can be
// discarded without corrupting the client response. It becomes a pure
// pass-through the moment the attempt commits (first byte of a < 400 response),
// which is what keeps SSE streaming immediacy intact.
type comboAttemptWriter struct {
    dst http.ResponseWriter
    mu  sync.Mutex
    hdr http.Header
    status   int
    buf      bytes.Buffer
    permitted bool
}

func newComboAttemptWriter(dst http.ResponseWriter) *comboAttemptWriter
func (w *comboAttemptWriter) Header() http.Header
func (w *comboAttemptWriter) WriteHeader(status int)
func (w *comboAttemptWriter) Write(p []byte) (int, error)
func (w *comboAttemptWriter) Flush()
func (w *comboAttemptWriter) Committed() bool   // satisfies httpx/adapter detection
func (w *comboAttemptWriter) Status() int       // 200 default once a body was written
func (w *comboAttemptWriter) Permit()           // adopt; copy shadow headers, replay buffer
func (w *comboAttemptWriter) Discard()          // drop buffer, headers and status
```

Rules:

1. `WriteHeader(s)` records `status`; while unpermitted nothing reaches `dst`.
2. `Write`/`Flush` with `status < 400` → `Permit()` first, then write/flush.
   This is the commit point; SSE keeps flowing immediately.
3. `WriteHeader(s)` with `s >= 400` stays buffered so the body can be discarded.
4. `comboMaxErrorBuffer = 64 << 10`: an unpermitted error body stops growing at
   the cap (excess is discarded) so a provider's HTML error page cannot be
   buffered unbounded.
5. `Permit`/`Discard` are idempotent and mutually exclusive; `Discard` after
   `Permit` is a no-op.
6. `Permit` copies `hdr` into `dst.Header()` before `dst.WriteHeader(status)`.

### Step 5 — server: writer tests

`internal/server/combo_writer_test.go`

- buffered 500 body never reaches `dst`, `Discard` leaves `dst` untouched
- `WriteHeader(500)` then `Write` keeps the body buffered
- `WriteHeader(200)` + `Write` permits and passes the payload through
- `Flush` on a 200 permits and flushes
- shadow headers are not leaked to `dst` after `Discard`, and are copied on `Permit`
- error body capped at `comboMaxErrorBuffer`
- `Committed()` false before a 200 write, true after
- `Permit` after `Discard` is rejected/no-op (documented behaviour)

### Step 6 — server: attempt loop

`internal/server/handlers.go`

- Extract the current body of `forwardEndpoint` from the credential acquire
  (`handlers.go:560`) through `adapter.Forward` (`handlers.go:715`) and the
  post-call accounting into a request-scoped runner:

  ```go
  type forwardRunner struct {
      // request-scoped state only: r, snap, tenant, model, origBody, stream,
      // upstreamPath, capture, requestID, deps, tokensIn (recomputed)
  }
  func (fr *forwardRunner) run(w *comboAttemptWriter, target *domain.Target, attempt, fallbackUsed int) error
  ```

  Moved **into** `run` (they are per-attempt, not per-request):
  `AcquireKeySlot`/`AcquireKeySlotLenient` and its `defer releaseCred()`,
  `IncKeyInflight`/`DecKeyInflight`, the token-saver pass, `tokensIn`
  recomputation, `capture.Stage(trace.StageAttempt, ...)`, the in-flight
  `LiveLog`, usage recording and `capture.SetTarget`.
  Left in the request scope: tenant inflight ticket, `releaseTenant`, the
  request buffer, `capture.Finish`, the final duration/total accounting.

- Body handling: capture `origBody` once before the loop and restore
  `body = append(body[:0], origBody...)` at the top of `run`, because
  `target.SystemPrompt` differs per member and `tokensaver.InjectSystemPrompt`
  must be re-applied for the member actually used.

- Loop:

  ```go
  excluded := make(map[string]bool)
  budget := comboAttemptBudget(snap, model)
  var lastErr error
  var lastStatus int

  for attempt := 1; attempt <= budget; attempt++ {
      if r.Context().Err() != nil { return }
      if deps.isDraining() || deadlineSoon(r.Context(), 2*time.Second) { break }

      target, fallbackUsed, err := snap.ResolveTargetExcluding(
          tenant, model, excluded, canUseUpstream, canUseCandidate)
      if err != nil {
          if attempt == 1 { writeResolveError(w, err) } else { writeUpstreamFailure(w, lastErr) }
          return
      }

      aw := newComboAttemptWriter(w)
      err = fr.run(aw, target, attempt, fallbackUsed)

      status := aw.Status()
      if err == nil && status < 500 {
          aw.Permit()
          return
      }
      if aw.Committed() || !retryableAcrossMembers(err, status, aw, r.Context()) {
          aw.Permit()
          writeUpstreamFailure(w, errOrStatusError(err, status))
          return
      }

      aw.Discard()
      lastErr, lastStatus = err, status
      recordFallbackHop(fr, target, attempt, status, err)

      if target.ComboMember == "" { break } // direct model: nothing to exclude
      excluded[target.ComboMember] = true
  }
  writeUpstreamFailure(w, errOrStatusError(lastErr, lastStatus))
  ```

- `comboAttemptBudget`:
  ```go
  func comboAttemptBudget(snap *domain.CatalogSnapshot, model string) int {
      combo, ok := snap.Combo(model)
      if !ok { return 1 }
      if n := len(combo.Models); n >= 1 {
          return min(n, domain.MaxComboFailoverAttempts)
      }
      return 1
  }
  ```

- `retryableAcrossMembers(err error, status int, aw *comboAttemptWriter, ctx context.Context) bool`
  implements §3 verbatim:
  - `context.Canceled` (in `err` or in `*openai.ErrUpstream.Cause`) → false
  - `upstream.ErrCircuitOpen` → true
  - `status == 0` and `err != nil` → true (transport)
  - `429` → parse `aw.Header().Get("Retry-After")`; true only if absent or
    `retryAfter > remaining(ctx)`
  - `401`, `402`, `403`, `404` → true
  - `status >= 500` → true
  - `400`, `409`, `413`, `422` and every other 4xx → false
  - unknown error with no status → true, logged at warn (it is almost certainly a
    transport or body-parse fault, and the budget bounds the damage)

- TTFB watchdog (D7), inside `run` right before `adapter.Forward`:
  ```go
  attemptCtx, cancelAttempt := context.WithCancel(r.Context())
  defer cancelAttempt()
  t := time.AfterFunc(comboAttemptTTFB, func() {
      if !aw.Committed() { cancelAttempt() }
  })
  defer t.Stop()
  ```
  The commit check and `Permit` share the writer mutex, so either the timer sees
  a committed attempt and does nothing, or it cancels an attempt that has not
  written a byte.

- Graceful shutdown: the loop must not start a new attempt once draining has
  begun; the existing drain guard answers 503 at admission, this closes the
  in-flight window.

### Step 7 — server tests

`internal/server/combo_failover_test.go`

- `TestComboFailover_5xxMovesToNextMember` — `c=[a,b]`, `a` → 500 with JSON body,
  `b` → 200. Assert the client sees `b`'s body, `a` was hit exactly once, and the
  log chain is `a→b`.
- `TestComboFailover_DoesNotConsumeNestedMembers` — `a=[a1,a2,a3]`; assert the
  handler hits exactly one member of `a` and then `b`.
- `TestComboFailover_400IsNotRetried` — `a` → 400; assert one upstream call only,
  client sees `a`'s 400 body.
- `TestComboFailover_CommittedStreamIsNotRetried` — `a` emits `200` + one SSE
  event, then resets; assert no call to `b` and the client got the partial stream.
- `TestComboFailover_ClientCancelIsNotRetried` — cancel the request context
  mid-attempt; assert no second upstream call.
- `TestComboFailover_401/404/429AreRetried` — table test.
- `TestComboFailover_429LongRetryAfterIsRetried`, `_429ShortRetryAfterRelays`.
- `TestComboFailover_DirectModelUnaffected` — non-combo model still resolves and
  forwards exactly once.
- `TestComboFailover_AllMembersFailReportsLast` — both members 500; client sees
  the last member's error and status.
- `TestComboFailover_TTFBWatchdogFires` — member hangs past the watchdog; assert
  the second member is used.
- `TestComboFailover_PromptGuardReappliedPerMember` — members with different
  `system_prompt`; assert each attempt carries its own directive.
- `TestComboFailover_KeySlotReleasedBetweenAttempts` — upstream with
  `MaxConcurrent: 1`; assert no deadlock and that the second attempt acquires.

### Step 8 — observability

- `internal/observability/trace/trace.go:25-40` — add `StageFallback = "fallback"`
  after `StageAttempt`, documented as "a combo member attempt was discarded and
  the next member was tried"; emitted with detail `"<from>→<to> (HTTP <status>)"`.
- `internal/storage/analytics/types.go:4` — add to `RequestLog`:
  ```go
  Attempt      int    `json:"attempt,omitempty"`
  FallbackFrom string `json:"fallbackFrom,omitempty"`
  ```
  `internal/server/livelogs.go:12` is an alias, so it inherits the fields.
- `internal/server/handlers.go` — emit one `LiveLog` per *failed* attempt
  (`Status` = the upstream status that was discarded, `Error` = the cause,
  `Attempt`, and `FallbackFrom` on the successful record), plus the existing
  final record. Token/cost accounting stays on the successful attempt only, so a
  discarded attempt never inflates usage.
- `frontend/` — Console log table: render `attempt` and `fallbackFrom` (a small
  `a→b` badge) and make the discarded attempt visually dimmed. Follow the
  existing table component in the Console page; no new design tokens.

### Step 9 — documentation

- `AGENTS.md`
  - §1 new invariant **12**: "A Combo Member Failover Must Never Outlive Its
    Commit Point" — the attempt writer, why the adapter returns `nil` after
    relaying an error body (D6), why the per-attempt timeout is a TTFB watchdog
    and not a context deadline (D7), and why the exclusion is attributed to the
    top-level member (D1).
  - §2 `server` row: mention `combo_writer.go` and the attempt loop.
  - §2 `domain` row: `Target.ComboMember`, `ResolveTargetExcluding`,
    `MaxComboFailoverAttempts`.
  - §3 flow diagram: add the `attempt N of M` note around step 6/7.
- `README.md` — new subsection "Combo member failover" under Virtual Combos,
  with the `a`/`b`/`c` example from §1 and the error matrix condensed.
- `llms.txt` — update the `combos.json` line to mention member failover.
- `CHANGELOG.md` — `## [1.52.0]` entry covering semantics, error classes, budget,
  observability, and the D6 pitfall.

### Step 10 — verification and release

```bash
go build ./...
go test -count=1 ./internal/domain/... ./internal/config/... ./internal/server/... ./internal/observability/...
go test -count=1 ./...
cd frontend && npx tsc --noEmit && npx vite build
```

Known-acceptable failures on this Windows host (pre-existing, unrelated):
`internal/config` `TestTunnelRoundTrip` and `TestTunnelQuickModeIsNotSecret`
(chmod 666 vs 600) and `internal/supervisor` path-assertion tests. `-race` is
unavailable here (`gcc` missing) — say so explicitly in the PR body instead of
claiming race coverage.

`gofmt` must be checked on LF-normalized copies; the working tree is CRLF and
`gofmt -l` flags whole files otherwise.

Release flow, same as `v1.51.0`:

1. commit steps 1–9 on `dev`
2. `gh pr create` dev → main, merge
3. annotated tag `v1.52.0` on the feature commit, push to trigger
   `.github/workflows/release.yml`
4. verify the release assets and `checksums.txt`

## 5. Risks

| Risk | Mitigation |
|---|---|
| Duplicate provider billing for an attempt that generated tokens but died before its first byte reached the client | budget 3; documented in the CHANGELOG |
| Latency stacking: three members each hanging to TTFB timeout | 20 s per-attempt watchdog (D7) plus `deadlineSoon(ctx, 2s)` before each attempt |
| Writer buffering an unbounded error body | `comboMaxErrorBuffer` cap (64 KiB) |
| Stream broken by the watchdog racing a commit | timer and `Permit` share the writer mutex |
| Credential ticket leak across attempts | `releaseCred` moved inside `run`; explicit test with `MaxConcurrent: 1` |
| Behaviour change for direct model requests | budget is 1 and `ComboMember` is empty, so the loop degenerates to today's single pass; covered by a test |
| Breaker semantics change | none: `ProcessAttemptOutcome` still runs per attempt, so member failover only shortens the window before the breaker learns |
