# Rencana Implementasi sing-box sebagai Egress Proxy (di samping WARP)

## 1. Ringkasan

Firefly menambah mode egress baru **`singbox`** — operator memilihnya per-upstream (di samping `direct`, `warp`, `proxy`). Backend meng-embed **sing-box v1.14.3 sebagai library Go**, membangun satu **pool Box instance global** yang menampung semua outbound (node) hasil parsing **subscription URL**, lalu me-routing dial per-upstream lewat **outbound tag** tertentu. UI mendapat halaman **Singbox** baru (grup PROXIES) yang mirip WarpPage: status pool, daftar node, health check, form subscription.

### Keputusan desain (sudah dikonfirmasi operator)

| Aspek | Keputusan |
| :--- | :--- |
| Lisensi | **Relicense Firefly MIT → GPL-3.0-or-later** (syarat embed sing-box). |
| Topologi | **Pool Box global**, dial per tag. Bukan Box per-upstream. |
| Sumber config | **Subscription URL** (fetch + parse periodik). Tanpa paste JSON / upload file / hot-reload file lokal. |
| UI | **Halaman "Singbox" baru** di grup PROXIES (`registry.tsx`), di samping WarpPage. |
| Cakupan egress | Per-upstream (bukan inbound / bukan reverse tunnel). |
| Parser subscription | **Ditulis sendiri dari nol** (tanpa dependensi eksternal) — mendukung `ss`, `vmess`, `vless` (termasuk **Reality**), `trojan`, `hysteria2`, `tuic`, `socks`, `http`. Library pihak ketiga (LalatinaHub/common) ditolak: gap Reality, `insecure:true` hardcoded, tanpa hysteria2/tuic. Lihat §5.2. |
| Default dial | **URLTest group `auto`** — upstream tanpa `singbox_outbound_tag` dial ke grup `urltest` (`auto`) yang memilih node tercepat; `selector` (`select`) tersedia untuk override manual. Lihat §5.2.4. |
| Gagal start | **Fail-open ke `direct`** — jika subscription/Box gagal saat boot, upstream `singbox` dial langsung tanpa proxy; log warning keras. Lihat §5.1 & §13. |
| Routing internal | **Tag per-upstream saja** — tanpa rule domain/IP, tanpa geoip/geosite. Hanya grup `auto`/`select` + `direct` bawaan. |
| Sumber subscription | **Satu URL** aktif (refresh periodik, bisa diganti dari dashboard). |

---

## 2. Perubahan Lisensi (prasyarat, non-teknis)

- Ganti isi `LICENSE` (root) dari MIT ke **GNU GPL v3** teks penuh.
- Tambah baris `## License` di `README.md`: "Firefly is licensed under the GNU General Public License v3.0 or later (GPL-3.0-or-later)."
- Cek `frontend/LICENSE` / `frontend/package.json` `license` field — ikut diganti.
- Pastikan tidak ada dependensi GPL-incompatible di go.mod yang bertabrakan (cek dengan `licensecheck` / manual). Sebagian besar dependensi Firefly sudah permissive (MIT/Apache-2.0), kompatibel dengan GPLv3 sebagai host.
- Catatan di `CHANGELOG.md`: versi rilis berikutnya menandai breaking license change.

---

## 3. Domain & Konstanta

### 3.1 `internal/domain/domain.go`

- Tambah konstanta:
  ```go
  // EgressModeSingbox routes traffic through the embedded sing-box engine.
  const EgressModeSingbox = "singbox"
  ```
- Di struct `Upstream`, tambah field baru:
  ```go
  // SingboxOutboundTag selects which sing-box outbound (node) this upstream
  // dials through when EgressMode is "singbox". Empty means "use the pool's
  // default / URL-test group".
  SingboxOutboundTag string `json:"singbox_outbound_tag,omitempty"`
  ```

### 3.2 `internal/transport/warp/proxy.go`

- Tambah `IsSingboxEgress(mode string) bool` (mirror `IsWarpEgress`).
- Di `ConfigureTransportEgress`, tambah cabang `singbox` di switch: ambil `cfg.SingboxDialer` (sebuah `Dialer` / `func(ctx, network, addr string) (net.Conn, error)`), set `dialContext` dan `proxyFunc = nil` (sama seperti warp).

