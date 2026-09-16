# Firefly Production Deployment & Hardening Guide

Operational and production deployment manual for **Firefly** (OpenAI-wire-compatible Reverse Proxy & AI Gateway) targeted at workloads handling 1,000+ simultaneous streaming connections.

---

## 1. Operating System Recommendations & File Descriptors (`ulimit`)

In reverse proxy scenarios with 1,000+ active streaming users, the server opens two TCP file descriptors (FD) per active connection (one inbound socket from client to Firefly, and one outbound socket to the upstream provider), in addition to configuration files, file watch descriptors, and internal pipes. Default Linux and macOS limits (typically 1,024) will quickly produce `too many open files` errors and connection drops.

### A. Configuring File Descriptor Limits (`ulimit`)
Set a minimum `nofile` threshold of **65535** (or higher) for the runtime user:

```bash
# Inspect current limit
ulimit -n

# Set limit for current shell session
ulimit -n 65535
```

### B. Permanent Configuration via `/etc/security/limits.conf`
Append the following configuration to `/etc/security/limits.conf`:

```ini
* soft nofile 65535
* hard nofile 65535
root soft nofile 65535
root hard nofile 65535
```

### C. Systemd Service Unit (`firefly.service`)
Firefly can be installed automatically via the one-line installer (`install.sh`), which sets up the service below at `/etc/systemd/system/firefly.service`:

```ini
[Unit]
Description=Firefly High-Concurrency AI Gateway
Documentation=https://github.com/dickymuliafiqri/firefly
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=firefly
Group=firefly
EnvironmentFile=-/etc/firefly/firefly.env
ExecStart=/usr/local/bin/firefly -config-dir=/etc/firefly -addr=0.0.0.0:8080
Restart=always
RestartSec=3s
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal

# Security hardening & configuration persistence
ProtectSystem=full
ReadWritePaths=/etc/firefly

# Privileged port binding (:80/:443) when enabling built-in Auto-TLS
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

Reload and enable the daemon:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now firefly.service
```

> **Turso embedded replica path:** the local replica database defaults to `data/firefly.db`, resolved **relative to `-config-dir`**. With the unit above it lands at `/etc/firefly/data/firefly.db`, which is covered by `ReadWritePaths=/etc/firefly`. To store it elsewhere, set an absolute path via `FIREFLY_TURSO_LOCAL_PATH` (or `-turso-local-path`) and ensure that directory is writable by the `firefly` user (add it to `ReadWritePaths`).

> **Turso native library cache:** the embedded Turso engine extracts a bundled native library at runtime. Firefly defaults its cache directory to `<local-db-dir>/.turso-cache` (e.g. `/etc/firefly/data/.turso-cache`) so it does not depend on the service user having a writable `$HOME`. Override with `TURSO_GO_CACHE_DIR` if you need a different location; make sure it is writable by the `firefly` user.

### D. Kernel Network Stack Tuning (`/etc/sysctl.conf`)
For sustained throughput during burst traffic:

```ini
# Socket queue backlog
net.core.somaxconn = 32768
net.ipv4.tcp_max_syn_backlog = 16384

# Port reuse and FIN-WAIT timeout reduction
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
```

---

## 2. Observability & Prometheus Metrics

Firefly exposes an isolated Prometheus registry accessible via the `/metrics` endpoint on the admin plane port (`-admin-addr`).

### Key Saturation & Upstream Metrics
- `firefly_key_inflight_requests{upstream="...", key_ref="..."}`: Gauge counting active in-flight requests on a specific key slot.
- `firefly_key_cooldown_events_total{upstream="...", key_ref="..."}`: Counter tracking how many times a key entered cooldown following an HTTP 429 response.
- `firefly_key_requests_total{upstream="...", key_ref="...", status="..."}`: Counter measuring requests per key categorized by HTTP status code (e.g. 200, 429, 500).
- `firefly_global_inflight_requests`: Gauge measuring server-wide in-flight requests across the admission semaphore.
- `firefly_http_requests_total{method="...", route="...", status="..."}`: Counter tracking gateway ingress requests with route template bounding.
- `firefly_http_request_duration_seconds`: Histogram measuring gateway-to-client response duration.
- `firefly_upstream_requests_total{upstream="...", model="...", outcome="..."}`: Outcome of outbound calls (`ok`, `upstream_error`, `circuit_open`, `client_cancel`).
- `firefly_upstream_request_duration_seconds{upstream="..."}`: Histogram tracking latency to upstream AI providers.
- `firefly_config_generation`: Gauge reporting the active configuration snapshot generation counter.

*Cardinality Note:* The `key_ref` label contains only the slot identifier or environment variable reference name (e.g. `openai-key-1`), never plain-text API secrets.

---

## 3. Secret Masking & Security Auditing

- **Automated Logging Redaction**: All sensitive authentication headers (`Authorization: Bearer ...`, `X-Api-Key`, `X-Admin-Token`, cookies) are automatically redacted by `logging.RedactHandler` into `"[REDACTED]"`.
- **Domain `KeySlot` Masking**: Key data structures implement `fmt.Formatter`, `fmt.Stringer`, and `json.Marshaler` ensuring `%v`, `%+v`, `%#v` verbs and JSON serialization never expose plain-text secrets.
- **Admin Plane Isolation**: Diagnostic endpoints (`/debug/*`, pprof) require constant-time bearer token validation (`-admin-token` or `FIREFLY_ADMIN_TOKEN`). If no token is configured, debug routes are automatically disabled (fail-closed).

