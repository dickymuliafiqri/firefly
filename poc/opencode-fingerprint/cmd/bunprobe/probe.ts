// Decisive experiment: replicate the real OpenCode client transport.
// Bun's fetch is backed by BoringSSL + Bun's own HTTP/2 stack — the same
// stack packages/core uses at runtime. If the gate is transport-level
// (JA3/TLS fingerprint), this succeeds where Go and curl fail.

const BASE = "https://opencode.ai/zen/v1";
const UA = "opencode/latest/2.0.29/opencode";

// v2.0.29 canonical root-session header block, verbatim from
// packages/core/src/session/model-request.ts
function v2Headers(sessionID: string): Record<string, string> {
  return {
    "x-opencode-session-id": sessionID,
    "x-session-affinity": sessionID,
    "X-Session-Id": sessionID,
    "User-Agent": UA,
    "x-opencode-project": "global",
    "x-opencode-session": sessionID,
    "x-opencode-client": "opencode",
  };
}

const SESSION = "ses_0123456789abcdef0123456789ab";

async function probe(label: string, url: string, init: RequestInit) {
  try {
    const res = await fetch(url, init);
    const text = await res.text();
    console.log(`\n--- ${label} ---`);
    console.log(`HTTP ${res.status}  via ${res.headers.get("x-opencode-log-id") ?? "?"}`);
    console.log(text.slice(0, 400));
    return res.status;
  } catch (e) {
    console.log(`\n--- ${label} ---`);
    console.log("THREW:", String(e).slice(0, 300));
    return -1;
  }
}

const body = JSON.stringify({
  model: "mimo-v2.6-flash-free",
  stream: true,
  max_tokens: 16,
  messages: [{ role: "user", content: "hi" }],
});

console.log("=== BUN TRANSPORT TEST ===");
console.log("bun version:", Bun.version);

await probe("A. bun fetch + 7 v2 headers + Bearer public", `${BASE}/v1/chat/completions`.replace("/v1/v1", "/v1"), {
  method: "POST",
  headers: { ...v2Headers(SESSION), "Content-Type": "application/json", Authorization: "Bearer public" },
  body,
});

await probe("B. bun fetch + 7 v2 headers, NO auth", `${BASE}/chat/completions`, {
  method: "POST",
  headers: { ...v2Headers(SESSION), "Content-Type": "application/json" },
  body,
});

await probe("C. bun fetch + v1 headers (what Firefly sends today)", `${BASE}/chat/completions`, {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    Authorization: "Bearer public",
    "User-Agent": "opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
    "x-opencode-client": "desktop",
    "x-opencode-project": "global",
    "x-opencode-request": "msg_0123456789abcdef0123456789ab",
    "x-opencode-session": SESSION,
  },
  body,
});

await probe("D. bun GET /models (keyless catalog)", `${BASE}/models`, { method: "GET" });