---

## 4. Konfigurasi & Validasi

### 4.1 `internal/config/dto.go`

- `UpstreamDTO`: tambah field `SingboxOutboundTag string \`json:"singbox_outbound_tag,omitempty"\``.
- Tambah DTO baru untuk file persistensi `singbox.json` (mirip `WarpDTO`):
  ```go
  type SingboxDTO struct {
      SubscriptionURL   string `json:"subscription_url,omitempty"`
      RefreshIntervalS  int    `json:"refresh_interval_s,omitempty"` // default 3600
      DefaultOutboundTag string `json:"default_outbound_tag,omitempty"`
  }
  ```

### 4.2 `internal/config/builder.go`

- Validasi `egress_mode`: tambah `"singbox"` ke daftar yang diizinkan.
- Jika `egress_mode == "singbox"`:
  - `SingboxOutboundTag` opsional (kosong = gunakan default group URLTest).
  - Validasi tag character set (harus valid identifier: alphanumerik, `-`, `_`).
  - Tidak wajibkan `ProxyURL`.
- Assign `SingboxOutboundTag` di struct hasil `translateUpstream`.

### 4.3 `internal/config/singbox.go` (file baru)

- `LoadSingbox(dir string) (SingboxDTO, error)` / `SaveSingbox(dir, cfg)` — pola identik `LoadWarp`/`SaveWarp`: atomic temp+rename, strict decode, missing file = zero-value.
- `FileNameSingbox = "singbox.json"`.

---

## 5. Package Transport Baru: `internal/transport/singbox/`

Struktur file (mirror `warp/`):

| File | Tanggung jawab |
| :--- | :--- |
| `singbox.go` | Tipe `Manager`, `Status`, konstanta `EgressModeSingbox`, `IsSingboxEgress`, `Dialer` interface. |
| `subscription.go` | Fetch + parse subscription URL → `[]option.Outbound`. Base64 decode, line-split, parse `ss://`, `vmess://`, `vless://`, `trojan://`, `hysteria2://`, `tuic://`, `socks://`, `http://` ke `option.Outbound{Type,Tag,Options}`. |
| `manager.go` | Lifecycle `Manager`: `NewManager`, `Start(ctx)`, `Close()`, `Refresh(ctx)` (fetch ulang + rebuild Box), `DialContext(ctx, network, addr)`, `OutboundForTag(tag)`, `Status()`, `SetSubscriptionURL`, `SetDefaultOutboundTag`, `Nodes()` (list tag+type+server untuk UI). |
| `health.go` | Periodic health-check (TCP dial test) ke tiap node, expose `NodeHealth` di Status. |
| `manager_test.go` / `subscription_test.go` | Unit test parsing & lifecycle. |

### 5.1 `Manager` (inti)

```go
type Manager struct {
    mu            sync.RWMutex
    box           *box.Box          // instance aktif; nil = belum siap (fail-open)
    outbounds     []option.Outbound // parsed dari subscription
    defaultTag    string            // "auto" (urltest group) secara default
    subURL        string
    refreshEvery  time.Duration
    logger        *slog.Logger
    stopRefresh   context.CancelFunc
    nodeHealth    map[string]NodeHealth
    ready         atomic.Bool       // false sampai Box sukses start
}

type Dialer interface {
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}
```

- `NewManager(logger, subURL, refreshEvery, defaultTag)` → validasi & siapkan. `defaultTag` default `"auto"` bila kosong.
- `Start(ctx)`:
  1. Fetch subscription (jika URL kosong / fetch gagal → Box dibiarkan `nil`, set `ready=false`, **fail-open**: `DialContext` akan langsung memanggil dialer `direct`).
  2. Build `option.Options{Outbounds: parsed + urltest(auto) + selector(select) + direct}` (tanpa inbounds/route/DNS; rule `ip_is_private → direct` tidak dipakai sesuai keputusan "tag per-upstream saja").
  3. `box.New(box.Options{Context: include.Context(context.Background()), Options: options})` → `box.Start()`; `ready=true`.
  4. `supervisor.Go(ctx, "singbox.refresh", ...)` loop `Refresh` periodik.