---

## 4. Graceful Shutdown & SSE Stream Draining

Firefly supports bounded graceful draining via `-shutdown-grace-seconds` (default: 30 seconds):

1. Upon receiving `SIGINT` or `SIGTERM`:
   - The server enters `shuttingDown` mode immediately.
   - New incoming HTTP requests are rejected with **`503 Service Unavailable`**, **`Retry-After: 5`**, and `Connection: close`.
2. Ongoing SSE streaming connections are given up to the grace period to finish transmitting tokens naturally without premature termination.
3. Once all in-flight requests conclude, or when the grace timeout elapses, sockets are closed and background workers are joined cleanly (verified zero goroutine leaks).

---

## 5. Memory Management & Concurrency Optimization

To maintain stability under 1,000+ simultaneous Server-Sent Events (SSE) streaming connections, Firefly implements multi-layered concurrency patterns:

### A. Zero-Allocation Routing Inspection via `tidwall/gjson`
- **Challenge:** Using `encoding/json.Unmarshal` to inspect `model` and `stream` fields on 100 KB–10 MB prompt bodies allocates massive AST trees or `map[string]any` per request, creating severe GC pressure under concurrent loads.
- **Implementation:** Firefly uses `gjson.GetBytes` directly against the raw byte buffer without full deserialization.
- **Efficiency:** Achieves >90% reduction in memory allocations (B/op and allocs/op) on prompt payloads exceeding 64 KB.

### B. Buffer Recycling (`sync.Pool`) with Capacity Guard
- Request body ingestion and SSE relay framing utilize managed memory pools:
  - `requestBufferPool`: Recycles `bytes.Buffer` instances for initial request ingestion.
  - `readerPool`: Recycles `bufio.Reader` instances (32 KiB buffer) for line-by-line streaming without heap churn.
  - `copyBufferPool`: Recycles 32 KiB slices for I/O streaming relays.
- **Anti-OOM Capacity Guard:** Before returning buffers to the pool, capacity is strictly validated:
  ```go
  if buf.Cap() <= 512*1024 {
      buf.Reset()
      requestBufferPool.Put(buf)
  }
  ```
  Buffers that have expanded past 512 KiB (e.g. from an occasional massive request) **are dropped to the GC**, preventing the pool from indefinitely holding oversized buffers in the heap.

### C. Outbound HTTP/2 Multiplexing & Connection Pooling (`upstream.Pool`)
- Outbound requests share singleton `*http.Client` instances cached for the process lifetime:
  ```go
  MaxIdleConns:          2000,
  MaxIdleConnsPerHost:   1000,
  MaxConnsPerHost:       1500,
  IdleConnTimeout:       90 * time.Second,
  ForceAttemptHTTP2:     true,
  ResponseHeaderTimeout: 30 * time.Second, // Timeout to first byte (TTFB)
  ```
- `http.Client.Timeout = 0`: Intentionally left unbounded so streaming sessions lasting many minutes are not severed prematurely. Timeouts are enforced via `ResponseHeaderTimeout` and `StreamWatchdog`.

### D. Structured Background Concurrency (`golang.org/x/sync/errgroup`)
- **Bounded Probing:** Background `HealthChecker` components limit concurrency via `g.SetLimit(10)`, preventing periodic health checks from congesting local or upstream network queues.
- **Panic Boundaries:** Every probe goroutine is wrapped in `defer func() { recover() }` to ensure isolated probe panics do not compromise background worker lifecycles.
- **Leak-Free Context Binding:** Workers terminate on `ctx.Done()`, verified with `go.uber.org/goleak`.

### E. Global Admission Semaphore & Backpressure (`httpx.GlobalLimiter`)
- Enforces a 1,500-slot server-wide in-flight semaphore with a bounded queue (timeout 1.5s).
- If saturated and the queue expires, requests fail fast with `HTTP 429 Too Many Requests` (`Retry-After: 2`), shielding the server from thundering herd spikes and OOM crashes.

---

## 6. Auto-TLS & HTTPS Deployment Guide

Firefly includes native Let's Encrypt automated TLS certificate issuance and renewal via ACME HTTP-01 challenge (`internal/server/autotls.go`).

### A. Port & Linux Capability Requirements
Auto-TLS binds directly to standard privileged ports:
- **Port 80 (HTTP)**: Serves the `/.well-known/acme-challenge/` token challenge and permanent 301 redirects to HTTPS.
- **Port 443 (HTTPS)**: Serves encrypted data and dashboard traffic.

When running as an unprivileged user (e.g. `firefly` via systemd), granting socket bind capabilities is required:
```bash
sudo setcap 'cap_net_bind_service=+ep' /usr/local/bin/firefly
```
Or in the systemd service unit (`/etc/systemd/system/firefly.service`):
```ini
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
```

### B. Coexistence with Existing Reverse Proxies (Nginx / Caddy / Traefik)
- **Behind a Reverse Proxy:** If Nginx, Caddy, or Traefik is already terminating SSL/TLS in front of Firefly (e.g., `proxy_pass http://localhost:8080`), **leave Firefly's built-in Auto-TLS disabled**. The edge proxy manages TLS certificates.
- **Standalone Direct Exposure:** If Firefly is directly exposed to the internet without an external proxy, ensure ports 80 and 443 are free (`sudo ss -tulpn | grep -E ':(80|443)'`), firewall allows ingress (`sudo ufw allow 80/tcp && sudo ufw allow 443/tcp`), and public DNS (A/AAAA record) resolves to the server's public IP.

