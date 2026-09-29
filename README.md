# Firefly

[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![CI Status](https://github.com/dickymuliafiqri/firefly/actions/workflows/ci.yml/badge.svg)](https://github.com/dickymuliafiqri/firefly/actions)
[![Docker](https://github.com/dickymuliafiqri/firefly/actions/workflows/docker.yml/badge.svg)](https://github.com/dickymuliafiqri/firefly/actions/workflows/docker.yml)
[![Concurrency](https://img.shields.io/badge/Concurrency-1%2C000%2B%20SSE%20Streams-emerald)](./docs/loadtest.md)

Firefly is a fast, multi-tenant AI gateway and reverse proxy compatible with the OpenAI API. It connects your applications to multiple AI providers (OpenAI, Claude, Grok, and more) with automatic failover, smart load balancing, key rotation, token optimization, and a built-in web dashboard.

Written in pure Go, Firefly handles thousands of simultaneous streaming connections with low memory usage and zero downtime.

---

## Why Firefly?

- **Zero Downtime**: If an AI provider or API key fails, Firefly automatically switches to the next available one without interrupting your users.
- **Cost & Token Savings**: The built-in Token Saver cleans repetitive logs, strips terminal codes, and trims prompts to save up to 60–90% on API costs.
- **System Prompt Guard**: Automatically attaches your custom instructions (like blocking unwanted promotional ads from API account sellers) to every chat request.
- **Smart Load Balancing**: Combine multiple models across different providers into a single "Virtual Combo" to distribute traffic smoothly.
- **Client Keys & Monetization**: Create and sell your own API keys with token budgets, rate limits, and expiration dates.
- **Built-in Web Dashboard**: Manage models, upstreams, API keys, and monitor real-time traffic from a clean web interface - no external tools required. The **Visualizer** page animates every request as a routed line on a zoomable canvas with a locked three-column topology - the Firefly root (it states the ingress call), one node per active upstream (it states the model that upstream served, or why it was skipped) and a fixed terminal column (Thinking, Tool, Writing, Usage, Error) - so untouched rows stay dashed and dimmed until a request lights them, every connector is permanent (the root and *every* upstream row fan out to the whole phase column) and only the state changes: the path the request actually took is bright and flowing, a traversed phase keeps a settled trail, the rest stay dim. Credentials and the Token Saver hop are deliberately not nodes here (a request spends one key out of a ring that may hold a thousand, and the rewrite is only visible when it changed the body); the rewrite is reported in the legend instead. The canvas opens auto-fitted to the viewport (drag to pan, wheel to zoom, double-click to snap back). A second **Town** view renders the same fleet as a pixel-art office powered by `agent-town`: one crew member per upstream lounges while idle and hurries to a desk when a stream arrives, with a persisted camera (zoom + follow) in the corner.
- **Connect Page**: A single screen (**Services → Connect**) that tells any AI agent how to reach Firefly - the **base URL** (defaults to your dashboard's own origin, editable for tunnel or reverse-proxy setups) with copy buttons, a **tenant key dropdown** with a masked key and its own copy button, the model id, and copy-ready configuration for **Cline**, **Roo Code**, **Kilo Code**, **Cursor**, **Continue**, **OpenCode**, **Aider**, the **OpenAI Python/Node SDKs** and plain `curl`. A **Test connection** button verifies the key against `GET /v1/models`, and **Copy everything** hands a teammate the whole bundle. All presets use the OpenAI wire surface (`/v1/chat/completions`, `/v1/embeddings`, ...) - Firefly exposes no inbound Anthropic (`/v1/messages`) or Responses (`/v1/responses`) route, so point agents at their "OpenAI Compatible" provider.
- **Single Binary, Easy Setup**: Runs as a single lightweight file with an embedded web UI and auto-generated configs.

---

## How It Works

```
  [ Your App / AI Client ]
             │
             ▼  (HTTP / OpenAI SDK)
 ┌────────────────────────────────────────────────────────┐
 │                      FIREFLY                           │
 │                                                        │
 │  1. Verify Client API Key & Check Rate Limits          │
 │  2. Pick Best Model, Upstream Provider & Key           │
 │  3. Apply Token Saver & System Prompt Guard            │
 │  4. Stream Response Back to Client in Real Time        │
 └───────────────────────────┬────────────────────────────┘
                             │
            ┌────────────────┼────────────────┐
            ▼                ▼                ▼
     [ OpenAI / vLLM ]   [ Anthropic ]   [ xAI Grok / Others ]
```

1. **Your Client sends an AI request** (using standard OpenAI SDKs or HTTP) to Firefly.
2. **Firefly checks the request**: Verifies the client API key, checks token balances and rate limits, and selects the best provider and key.
3. **Optimizes the prompt**: Applies Token Saver and System Prompt Guard rules (if enabled).
4. **Forwards to the AI provider**: Streams the answer back to your user line-by-line in real time.
5. **Handles failures automatically**:
   - If a key hits rate limits (`429`), Firefly instantly switches to another key.
   - If a provider server goes down (`5xx`), Firefly reroutes to a backup provider.

---

## Supported Providers & Protocols

Firefly supports direct API keys and native OAuth login for:

- **OpenAI** (and compatible engines: Azure OpenAI, vLLM, Ollama)
- **Anthropic Claude** (automatically translates between OpenAI and Claude message formats)
- **xAI Grok** (supports Grok CLI with native OAuth device login)
- **Google Antigravity / Cloud Code** (Gemini and Claude models via OAuth)
- **Cline** (native OAuth)
- **CodeBuddy** (China & International)
- **OpenCode & OpenCode Go**

> **Server deployments:** the consent link's `redirect_uri` is always pinned to `localhost` (the only redirect family these OAuth clients register), so the link stays valid no matter which host serves the dashboard. When the provider's login then redirects to a callback URL the gateway never receives (e.g. `http://localhost:<port>/api/oauth/callback?...`), copy that URL from the browser address bar and paste it into the OAuth dialog's **Verify** field — the gateway validates the state and exchanges the authorization code manually.

> **Google Cloud Code (Antigravity):** Cloud Code serves no model-list route, so **Fetch models** returns Firefly's built-in catalog for that provider (the ids below, plus anything already routed to it) instantly and without spending a token - use the **"Add a model id manually"** field in **Upstreams → Edit → Models** for any id that is not in it. The per-model **Check** (and **Check all health**) is the real verification: an authenticated one-token generation against Google whose verdict mirrors the host. Both need a connected Google account. Model ids follow the Cloud Code convention - the version sits in the **middle** and the tier is a **suffix**: `gemini-3.8-flash`, `gemini-3.1-pro-high` (a bare `gemini-3.1-pro` is rejected), `claude-sonnet-4-6-thinking`, `gpt-oss-120b-medium`. Map them in **Models** exactly as they appear.

---

## Quick Start

### Option 1: One-Line Production Install (Linux)

Install and run Firefly as an automatic background service:

```bash
curl -fsSL https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.sh | bash
```

Once installed, Firefly starts automatically:
- **Web Dashboard**: `http://<SERVER_IP>:8080` (Default password: `12345678`)
- **OpenAI API Endpoint**: `http://<SERVER_IP>:8080/v1/chat/completions`
- **Config Directory**: `/etc/firefly/`

Manage the service:
```bash
sudo systemctl status firefly    # Check status
sudo systemctl restart firefly   # Restart gateway
sudo journalctl -u firefly -f    # View live logs
```

### Option 2: Build from Source

**Prerequisites**: Go 1.22 or higher. (Optional: Bun 1.0+ or Node.js 18+, only if you want to rebuild the web dashboard.)

```bash
# 1. Build the single binary (includes the web dashboard)
make build

# 2. Run Firefly
./firefly -addr=0.0.0.0:8080 -admin-token="your-secret-token"
```

---

## Key Features

### 1. Virtual Combos (Smart Load Balancing)

Group multiple models from different providers into one virtual model name. For example, create a combo called `smart-router` that balances traffic between `gpt-4o`, `claude-3-5-sonnet`, and `deepseek-chat`:

```json
{
  "combos": [
    {
      "name": "smart-router",
      "models": ["gpt-4o", "claude-3-5-sonnet", "deepseek-chat"],
      "strategy": "least_inflight",
      "enabled": true
    }
  ]
}
```

- **Least In-Flight**: Routes to whichever model is currently processing the fewest active requests.
- **Round Robin**: Cycles evenly through candidate models one by one.
- **Failover**: Uses the primary model first, and switches to the next only if the primary fails.

### 2. Token Saver Suite (Reduce API Costs)

Firefly includes built-in tools to shrink token usage without losing answer quality:

- **RTK (Tool Output Cleaner)**: Removes terminal colors (ANSI), deduplicates repeating log lines, and trims huge command outputs before sending them to the AI.
- **Caveman (Concise Output)**: Tells the model to skip filler phrases, greetings, and conversational fluff, cutting output tokens by up to 65%.
- **Ponytail (Minimal Code)**: Encourages the model to write clean, direct code using standard libraries rather than over-engineered solutions.
- **Headroom (Context Pruning)**: Automatically trims older middle messages in long conversations when approaching context limits.
- **System Prompt Guard**: Adds your own custom rules to every chat request (for example: *"Do not send any promotional messages to the user"*). Great for blocking ads or group invites injected by third-party account sellers.
- **Direct Compression Endpoint (`POST /v1/compress`)**: Standalone API to clean and compress text or tool outputs on demand.

All Token Saver features can be toggled on or off individually from the **Settings** page.

### 3. Client API Keys & Monetization

Create and distribute your own gateway API keys (`sk-gw-...`) to customers or team members:

- **Token Quotas**: Set a maximum token balance (e.g. 5,000,000 tokens) or leave unlimited (`0`).
- **Provider Quota (upstream side)**: the **Quota** page also shows what each connected OAuth account has left, straight from the provider — per model and per rolling window (weekly / 5h). These reads are read-only: refreshing them never spends the quota it reports. A model the provider reports at 0% is skipped by routing until its reset time instead of being forwarded into a guaranteed failure, and a generation 429/409 invalidates the snapshot so the next read reflects reality. A provider that does not expose a quota endpoint is omitted rather than shown as zero.
- **Expiration Dates**: Set validity periods (e.g. 7 days, 30 days). Expired keys are rejected automatically.
- **Rate Limits**: Control requests-per-second (RPS) and concurrent connections per client.
- **Check Balance Endpoint (`GET /v1/usage`)**:
  Clients can check their remaining tokens and expiration anytime:
  ```bash
  curl http://localhost:8080/v1/usage \
    -H "Authorization: Bearer sk-gw-YOUR_KEY"
  ```
- **Automated Top-Ups (`POST /api/tenants/topup`)**:
  Add tokens or extend expiry dates via API — easily connected to payment webhooks like Stripe, Midtrans, or Lemon Squeezy:
  ```bash
  curl -X POST http://localhost:8080/api/tenants/topup \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer <ADMIN_TOKEN>" \
    -d '{
      "api_key": "sk-gw-YOUR_KEY",
      "add_tokens": 5000000,
      "extend_days": 30
    }'
  ```

### 4. Smart Resilience & Failover

Firefly protects your service from both account-level issues and provider outages:

- **Key Rotation**: If an upstream key hits a rate limit (`429`) or runs out of credits (`401`), Firefly temporarily pauses that key and instantly retries the request using another available key in the pool.
- **Circuit Breaker**: If an entire provider host is failing (`5xx` errors or network drops), Firefly stops sending traffic there and reroutes requests to a backup provider until the primary recovers.
- **Background Health Checks**: Periodically verifies upstream connections in the background so faulty endpoints are detected before your users hit them.

### 5. Remote Access & Cloudflare Integrations

- **Cloudflare Tunnel**: Make your gateway accessible from the internet without port forwarding or a static IP. Supports instant quick tunnels (`trycloudflare.com`) or production named tunnels directly from the dashboard.
- **Cloudflare WARP Egress**: Route outbound calls to AI providers through Cloudflare WARP with automatic IP rotation to avoid IP-based rate limiting.
- **Free HTTPS (Auto-TLS)**: Enable free Let's Encrypt certificates from **Settings → Native Let's Encrypt Auto-TLS**. You only need a real domain pointing to your server and TCP ports `80` and `443` open and unused. Firefly then serves your gateway over HTTPS automatically.

---

## Configuration & CLI Options

Configuration files are stored in `configs/` as simple JSON files:
- `upstreams.json`: Provider endpoints, protocols, and API keys.
- `models.json`: Public model routes and upstream mappings.
- `combos.json`: Virtual load-balancing combos.
- `tenants.json`: Client API keys, quotas, and limits.
- `tokensaver.json`: Token optimization and System Prompt Guard settings.

All settings can be updated live from the web dashboard with **zero server restarts** (hot-swapped immediately).

### Common Command Flags

| Flag | Environment Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `-addr` | `FIREFLY_ADDR` | `0.0.0.0:8080` | Address for incoming AI traffic and web dashboard |
| `-admin-addr` | `FIREFLY_ADMIN_ADDR` | `""` | Address for admin plane (`/metrics`, `/api/*`); disabled if empty |
| `-config-dir` | `FIREFLY_CONFIG_DIR` | `configs` | Directory for JSON configuration files |
| `-admin-token` | `FIREFLY_ADMIN_TOKEN` | `""` | Secret token protecting administrative endpoints |
| — | `FIREFLY_SESSION_SECRET` | auto | HMAC key for stateless dashboard sessions; set it on serverless/multi-instance hosts so sessions survive restarts |
| `-log-level` | `FIREFLY_LOG_LEVEL` | `info` | Log detail level (`debug`, `info`, `warn`, `error`) |
| `-health-check-interval` | `FIREFLY_HEALTH_CHECK_INTERVAL` | `15s` | Background health probe interval (`0` to disable) |
| `-quota-poll-interval` | — | `2m` | Background provider-quota refresh for OAuth connections (`0` keeps quota reads on demand only) |
| `-tunnel` | `FIREFLY_TUNNEL` | `disabled` | Cloudflare Tunnel mode (`disabled`, `quick`, `named`) |
| `-tunnel-token` | `FIREFLY_TUNNEL_TOKEN` | `""` | Secret token for named Cloudflare Tunnels |
| `-tunnel-bin-dir` | `FIREFLY_TUNNEL_BIN_DIR` | auto | Custom folder to find or download the `cloudflared` binary |
| `-tunnel-url` | `FIREFLY_TUNNEL_URL` | auto | Local service address to expose through the tunnel |
| `-warp-rotate-interval` | `FIREFLY_WARP_ROTATE_INTERVAL` | `5m` | Interval for automatic WARP IP rotation (`0` to disable) |
| `-shutdown-grace-seconds` | `FIREFLY_SHUTDOWN_GRACE_SECONDS` | `30` | Seconds to let active streams finish before shutting down |
| `-version` | — | — | Print version and build information, then exit |

---

## Testing & Performance Validation

```bash
# Run all unit and integration tests
go test -count=1 -race ./...

# Run the benchmark load test tool (100 simultaneous requests)
make build-loadtest
./bin/loadtest -mock -c 100 -profile low

# Or run the full automated 6-scenario benchmark suite (100 & 1,000 requests)
./bin/loadtest -mock -suite
```

---

## Documentation & References

- [Production Deployment Guide](./docs/production.md)
- [Load Testing & Benchmark Guide](./docs/loadtest.md)
- [Backend Architecture & AI Agent Manual](./AGENTS.md)
- [Frontend Design System](./DESIGN.md)
- [Release Changelog](./CHANGELOG.md)
- [Contribution Guidelines](./CONTRIBUTING.md)
- [License (MIT)](./LICENSE)