- `Refresh(ctx)`:
  1. Re-fetch subscription → parse → diff dengan `outbounds` lama.
  2. Jika berubah: `box.Close()`, rebuild Box baru, swap atomik (mirror WARP hot-swap).
  3. Jika gagal fetch: pertahankan Box lama, log warning (jangan turunkan `ready`).
- `DialContext(ctx, network, addr string) (net.Conn, error)`:
  1. `m.mu.RLock()` → ambil `m.box`, `m.defaultTag`.
  2. **Fail-open**: jika `m.box == nil` (belum/tidak siap), pakai `net.Dialer` langsung (bypass sing-box) dan log warning pertama.
  3. `out, ok := box.Outbound().Outbound(tag)` — `out` adalah `adapter.Outbound` yang mengimplement `N.Dialer`. Jika tag tidak ditemukan → fallback ke `"auto"` → lalu `direct`.
  4. Konversi `addr` → `M.Socksaddr` (parse host:port), panggil `out.DialContext(ctx, network, sockAddr)`.
  5. Kembalikan `net.Conn`.
- `OutboundForTag(tag string) (adapter.Outbound, bool)` — akses tag spesifik untuk dial per-upstream.
- `Status()` → `StatusDTO{ Running, BoxCreated, NodeCount, DefaultTag, SubscriptionURL (masked), RefreshInterval, LastRefresh, Nodes []NodeStatus, Health []NodeHealth }`.

### 5.2 `subscription.go` — parser ditulis sendiri dari nol

> **Keputusan**: parser dibuat sendiri, **tanpa dependensi eksternal** (LalatinaHub/common ditolak karena gap Reality, `insecure:true` hardcoded, tanpa hysteria2/tuic). Alasan: kontrol penuh atas output `option.Outbound` (security, Reality, tanpa bloat), dan sing-box `option` package sudah menyediakan semua struct yang dibutuhkan.

#### 5.2.1 Fetch & deteksi format

1. `http.Get(subURL)` — timeout 30s, `User-Agent` default, ikuti redirect.
2. Baca body (limit 10MB).
3. **Deteksi whole-body base64**: jika body tidak mengandung `://` sama sekali, coba decode base64 (Std/URL/Raw). Jika hasil decode mengandung `://`, gunakan hasil decode. Jika tidak, anggap plain text.
4. Split baris → trim → skip kosong / comment `#` dan `//`.

#### 5.2.2 Per-scheme parser → `option.Outbound`

Setiap fungsi mengembalikan `option.Outbound{Type, Tag, Options}` langsung (bukan map). Tag default: `#fragment` (URL-unescaped) atau hostname atau `<type>-<N>`.

| Scheme | Struct target | Catatan parsing |
| :--- | :--- | :--- |
| `ss://` | `option.ShadowsocksOutbound` | Dua varian: `ss://base64(method:pass)@host:port` dan `ss://base64(method:pass@host:port)`. Baca `plugin` & `plugin_opts` dari query. |
| `vmess://` | `option.VMessOutbound` | base64 JSON → field `add/port/id/aid/net/host/path/tls/sni/ps`. `alter_id=0` (deprecated di 1.14). Jika query ada `security=reality` → `RealityOptions{PublicKey, ShortID}`. |
| `vless://` | `option.VLESSOutbound` | `vless://uuid@host:port?params#name`. Params: `type` (transport), `security` (`tls`/`reality`/`none`), `sni`, `host`, `path`, `serviceName`, `fp`, `pbk` (reality public key), `sid` (reality short id). Reality → `RealityOptions`. |
| `trojan://` | `option.TrojanOutbound` | `trojan://pass@host:port?params#name`. Params sama seperti vless (transport, security=tls, sni, host, path). |
| `hysteria2://` / `hy2://` | `option.Hysteria2Outbound` | `hy2://auth@host:port?params#name`. Params: `sni`, `insecure`, `obfs`, `obfs-password`, `up`, `down`. |
| `tuic://` | `option.TUICOutbound` | `tuic://uuid:password@host:port?params#name`. Params: `congestion_control`, `alpn`, `udp_relay_mode`, `sni`, `insecure`. |
| `socks://` | `option.SOCKSOutbound` | `socks://user:pass@host:port`. |
| `http://` / `https://` | `option.HTTPOutbound` | `http://user:pass@host:port`. |
| `ssr://`, `snell://`, `anytls://` | *(unsupported)* | Log warning, skip node, lanjut ke line berikutnya. |

