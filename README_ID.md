# Firefly

[English](./README.md) | **Bahasa Indonesia**

[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![CI Status](https://github.com/dickymuliafiqri/firefly/actions/workflows/ci.yml/badge.svg)](https://github.com/dickymuliafiqri/firefly/actions)
[![Concurrency](https://img.shields.io/badge/Concurrency-1%2C000%2B%20SSE%20Streams-emerald)](./docs/loadtest.md)

Firefly adalah AI reverse proxy dan API gateway berkonkurensi tinggi, multi-tenant, dan kompatibel dengan wire OpenAI. Ditulis murni dalam Go, Firefly dirancang khusus untuk melayani ribuan koneksi streaming Server-Sent Events (SSE) secara bersamaan (1.000+ stream inferensi konkuren) dengan zero allocation pada hot path, Virtual Combos untuk load balancing model, rotasi API key lock-free, dan dashboard manajemen React bertema nokturnal yang tertanam (embedded).

> Dokumen ini adalah terjemahan Bahasa Indonesia dari [README.md](./README.md) (versi Bahasa Inggris). Bila terjadi perbedaan, versi Bahasa Inggris menjadi rujukan utama.

---

## Arsitektur Sistem Backend

Firefly memisahkan secara ketat data plane klien (penerusan request) dari admin plane (observabilitas, metrik, dan manajemen konfigurasi):

```
                          [ HTTP Clients / AI SDKs ]
                                    │
                                    ▼ 0.0.0.0:8080 (Data Plane)
 ┌────────────────────────────────────────────────────────────────────────┐
 │  [ Drain Guard ] ──► [ RequestID ] ──► [ Metrics ] ──► [ Recover ]    │
 │                           │                                            │
 │                           ▼                                            │
 │                 [ Logging (Masked slog) ]                              │
 │                           │                                            │
 │                           ▼                                            │
 │             [ Global Admission Limiter (1,500 slots) ]                 │
 │                           │                                            │
 │                           ▼                                            │
 │                     [ http.ServeMux ]                                  │
 │         ┌─────────────────┼─────────────────┬────────────────┐         │
 │         ▼                 ▼                 ▼                ▼         │
 │    GET /healthz     GET /api/settings  GET /v1/models   POST /v1/chat  │
 │    (Probe Bypass)   (Admin Config)     (Tenant Catalog) POST /v1/comp  │
 │                                       POST /v1/embeddings              │
 │                                             │                          │
 │                                             ▼                          │
 │                              ┌──────────────────────────────┐          │
 │                              │ Route Protection Middleware  │          │
 │                              │  - Auth (Bearer Token Check) │          │
 │                              │  - Tenant Admission (RPS)    │          │
 │                              └──────────────┬───────────────┘          │
 │                                             │                          │
 │                                             ▼                          │
 │                                   [ forwardEndpoint ]                  │
 │                                             │                          │
 │                  ┌──────────────────────────┴──────────────────────┐   │
 │                  ▼                                                 ▼   │
 │     [ Target & KeyRing Selection ]                   [ sync.Pool Buffer ]
 │     (Least-Inflight / Round-Robin)                   (Max 32MB / Cap Guard)
 │                  │                                                     │
 │                  ▼                                                     │
 │       [ Upstream Adapter ] (OpenAI / Anthropic Claude)                 │
 └──────────────────┬─────────────────────────────────────────────────────┘
                    │
                    ▼
 ┌────────────────────────────────────────────────────────────────────────┐
 │                         OUTBOUND CONNECTION LAYER                      │
 │  [ upstream.Pool ] ──► Persistent *http.Client (HTTP/2 Multiplex)      │
 │  [ Layer 1: KeyRing ] (429/401 Cooldown)  │ [ Layer 2: Breaker ] (5xx) │
 │                                           ▼                            │
 │                    [ Upstream AI Providers ]                           │
 └────────────────────────────────────────────────────────────────────────┘
```

---

## Alur Pemrosesan Request End-to-End

Setiap request yang masuk ke Firefly mengikuti siklus hidup yang ketat dan deterministik:

### 1. Ingress & Drain Guard
- Request tiba pada listener data plane (default `0.0.0.0:8080`).
- **Zero-Config First Run:** Firefly boot dengan mulus bahkan tanpa berkas konfigurasi awal, secara otomatis menghasilkan skema JSON default dan menunggu konfigurasi melalui Admin UI/API (`/api/settings`).
- **Drain Guard:** Memeriksa status siklus hidup server secara atomik. Saat graceful shutdown dimulai, request baru yang masuk langsung ditolak dengan `HTTP 503 Service Unavailable` (`Retry-After: 5`), sementara koneksi streaming SSE yang aktif diberi waktu hingga `ShutdownGrace` (default 30 detik) untuk selesai secara alami.

### 2. Pipeline Middleware Global (`httpx`)
1. **`RequestIDMiddleware`:** Menerima `X-Request-ID` dari klien atau menghasilkan UUIDv4 acak secara kriptografis, lalu melampirkannya ke `context.Context` dan header respons.
2. **`MetricsMiddleware`:** Mencatat durasi request, kode status, dan counter request in-flight ke Prometheus.
3. **`RecoverMiddleware`:** Melindungi dari panic. Setiap panic yang tidak tertangani dipulihkan dengan aman, stack trace dicatat ke log, dan respons JSON `HTTP 500` dikembalikan bila header belum ditulis.
4. **`LoggingMiddleware`:** Menghasilkan log akses JSON terstruktur via `log/slog` dengan redaksi rahasia otomatis (`RedactHandler`).
5. **`GlobalLimiter`:** Semaphore konkurensi seluruh server (default 1.500 slot). Bila jenuh, request mengantre hingga 1,5 detik sebelum gagal cepat dengan `HTTP 429 Too Many Requests` (`Retry-After: 2`). Probe `/healthz` melewati limiter ini sepenuhnya.

### 3. Multiplexing Rute (`http.ServeMux` Go 1.22+)
- `GET /healthz` -> Mengembalikan `HTTP 200 "ok"` langsung tanpa autentikasi.
- `GET /v1/models` & `GET /v1/models/{id}` -> Rute terautentikasi yang mengembalikan model katalog yang dapat diakses tenant peminta.
- `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings` -> Diarahkan ke mesin eksekusi proxy bersama `forwardEndpoint`.

### 4. Proteksi Rute Tenant (`deps.protected`)
Sebelum mem-parsing body request, request melewati dua lapis proteksi tenant:
1. **`AuthMiddleware`:** Memvalidasi `Authorization: Bearer sk-gw-...`. Token diverifikasi terhadap `TenantStore` menggunakan perbandingan constant-time yang aman. Token tidak valid langsung menghasilkan `HTTP 401 Unauthorized` menggunakan skema error JSON standar OpenAI. Interface dependensi dijaga dengan `httpx.IsNil` untuk menghilangkan panic akibat typed-nil.
2. **`AdmissionMiddleware`:** Menegakkan batas rate tenant menggunakan Token Bucket (`golang.org/x/time/rate`) untuk request per detik (RPS) dan menegakkan batas in-flight konkuren (`MaxConcurrent`). Batas yang terlampaui mengembalikan `HTTP 429`.

### 5. Eksekusi Forward Handler (`server.forwardEndpoint`)
Handler terpadu untuk endpoint inferensi:
- **Pemeriksaan Ukuran Body:** Langsung menolak body lebih besar dari 32 MB dengan `HTTP 413 Payload Too Large`.
- **Buffer Recycling (`sync.Pool`):** Meminjam instance `bytes.Buffer` dari `requestBufferPool` dan membaca body melalui `io.LimitReader`. Buffer berkapasitas `<= 512KB` di-reset dan dikembalikan ke pool; buffer yang lebih besar dilepas ke Garbage Collector untuk menghindari heap bloat yang persisten.
- **Fast Zero-Allocation JSON Decoding (`gjson`):** Mengekstrak field routing (`model` dan `stream`) langsung dari byte slice mentah menggunakan `tidwall/gjson`, melewati alokasi AST dan pembuatan `map[string]any`. Menghemat >90% alokasi heap pada payload besar.
- **Resolusi Target & Fallback:** Menyelesaikan pemetaan model-ke-upstream. Jika Circuit Breaker upstream primer berstatus `OPEN`, Firefly otomatis mengalihkan trafik ke upstream fallback yang dikonfigurasi.
- **Seleksi KeyRing Lock-Free:** Memilih API key aktif menggunakan strategi `least-inflight` atau `round-robin`. Key yang sedang cooldown (akibat 429 upstream) atau dicabut (akibat 401 upstream) dilewati secara lock-free.
- **Gate Konkurensi Kredensial:** Membatasi request konkuren per API key upstream menggunakan counter atomik CAS (`Limiter.AcquireKeySlot`). Key yang jenuh mengembalikan `HTTP 429`.

### 6. Pooling Koneksi Upstream (`upstream.Pool`)
- Request keluar memakai instance `*http.Client` singleton yang di-pool per host upstream.
- **Konfigurasi Transport Skala Tinggi:**
  - `MaxIdleConns: 2000`, `MaxIdleConnsPerHost: 1000`, `MaxConnsPerHost: 1500`.
  - `IdleConnTimeout: 90s`, `ForceAttemptHTTP2: true` untuk multiplexing koneksi HTTP/2.
  - `ResponseHeaderTimeout: 30s`: Timeout Time-To-First-Byte (TTFB) tanpa memutus stream token yang berjalan lama.
  - `http.Client.Timeout = 0`: Sengaja tak dibatasi agar completion LLM yang berlangsung bermenit-menit dapat streaming tanpa interupsi dini.

### 7. Penanganan Respons Upstream & Mesin Streaming (`openai.RelaySSE`)
- **JSON Non-Streaming:** Byte respons di-buffer hingga 32 MB dan diteruskan langsung ke klien.
- **Streaming SSE (Server-Sent Events):**
  - Mengirim header segera: `text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`.
  - Mem-flush token segera menggunakan interface `http.Flusher`.
  - **`StreamWatchdog`:** Terus memantau aliran data dari upstream. Jika upstream berhenti mengirim byte melewati `stream_idle_timeout_ms` yang dikonfigurasi, watchdog menutup paksa socket untuk mencegah goroutine yang menggantung.
  - **Client Disconnect Abort:** Mendengarkan `ctx.Done()`. Jika klien terputus atau membatalkan request, socket upstream ditutup seketika, menghentikan generasi token upstream lanjutan beserta penagihannya.

### 8. Teardown & Observabilitas
- Tiket konkurensi in-flight dilepas via `defer releaseCred()` dan `defer releaseTenant()`.
- Metrik latensi, kode status, dan saturasi dikirim ke Prometheus.
- Konsumsi token dicatat ke `UsageRecorder`.
- Buffer memori dibersihkan dan dikembalikan ke `sync.Pool`.

---

## Arsitektur Ketahanan Berlapis

Firefly memisahkan secara ketat kegagalan kredensial/kuota dari kegagalan infrastruktur host:

```
                       [Incoming Request]
                               │
               ┌───────────────┴───────────────┐
               ▼                               ▼
       [Layer 1: KeyRing]             [Layer 2: Breaker]
  (Credential & Quota Handling)   (Host Infrastructure Health)
  ─────────────────────────────   ────────────────────────────
  - Target: Individual API Keys   - Target: Upstream host (5xx/TCP)
  - HTTP 429: Dynamic cooldown    - Trip: 5 consecutive failures
    (reads Retry-After header)    - Cooldown: 10 seconds in StateOpen
  - HTTP 401: Key revocation      - State: Closed -> Open -> HalfOpen
  - Failover: Switch to next      - Failover: Reroute to backup
    key in keyring instantly        upstream when breaker is OPEN
```

| Dimensi | Layer 1: KeyRing (Kredensial & Kuota) | Layer 2: Circuit Breaker (Infrastruktur Host) |
| :--- | :--- | :--- |
| **Cakupan Target** | API Key individual (`KeySlot`) | Seluruh server host upstream |
| **Pemicu Error** | HTTP `429 Too Many Requests` & `401 Unauthorized` | HTTP `5xx` & Error Transport (TCP Reset, Dial Timeout) |
| **Aksi 429** | Cooldown dinamis dari `Retry-After` (RFC 7231, default 30 detik) | Tidak pernah men-trip atau memengaruhi Circuit Breaker |
| **Aksi 401** | Pencabutan permanen key slot hingga reload konfigurasi berikutnya | Tidak pernah men-trip atau memengaruhi Circuit Breaker |
| **Mekanisme Failover** | Mencoba ulang secara transparan dengan key berikutnya di keyring | Mengalihkan request ke upstream fallback bila breaker `OPEN` |
| **Ambang Trip** | Tidak berlaku | 5 kegagalan berturut-turut memicu `StateOpen` (cooldown 10 detik) |

Klasifikasi ini ditegakkan di satu tempat — `upstream.ProcessAttemptOutcome` — yang dipanggil setiap adapter setelah tiap percobaan, sehingga error kredensial/kuota tidak pernah men-trip circuit breaker host.

### Health Checking Aktif (`golang.org/x/sync/errgroup`)
Selain klasifikasi error inline yang pasif, Firefly menjalankan [`HealthChecker`](internal/upstream/health.go) latar belakang yang aktif:
- Memeriksa endpoint upstream secara berkala menggunakan `errgroup.WithContext` dengan konkurensi terbatas (`SetLimit(10)`).
- Menghormati cooldown Circuit Breaker dan memicu validasi `StateHalfOpen` dengan aman.
- Mengklasifikasikan kode status `< 500` (termasuk 401/404) sebagai transport host yang sehat (Layer 2), mencegah error kredensial key menyebabkan gangguan infrastruktur palsu.
- Terverifikasi bebas kebocoran goroutine saat shutdown server via `goleak`.

---

## Adapter Multi-Protokol & Integrasi OAuth Native

Firefly menyediakan kontrak interface yang bersih (`ports.UpstreamAdapter`) untuk routing multi-provider dan autentikasi pihak ketiga native:
- **OpenAI Adapter (`internal/openai`):** Proxy nyaris transparan untuk OpenAI, Azure OpenAI, vLLM, dan Ollama. Menulis ulang identifier model publik ke nama privat provider, membersihkan header hop-by-hop, dan menyuntikkan rahasia upstream dari environment variable atau penyimpanan keyring. Juga membuang header `Accept-Encoding` klien lalu mendekompresi respons gzip/deflate secara transparan (`decodeResponseBody`) sehingga SSE/JSON terkompresi tidak pernah diteruskan sebagai byte mentah.
- **Anthropic Claude Adapter (`internal/anthropic`):** Menyediakan translasi skema dua arah yang transparan antara OpenAI Chat Completions dan Anthropic Messages API (`/v1/messages`), mendukung payload non-streaming maupun chunk streaming SSE.
- **Google Antigravity Cloud Code Adapter (`internal/antigravity`):** Menerjemahkan request inferensi OpenAI yang masuk ke protokol internal Google Cloud Code dengan pengikatan Google Cloud companion project ID autentik, mendukung model inferensi Gemini 3.8/3.7 Flash tiers, Gemini 2.5 Pro/Flash, dan Claude 3.7/4.6 Sonnet dengan onboarding OAuth non-blocking.
- **Cline OAuth Adapter (`internal/cline`):** Mem-proxy completion ke endpoint API Cline (`api.cline.bot`), otomatis melampirkan header identifikasi klien (`HTTP-Referer`, `X-Title`), membuka envelope payload, dan me-relay stream SSE.
- **CodeBuddy China & International Adapter (`internal/codebuddy`):** Mengelola device authorization grant RFC 8628, penulisan ulang payload, dan streaming completion untuk endpoint CodeBuddy China dan International.
- **Grok CLI / Grok Build Adapter (`internal/grok`):** Merutekan request ke inference API xAI Grok CLI (`cli-chat-proxy.grok.com`, OpenAI Responses API) menggunakan bearer token xAI OAuth, menerjemahkan OpenAI Chat Completions ke/dari format wire Responses (termasuk output penalaran/thinking) dan mem-pool token akun yang dikumpulkan di seluruh key ring.

---

## OAuth AI Pihak Ketiga & Rotasi Multi-Akun

Firefly mengintegrasikan alur OAuth 2.0 authorization code dan device code secara native langsung di dalam gateway:
- **Tanpa Restart Environment Variable:** Hubungkan akun secara interaktif dari dashboard web. Token OAuth dan siklus refresh-nya dipersist di penyimpanan backend terenkripsi (`configs/oauth.json`).
- **Keyring Upstream Multi-Akun:** Ikat beberapa akun pihak ketiga (mis. 2+ akun Cline atau Google) ke satu host upstream. Firefly mem-pool dan merotasi request di seluruh slot akun menggunakan strategi `least_inflight` atau `round_robin`, otomatis failover jika salah satu akun kena rate limit (`HTTP 429`).
- **Resolusi Token Dinamis:** Referensi berformat `oauth:<connection_id>` me-resolve bearer token yang valid secara dinamis pada setiap request keluar, secara proaktif me-refresh token sebelum kedaluwarsa.

---

## Verifikasi Model Aktif & Health Probing

Sebelum mengekspos rute model ke aplikasi klien, Firefly memungkinkan verifikasi model aktif langsung dari dialog **Create Model Route / Edit Route**:
- **Ping Inferensi Minimal:** Mengirim pemeriksaan inferensi minimal (`max_tokens: 1`) ke host upstream (`POST /api/upstreams/check` dengan `model`).
- **Klasifikasi Status Langsung:**
  - **HTTP 200 (Sukses):** Mengonfirmasi konektivitas model, mengembalikan latensi TTFB dalam milidetik, dan memverifikasi otorisasi.
  - **HTTP 401 / 403 (Auth Ditolak):** Merinci masalah izin atau kredensial kedaluwarsa.
  - **HTTP 404 (Tidak Ditemukan):** Mendeteksi identifier model salah ketik atau model tidak tersedia pada tier provider.
  - **HTTP 429 (Kuota Habis):** Mengidentifikasi habisnya akun upstream sebelum mengirim trafik pengguna.
- **Zero Secret Leakage:** API key upstream dan token OAuth dievaluasi di sisi server dan tidak pernah diekspos dalam payload klien.

## Virtual Combos (Load Balancing Level Model)

Virtual Combos mengagregasi beberapa model dari upstream berbeda ke dalam satu rute model virtual:
- **Strategi Load Balancing**:
  - `least_inflight`: Mengarahkan ke model kandidat yang sedang memproses request konkuren paling sedikit.
  - `round_robin`: Bergilir merata di antara model kandidat secara sirkular.
  - `failover`: Rantai berurutan yang mencoba model primer lebih dulu, jatuh ke model berikutnya saat terjadi kegagalan transport.
- **Distribusi Upstream Terpadu**: Model anggota dapat menunjuk ke provider AI berbeda, mendistribusikan beban secara alami di infrastruktur yang berbeda tanpa provider lock-in.
- **Skema Konfigurasi (`combos.json`)**:
  ```json
  {
    "combos": [
      {
        "name": "smart-router",
        "models": ["gpt-4o-main", "claude-3-5-sonnet", "deepseek-chat"],
        "strategy": "least_inflight",
        "enabled": true
      }
    ]
  }
  ```

---

## Penyimpanan Terpusat Turso (Opsional)

Firefly dapat mem-backing routing catalog dan settings-nya dengan penyimpanan Turso/libSQL opsional (`internal/turso`) untuk sinkronisasi terpusat antar-node:
- **Persistensi Katalog & Settings:** `SaveSettings`/`LoadSettings` menyimpan upstream, model, tenant, dan combo di database terpusat.
- **Penghapusan Aman:** Penghapusan digerbangi oleh flag otoritatif (`manage_upstreams`, `manage_models`, `manage_combos`, `manage_tenants`) sehingga save parsial atau usang tidak pernah menghapus katalog secara tidak sengaja.
- **Harvester Provider/Key & Usage Flusher:** Memanen key provider aktif dan mempersist aksi siklus hidup key otomatis (deactivate/delete) ke database.

---

## Dashboard Web Tertanam & Keamanan Navigasi

Firefly hadir dengan Single Page Application React 19 bertema nokturnal yang tertanam langsung di dalam binary standalone (`//go:embed all:dist`):
- **Responsif Mobile & Desktop**: Navigasi bersih dan terpadu di semua viewport dengan menu burger mobile bioluminesen nokturnal, stabilitas viewport adaptif (`min-h-[100dvh]`), dan kontrol ramah sentuh.
- **Overview Publik**: Denyut trafik real-time, fisika particle canvas, grafik latensi, penghitung koneksi aktif, dan pemutar lo-fi prosedural ambient.
- **Navigasi Rute Terproteksi**: Mengakses modul konfigurasi dan diagnostik (*Upstreams, Models, Combos, Tenants, Telemetry, Settings, Playground*) digerbangi autentikasi session token backend (default: `12345678`).
- **Vault Kredensial Backend**: Password dan session token disimpan serta diverifikasi khusus di backend (`configs/auth.json`), menghilangkan penyimpanan kredensial sensitif di `localStorage` browser.
- **Playground LLM di Browser**: Menguji endpoint model, memeriksa waterfall token streaming, menampilkan jejak "thinking"/reasoning model (`delta.reasoning_content`) dalam panel yang dapat dilipat, dan meninjau diagnostik paket SSE mentah.

---

## Instalasi Produksi Satu Baris

Instal dan jalankan Firefly sebagai layanan sistem latar belakang otomatis di server Linux mana pun (Debian, Ubuntu, Fedora, CentOS, RHEL, Rocky, Arch, openSUSE, Alpine):

```bash
curl -fsSL https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.sh | bash
```

Setelah terpasang, Firefly langsung berjalan sebagai daemon latar belakang `systemd` terkelola:
- **Dashboard Web UI**: `http://<SERVER_IP>:8080` (Password akses default: `12345678`)
- **Permukaan API OpenAI**: `http://<SERVER_IP>:8080/v1/chat/completions`
- **Direktori Konfigurasi**: `/etc/firefly/`
- **Manajemen Layanan**:
  ```bash
  sudo systemctl status firefly    # Cek status latar belakang
  sudo systemctl restart firefly   # Restart daemon gateway
  sudo journalctl -u firefly -f    # Streaming log real-time
  ```

---

## Mulai Cepat (Build Manual)

### Prasyarat
- Go 1.22 atau lebih tinggi.
- Bun 1.0+ atau Node.js 18+ (opsional, hanya diperlukan bila membangun ulang frontend React yang tertanam).

### Membangun & Menjalankan
```bash
# 1. Build single-binary terpadu dengan frontend tertanam
make build

# 2. Jalankan Firefly dengan data plane dan admin plane
./firefly \
  -config-dir=configs \
  -addr=0.0.0.0:8080 \
  -admin-addr=:9090 \
  -admin-token="your-admin-token" \
  -shutdown-grace-seconds=30
```

### Flag Perintah CLI & Environment Variable
- `-config-dir` / `FIREFLY_CONFIG_DIR`: Direktori berisi berkas konfigurasi (`upstreams.json`, `models.json`, `tenants.json`, `combos.json`). Default: `configs`. Bila kosong, Firefly boot dalam mode zero-config dan auto-generate berkas template.
- `-addr` / `FIREFLY_ADDR`: Alamat listen data plane untuk trafik AI masuk. Default: `0.0.0.0:8080`.
- `-admin-addr` / `FIREFLY_ADMIN_ADDR`: Alamat listen admin plane untuk `/metrics` dan `/api/*`. Dinonaktifkan bila kosong. Default: `""`.
- `-admin-token` / `FIREFLY_ADMIN_TOKEN`: Bearer token yang melindungi endpoint administratif.
- `-log-level` / `FIREFLY_LOG_LEVEL`: Level logging terstruktur (`debug`, `info`, `warn`, `error`). Default: `info`.
- `-health-check-interval`: Frekuensi probe health latar belakang (durasi, mis. `15s`; `0` menonaktifkan). Default: `15s`.
- `-shutdown-grace-seconds` / `FIREFLY_SHUTDOWN_GRACE_SECONDS`: Waktu maksimum yang diberikan untuk stream SSE aktif menyelesaikan diri saat shutdown. Default: `30`.
- `-version`: Cetak informasi versi, commit hash, dan build timestamp, lalu keluar.

### Auto-TLS Native (Let’s Encrypt)

Auto-TLS dinonaktifkan secara default dan dikonfigurasi dari **Settings → Native Let's Encrypt Auto-TLS**. Aktifkan dengan satu hostname publik dan email notifikasi ACME; Firefly mempersist setelan di `configs/tls.json` serta cache sertifikat/key-nya di `configs/certificates/` (mode `0700`).

Saat aktif, Firefly memulai listener HTTP pada `:80` untuk tantangan HTTP-01 dan mengalihkan request HTTP lain yang cocok ke HTTPS, lalu menyajikan handler Firefly normal pada `:443`. DNS untuk hostname yang tepat harus resolve ke server dan port TCP 80 serta 443 harus terjangkau dan tidak terpakai. Alamat IP, `localhost`, domain wildcard, dan nilai URL ditolak; sertifikat wildcard memerlukan DNS-01 dan tidak didukung oleh mode native ini. Listener HTTP `-addr` yang ada tetap tidak berubah demi kompatibilitas mundur.

---

## Pengujian & Validasi Performa

```bash
# Jalankan seluruh suite unit & integration test dengan Go race detector aktif
go test -count=1 -race ./...

# Jalankan test deteksi kebocoran goroutine (goleak)
go test -v -race -run Test.*Leak ./...

# Jalankan benchmark alokasi memori
go test -benchmem -bench=. ./internal/server/...
```

---

## Load Testing Berkonkurensi Tinggi (100 & 1.000 Request Bersamaan)

Firefly menyertakan alat CLI benchmark standalone yang terpisah di `cmd/loadtest` dengan synchronized barrier concurrency:

```bash
# Build alat load test standalone
make build-loadtest

# Jalankan 100 request bersamaan terhadap mock in-process (profil beban rendah)
./bin/loadtest -mock -c 100 -profile low

# Jalankan 1.000 request bersamaan terhadap mock in-process (profil beban rendah)
./bin/loadtest -mock -c 1000 -profile low

# Jalankan suite benchmark 6-skenario otomatis penuh (100 & 1.000 request lintas low/med/heavy)
./bin/loadtest -mock -suite
```

Untuk profil beban lengkap, analisis metrik, dan flag CLI, lihat [Panduan Load Testing & Benchmark](./docs/loadtest.md).

---

## Dokumentasi & Referensi

- [Versi Bahasa Inggris (English README)](./README.md)
- [Panduan Deployment & Hardening Produksi](./docs/production.md)
- [Panduan Load Testing & Benchmark](./docs/loadtest.md)
- [Arsitektur & Panduan AI Agent](./AGENTS.md)
- [Sistem Desain & Spesifikasi Frontend](./DESIGN.md)
- [Panduan Kontribusi](./CONTRIBUTING.md)
- [Changelog Rilis](./CHANGELOG.md)
- [Lisensi (MIT)](./LICENSE)
- [Ikhtisar LLM](./llms.txt)
