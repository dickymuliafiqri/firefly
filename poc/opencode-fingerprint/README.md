# PoC — OpenCode free-tier fingerprint refresh (v1.18.31 → v2.0.29)

Standalone module. Nothing under `internal/` imports it and it imports nothing
from `internal/`. It exists to be reviewed and tested before anything is ported
into the adapter.

```
poc/opencode-fingerprint/
├── go.mod
├── fingerprint.go        # the v2 fingerprint, transcribed from upstream sources
├── fingerprint_test.go   # assertions that pin the v2 shape
├── system.txt            # v2 official system prompt, verbatim
├── models.addendum.json  # the configs/models.json entry to add
└── cmd/pocdryrun/        # prints (and can fire) the assembled request
```

## Run it

```bash
cd poc/opencode-fingerprint
go test ./...                                    # pin the v2 shape
go run ./cmd/pocdryrun                           # print the request, send nothing
go run ./cmd/pocdryrun -delta                    # print the v1 → v2 delta
go run ./cmd/pocdryrun -live                     # actually POST it
go run ./cmd/pocdryrun -live -model muse-spark-1.3
```

`-live` exits 1 on a 403 and prints the gate's message, so it doubles as the
acceptance check for the port.

## What the 403 actually is

The zen routes in the v2 repo are 191-byte stubs:

```ts
// packages/console/app/src/routes/zen/v1/chat/completions.ts
export function POST(input: APIEvent) { return proxyInference(input.request) }
```

`packages/console/app/src/lib/inference-proxy.ts` forwards them to
`Resource.ConsoleMigration.inferenceUrl` with the comment:

> `// New inference owns authentication, model availability, free-tier access, and billing.`

Keyless requests reach the gate anonymously — `Bearer public` is stripped
before forwarding:

```ts
const anonymous = !key || key === "public";
if (anonymous) for (const name of ["authorization","x-api-key","x-goog-api-key"]) forwarded.headers.delete(name)
```

So the gate has no credential to judge. It judges the **client fingerprint**,
and it lives in a closed service — its rules can change without any public
commit. That is why the 403 comes back every release: Firefly pins the
v1.18.31 fingerprint and each release advances at least one validated
attribute.

Upstream says so out loud, in
`packages/core/src/plugin/provider/opencode.ts`:

> `// Every request here goes to the Console, which reads the User-Agent to tell which OpenCode a member runs and whether it evaluates the policies it is being sent.`

## The delta

| Attribute | v1 (pinned today) | v2 (v2.0.29) |
| :-- | :-- | :-- |
| `User-Agent` | `opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14` | `opencode/latest/2.0.29/opencode` |
| `x-opencode-client` | `desktop` | `opencode` |
| `x-opencode-session-id` | absent | `<session.id>` |
| `x-session-affinity` | absent | `<affinity>` |
| `X-Session-Id` | absent | `<affinity>` |
| `x-opencode-request` | `<msg_ id>` | absent |
| system prompt | "You are opencode, an interactive CLI tool…" + "You are powered by the model named big-pickle" | "You are an AI agent running in OpenCode, a coding agent harness…" |
| verification tools | `bash` + `read` | `shell` + `read` |
| session/request id format | `ses_`/`msg_` + descending timestamp id | unchanged |
| `x-opencode-project` | `global` | unchanged |

The header block is transcribed verbatim from
`packages/core/src/session/model-request.ts`:

```ts
"x-opencode-session-id": session.id,
...(session.parentID ? { "x-opencode-parent-session-id": session.parentID } : {}),
"x-session-affinity": affinity,
"X-Session-Id": affinity,
...(session.parentID ? { "x-parent-session-id": session.parentID } : {}),
"User-Agent": App.useragent(app),
"x-opencode-project": session.projectID,
"x-opencode-session": affinity,
"x-opencode-client": app.name,
```

`affinity = session.parentID ?? session.fork?.sessionID ?? session.id`
(`packages/core/src/session/affinity.ts`), so a root session sends the same
value in all three session-ish headers. `packages/core/test/session-title.test.ts:220`
and `session-runner.test.ts:4727` assert exactly seven headers for a root
session — that count is what `RootHeaderCount` pins.

The UA format comes from `packages/core/src/app.ts`:

```ts
export function useragent(app: Info) { return `opencode/${app.channel}/${app.version}/${app.name}` }
```

`latest` is the stable channel string (`packages/script/src/index.ts`:
`IS_PREVIEW = CHANNEL !== "latest"`), and `2.0.29` is what
`https://opencode.ai/update/api/latest/cli/opencode` currently reports.

## What the port touches

| File | Change |
| :-- | :-- |
| `internal/transport/upstream/health_opencode.go:15` | `OpenCodeUserAgent` → `opencode/latest/2.0.29/opencode`; make the version a single constant so the next bump is one edit |
| `internal/transport/upstream/health_opencode.go` | `SetOpenCodeHeaders`: `x-opencode-client` → `opencode`; add `x-opencode-session-id`, `x-session-affinity`, `X-Session-Id` |
| `internal/adapter/opencode/free_prompt.txt` | replace with the v2 prompt; drop the `big-pickle` tail |
| `internal/adapter/opencode/free_template.go` | `OfficialFreePromptFor` loses its `big-pickle` substitution; `EnforceFreeSessionPayload` injects `shell` instead of `bash` |
| `internal/adapter/opencode/free_tools.json` | replace with the v2 `shell` + `read` schemas |
| `configs/models.json` | add the entry from `models.addendum.json` |

`GenerateOpenCodeTimestampID` needs no change —
`packages/schema/src/identifier.ts` is byte-identical to it, including the
12-bit per-millisecond counter that commit `ad68205` added to fix this same
403 the first time.

## Two things the PoC deliberately does not decide

**`union-alpha` and `union-alpha-free` are gone from the live catalog.**
`https://models.opencode.ai/api.json` lists 123 models under the `opencode`
provider and neither id is among them, yet `configs/models.json` registers two
`oc` routes pointing at `union-alpha`. Those routes are dead upstream. Removing
them is a separate call — the PoC leaves them alone.

**`deepseek-v4.1-flash` is one character away from an existing route.** The
`dahl` upstream already registers `DeepSeek-V4-Flash` →
`deepseek-ai/DeepSeek-V4-Flash-0731`. `config.Build` rejects duplicate
`public_name`s, and these two differ, so there is no collision — but they are
confusingly similar on the dashboard. Rename the new one if that bothers you.

## Known unknowns

- The exact JSON schema for `shell` and `read` is reconstructed from the
  Effect `Schema.Struct` definitions in
  `packages/core/src/tool/plugin/{shell,read}.ts`. Field names, types and
  descriptions are verbatim; the wire shape is inferred. Nothing in the
  evidence shows the gate validates tool *schemas* — only that v2 renamed
  `bash` → `shell` — but this is the one place the PoC is reconstructing
  rather than transcribing.
- `anthropic-version: 2023-06-01` is set unconditionally by
  `adapter.go attempt()`. It is an Anthropic-API header and only matters on
  the `/messages` route, so the PoC leaves it out of the v2 set. Out of scope.
- `-live` goes out over the direct connection. Firefly routes the `oc`
  upstream through WARP (`egress_mode: warp`), so a live run proves the
  fingerprint, not the egress path.