#### 5.2.3 Aturan keamanan (non-negotiable)

- **`insecure` default `false`**. Field `ProxyNode.Insecure`-style hanya di-set `true` jika URL query eksplisit membawa `insecure=1`/`true`. Tidak pernah di-set otomatis.
- **Reality dihormati**: jika `security=reality`, populate `option.VLESSOutbound.RealityOptions{PublicKey: pbk, ShortID: sid}` (atau `option.VMessOutbound.RealityOptions` untuk vmess) — JANGAN dipaksa jadi TLS biasa.
- **`utls` fingerprint**: jika query `fp` ada, set `option.OutboundTLSOptions.Fingerprint` (mis. `chrome`, `firefox`, `safari`).

#### 5.2.4 Hasil akhir

- Kembalikan `[]option.Outbound` (hanya node outbounds — TANPA selector/urltest/direct).
- `Manager.Start` menambahkan grup `urltest` + `selector` + `direct` secara terprogram (bukan dari parser). **`auto` (urltest) adalah dial target default**; `select` (selector) untuk override manual dari dashboard/upstream tag:
  ```go
  outbounds = append(outbounds,
      // node outbounds dari parser...
      option.Outbound{Type: "urltest", Tag: "auto", Options: &option.URLTestOutboundOptions{
          Outbounds: nodeTags,
          URL:       "https://cp.cloudflare.com/generate_204",
          Interval:  badoption.Duration(3 * time.Minute),
          Tolerance: 50,
      }},
      option.Outbound{Type: "selector", Tag: "select", Options: &option.SelectorOutboundOptions{
          Outbounds: append([]string{"auto"}, nodeTags...),
          Default:   "auto",
      }},
      option.Outbound{Type: "direct", Tag: "direct"},
  )
  ```
- **Tidak ada inbound / route rule / DNS** di config — kita hanya perlu outbounds. `option.Options` cukup dengan `Outbounds` saja.

#### 5.2.5 File & test

| File | Isi |
| :--- | :--- |
| `subscription.go` | `Fetch(url) ([]byte, error)`, `ParseSubscription(raw []byte) ([]option.Outbound, error)`. |
| `parse_ss.go` | `parseShadowsocks(line string) (option.Outbound, error)` — dua varian URL. |
| `parse_vmess.go` | `parseVMess(line string) (option.Outbound, error)` — base64 JSON + Reality. |
| `parse_vless.go` | `parseVLESS(line string) (option.Outbound, error)` — URL parse + Reality + utls. |
| `parse_trojan.go` | `parseTrojan(line string) (option.Outbound, error)`. |
| `parse_hysteria2.go` | `parseHysteria2(line string) (option.Outbound, error)`. |
| `parse_tuic.go` | `parseTUIC(line string) (option.Outbound, error)`. |
| `parse_socks_http.go` | `parseSocks`, `parseHTTP` — untuk proxy statis via subscription. |
| `subscription_test.go` | Test per-scheme (varian URL umum), whole-body base64, insecure default, Reality, skip invalid line. |

#### 5.2.6 Referensi format

- https://sing-box.sagernet.org/configuration/outbound/ (struktur `option.Outbound`)
- https://github.com/SagerNet/sing-box/blob/main/option/vless.go (RealityOptions)
- https://github.com/SagerNet/sing-box/blob/main/option/hysteria2.go
- https://github.com/SagerNet/sing-box/blob/main/option/tuic.go
- Format subscription nyata: https://github.com/Pawdroid/Free-servers (contoh base64 blob), berbagai provider VPN.

---

## 6. Integrasi Upstream (transport/upstream)

### 6.1 `internal/transport/upstream/client.go`

- `buildClient(...)` — tambah parameter `singboxMgr singbox.Dialer` (atau lewat struct deps).
- Setelah `warp.ConfigureTransportEgress(tr, ...)`, tambah cabang:
  ```go
  if singbox.IsSingboxEgress(u.EgressMode) && singboxMgr != nil {
      // ConfigureTransportEgress sudah set dialContext ke singboxMgr.DialContext
      // via cfg.SingboxDialer.
  }
  ```
- Tidak perlu retry-transport khusus (sing-box outbound handle failover internal via `detour`/URLTest group).

### 6.2 `internal/server/upstream_check.go`

- Sama: teruskan `singboxMgr` ke `ConfigureTransportEgress` via `EgressConfig.SingboxDialer`.

---

## 7. Server Routes & Handlers

### 7.1 `internal/server/router.go`

- `RouterDeps` tambah field `SingboxManager *singbox.Manager`.
- Di `buildHandler`, tambah route group `/api/singbox/` (pola `/api/warp/`):
  ```
  GET  /api/singbox/status      → deps.authorizeAdmin → handleSingboxStatus
  POST /api/singbox/refresh     → admin → handleSingboxRefresh (force re-fetch subscription)
  POST /api/singbox/subscription → admin → body {subscription_url, refresh_interval_s} → save + apply
  POST /api/singbox/default-tag  → admin → body {tag} → set default outbound tag
  GET  /api/singbox/nodes        → admin → list node + health
  ```

### 7.2 `internal/server/singbox_handlers.go` (file baru)

- `handleSingboxStatus`, `handleSingboxRefresh`, `handleSingboxSubscription`, `handleSingboxDefaultTag`, `handleSingboxNodes`.
- Pola identik `warp_handlers.go`: `authorizeAdmin(r)`, `writeJSON(w, r, status)`, error mapping (400 invalid input, 409 sudah jalan, 503 belum siap).

---

## 8. Wiring di `cmd/firefly/main.go`

- Flag baru (mirip `warp-*`):
  ```go
  singboxSubURL      = flag.String("singbox-subscription-url", "", "subscription URL for sing-box egress nodes (defaults to $FIREFLY_SINGBOX_SUBSCRIPTION_URL or singbox.json)")
  singboxRefresh     = flag.Duration("singbox-refresh-interval", 3600*time.Second, "interval between sing-box subscription refreshes (0 disables)")
  singboxDefaultTag  = flag.String("singbox-default-tag", "", "default sing-box outbound tag for upstreams with empty singbox_outbound_tag")
  ```
- Baca env `FIREFLY_SINGBOX_SUBSCRIPTION_URL`, `FIREFLY_SINGBOX_REFRESH_INTERVAL`, `FIREFLY_SINGBOX_DEFAULT_TAG` (pola flag > env > file).
- `config.LoadSingbox(configDir)` → prioritas flag > env > file.
- `singboxManager := singbox.NewManager(logger, subURL, refresh, defaultTag)` (dekat `warpManager`).
- `singboxManager.Start(ctx)` (supervised, mirror warm-up warp).
- Pass ke `upstream.NewPool(...)` / `buildClient` via deps struct.
- Pass ke `server.RouterDeps{ SingboxManager: singboxManager }`.
- `defer singboxManager.Close()` saat shutdown.

---

## 9. Frontend

### 9.1 `frontend/src/registry.tsx`

- Import `SingboxPage`.
- Tambah entry di grup `PROXIES` (setelah warp):
  ```tsx
  singbox: { id: 'singbox', title: 'Singbox', description: 'sing-box egress pool & subscription.', group: 'PROXIES', icon: Network, element: () => <SingboxPage /> },
  ```

### 9.2 `frontend/src/pages/SingboxPage.tsx` (file baru)

- Section **Status**: running/box created/node count/default tag/last refresh (mirror WarpPage status card).
- Section **Subscription**: form input `subscription_url`, `refresh_interval_s`, tombol Save + Refresh Now.
- Section **Default Outbound**: dropdown node (dari `/api/singbox/nodes`), tombol Save.
- Section **Nodes**: tabel `tag | type | server | latency | health` (health check badge).
- Hook `useSingboxStatus()` → `services/api.ts`.

### 9.3 `frontend/src/services/api.ts`

- Tambah DTO `SingboxStatusDTO`, `SingboxNodeDTO`.
- Tambah fetcher: `fetchSingboxStatus`, `refreshSingbox`, `updateSingboxSubscription`, `updateSingboxDefaultTag`, `fetchSingboxNodes`.
- Tambah mock untuk dev offline (`singboxMock`).

### 9.4 `frontend/src/services/schema.ts`

- Tambah `singbox_outbound_tag?: string` ke `UpstreamDTO`.
- Update union `egress_mode` di `UpstreamDTO` untuk sertakan `'singbox'`.

### 9.5 `frontend/src/components/upstream/GeneralTab.tsx`

- Tambah `<option value="singbox">singbox</option>` ke dropdown Egress mode.
- Saat `egressMode === "singbox"`, render field `Singbox outbound tag` (text input, opsional, placeholder "default / URL-test group").
- Extend `GeneralState.egressMode` union: `"direct" | "warp" | "proxy" | "singbox"`.
- Tambah `singboxOutboundTag: string` ke `GeneralState`.

### 9.6 `frontend/src/pages/UpstreamEditorPage.tsx`

- Pass `singboxOutboundTag` ke state `general`, kirim di body `egress_mode === "singbox" ? { singbox_outbound_tag } : undefined`.

---

## 10. Observability & Resilience

- `supervisor.Go` untuk loop refresh subscription (`singbox.refresh`) dan health check (`singbox.health`).
- `metrics.Metrics`: counter `singbox_refresh_total{result}`, gauge `singbox_nodes`, gauge `singbox_ready` (0/1, untuk memantau kondisi fail-open), histogram `singbox_node_latency_seconds{tag}`.
- Logging: `logger.Info("sing-box egress refreshed", "nodes", n, "changed", bool)`; **warning keras sekali** saat fail-open aktif (`"sing-box egress unavailable; falling back to direct dial"`).
- Graceful shutdown: `Manager.Close()` → cancel refresh ctx, `box.Close()`.
- Subscription fetch gagal → pertahankan Box lama, exponential backoff (mirip WARP rotation backoff).

---

## 11. Strategi Pengujian

| Layer | Test |
| :--- | :--- |
| `subscription_test.go` | Whole-body base64 & plain text; per-scheme parser (`ss`, `vmess`, `vless`, `trojan`, `hysteria2`, `tuic`, `socks`, `http`); **Reality** (`vless`/`vmess` → `RealityOptions`); **`insecure` default false** (hanya true bila query eksplisit); `utls` fingerprint (`fp`); invalid line di-skip; tag default (`#fragment` → hostname → `<type>-N`); unsupported scheme di-skip tanpa error fatal. |
| `manager_test.go` | Start/Close, Refresh diff & swap (race-free), `DialContext` via mock outbound (localhost SOCKS5 server). |
| `warp/proxy_test.go` | Extend `TestConfigureTransportEgress` dengan cabang `singbox`. |
| `builder_test.go` | Validasi `egress_mode=singbox` + tag, tag invalid character. |
| `server_test.go` | Route `/api/singbox/status` auth & shape, subscription update 400/200. |
| `frontend` | Component test SingboxPage (mock fetch), GeneralTab egress union. |
| Integration | E2E: spin mock subscription server → `Manager.Start` → `DialContext` ke target → assert koneksi via node. |

---

## 12. Urutan Implementasi (disarankan)

1. **Lisensi** — ganti LICENSE, README, frontend license (prasyarat, commit terpisah).
2. **Domain & konstanta** — `EgressModeSingbox`, `IsSingboxEgress`, field `Upstream.SingboxOutboundTag`, wire ke `EgressConfig`.
3. **Config** — DTO, validation, `singbox.go` persistence, test.
4. **Package `internal/transport/singbox/`** — `Manager` + **parser dari nol** (`subscription.go`, `parse_ss.go`, `parse_vmess.go`, `parse_vless.go`, `parse_trojan.go`, `parse_hysteria2.go`, `parse_tuic.go`, `parse_socks_http.go`) + test (ini bagian terbesar; pecah jadi PR kecil: parser dulu, lalu manager).
5. **Integrasi upstream** — `client.go`, `upstream_check.go`.
6. **Server routes** — `singbox_handlers.go`, `router.go`.
7. **Wiring main.go** — flag/env/file precedence, lifecycle.
8. **Frontend** — schema, api, GeneralTab, UpstreamEditorPage, SingboxPage, registry.
9. **Observability** — metrics, logging, supervisor.
10. **Docs** — CHANGELOG, README egress section, AGENTS.md.

---

## 13. Risiko & Mitigasi

| Risiko | Mitigasi |
| :--- | :--- |
| **Bug parser sendiri** (format subscription liar) | Parser ditulis dari nol tanpa dependensi eksternal (keputusan: library LalatinaHub ditolak karena gap Reality, `insecure:true` hardcoded, tanpa hysteria2/tuic). Mitigasi: test per-scheme dengan varian URL nyata, fixture subscription asli, skip-dengan-warning alih-alih gagal fatal, dan validasi output via `option.Outbound` struct (compile-time typed). |
| Format proprietary (Clash YAML, Surge) tidak didukung | Batasi ke format standar URI list. Tolak dengan pesan jelas + log. Parser Clash YAML bisa ditambah di iterasi berikutnya. |
| **Reality / hysteria2 / tuic salah parse** | Parser menangani Reality (`RealityOptions`), hysteria2 (`obfs`, `up`/`down`), tuic (`congestion_control`, `udp_relay_mode`) secara eksplisit. Test khusus per-protokol. |
| **`insecure:true` membocorkan verifikasi TLS** | Default `false`; hanya `true` bila query eksplisit (`insecure=1`). Test eksplisit untuk default aman. |
| sing-box API break antar minor version | Pin `v1.14.3` eksplisit di go.mod. Upgrade = deliberate, jalankan ulang test suite. |
| Memory / goroutine leak dari Box instance | `Close()` on shutdown + `Refresh` diff-swap (close Box lama). Test leak dengan `goleak`. |
| Subscription fetch lambat / gagal | Timeout 30s, backoff, pertahankan Box lama, health check terpisah. |
| License incompatibility dependensi | Audit go.mod licenses sebelum commit LICENSE baru (tool `licensecheck`). Sing-box GPLv3 + deps harus kompatibel; parser kita sendiri tidak menambah dependensi. |
| `DialContext` signature mismatch (N.Dialer vs std) | Wrapper yang konversi `addr string` → `M.Socksaddr` di `Manager.DialContext`. Test dengan real outbound (direct). |

---

## 14. Keputusan Final (semua pertanyaan terbuka sudah terjawab)

| # | Pertanyaan | Keputusan | Implikasi implementasi |
| :--- | :--- | :--- | :--- |
| 1 | Default dial saat `singbox_outbound_tag` kosong | **URLTest group `auto`** | `Manager.Start` selalu menambahkan `urltest` tag `auto` + `selector` tag `select`; `defaultTag = "auto"`. Upstream tanpa tag → dial `auto`. |
| 2 | Perilaku jika Box gagal start / subscription gagal | **Fail-open ke `direct`** | `Manager.DialContext` cek `box == nil` → pakai `net.Dialer` langsung + log warning pertama. `ready` atomic untuk status UI. |
| 3 | Kedalaman routing internal | **Tag per-upstream saja** | Tanpa `route.rules`, tanpa `ip_is_private`, tanpa geoip/geosite. Outbounds = node + `auto` + `select` + `direct`. |
| 4 | Jumlah sumber subscription | **Satu URL** | `singbox.json` hanya menyimpan satu `subscription_url`; tanpa prefix tag per-sumber, tanpa file lokal. |

Verifikasi: semua keputusan di atas **konsisten** dengan §1 (Keputusan desain), §5.1 (`Manager`), §5.2.4 (grup outbound), dan §13 (risiko fail-open).

---

*File ini adalah living document — perbarui saat keputusan desain berubah.*
