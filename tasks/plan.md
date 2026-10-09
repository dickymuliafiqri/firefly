# Implementation Plan: Tier-1 Roadmap — `/v1/messages` Ingress, Cost Analytics & Budget Guard, Notification Alerting

> **Status**: Planning — awaiting human approval before implementation.
> **Scope**: Roadmap gabungan 3 fitur Tier-1 (hasil analisis project), dieksekusi berurutan per fase.
> **Target versions**: `1.44.0` (P0), `1.45.0` (P1), `1.46.0` (P2).
> **Sebelumnya**: Rencana MCP Server (terarsip di `tasks/archive/`) — sudah tereks implementasi penuh (`internal/mcp/` ada dan seluruh testnya hijau), checkbox-nya hanya tidak pernah dicentang.

## Overview

Tiga fitur yang saling memperkuat positioning Firefly sebagai gateway AI monetizable:

1. **P0 — Inbound `/v1/messages` (Anthropic surface)**: menerima payload Anthropic Messages API secara native sehingga klien hard-routed seperti Claude Code / Claude Desktop / SDK Anthropic bisa memakai Firefly tanpa adapter eksternal. Traffic diterjemahkan ke format kanonik internal (OpenAI) yang sudah didukung seluruh pipeline (KeyRing, breaker, limits, Token Saver, usage), lalu respons diterjemahkan balik ke wire-format Anthropic.
2. **P1 — Cost Analytics + Budget Guard**: tabel harga per-model (input/output/cache per 1M token, konfigurable & hot-reload) yang di-*seed* dari katalog publik **models.dev** (`https://models.dev/api.json`, mode seed-once — importer mengisi entri yang belum ada, override operator tidak pernah ditimpa), standardisasi perhitungan biaya (menggantikan 5 titik hardcoded `tokensIn * 0.0000025` di `handlers.go`) termasuk penagihan token cache (`cached_tokens` OpenAI / `cache_read_input_tokens` + `cache_creation_input_tokens` Anthropic yang kini ditangkap dari respons upstream), agregasi biaya per tenant/key/model/hari, hard budget cap per-tenant (tolak dengan HTTP 429 + `Retry-After` saat limit terlampaui) + soft warning di 80%, halaman dashboard **Pricing** dedicated (telusuri katalog models.dev, daftar model + harga, override manual), dan filter status pricing di halaman Models.
3. **P2 — Notification & Webhook Alerting**: dispatcher webhook generik + formatter Discord/Slack/Telegram untuk event operasional kritis (`key.revoked`, `breaker.open`, `key.threshold_action`, `tenant.budget_exceeded`, `upstream.health_failed`), dikonfigurasi lewat settings + dashboard, dengan tombol test-send.

## Architecture Decisions

### Keputusan global

- **A1 — Format kanonik internal tetap OpenAI; `/v1/messages` adalah permukaan ingress translation.** Tidak ada adapter yang berubah. `TranslateAnthropicToOpenAI` (sudah ada) dipakai untuk request masuk; translator arah balik (respons OpenAI → Anthropic, JSON + SSE chunk) adalah kode baru murni di `internal/adapter/anthropic/`. Dengan ini KeyRing, breaker, admission, Token Saver, dan usage metering bekerja seragam untuk kedua surface.
- **A2 — Uang disimpan sebagai integer micro-USD (`atomic.Int64`)**, bukan float — menghindari drift akumulasi dan race; konversi ke USD hanya di lapisan presentasi/API.
- **A3 — Tabel harga dimiliki konfigurasi** (JSON settings + Turso settings payload), ikut hot-reload snapshot yang sudah ada; lookup exact-match → wildcard prefix terpanjang → nol. Isi awal tabel datang dari importer models.dev (A7); operator selalu bisa menambah/mengoverride entri manual yang menang atas seed.
- **A4 — Notifikasi tidak pernah block hot path request.** Dispatch lewat goroutine dengan bounded queue; saat penuh: drop + counter (terlihat di telemetry), bukan backpressure ke data plane.
- **A5 — Tanpa dependensi pihak ketiga baru.** Webhook dikirim via `net/http` bawaan; formatter Discord/Slack/Telegram di-hand-roll (pola yang sama dengan keputusan MCP: `go.mod` sengaja ramping).
- **A6 — Backward-compatible:** saat tabel harga kosong, estimasi biaya mempertahankan flat-rate lama (`0.0000025/token`) agar dash/history tidak melompat tanpa penjelasan.
- **A7 — models.dev adalah seeder, bukan source of truth runtime.** Katalog 5.3 MB tidak di-fetch per request. Importer (tombol dashboard atau trigger manual) mengambil `api.json`, mem-parse subset `cost`/`limit`/`canonical_model_id`, menulisnya ke tabel harga lokal (`configs/pricing.json` + settings payload + Turso). Mode **seed-once**: entri yang sudah ada di tabel lokal tidak pernah ditimpa (override operator menang); entri baru dari models.dev bertanda `source: "models.dev"`, entri buatan operator bertanda `source: "manual"`. Lookup biaya selalu memakai tabel lokal — models.dev bukan dependensi runtime (gateway tetap berfungsi penuh kalau models.dev mati). Mapping protocol Firefly → provider models.dev memakai tabel config dengan default: `openai`→`openai`, `anthropic`→`anthropic`, `grok-cli`→`xai`, `opencode`→`opencode`, `opencode-go`→`opencode-go`; `antigravity` melayani Gemini DAN Claude sehingga resolusi harga per-model memakai `canonical_model_id` (`anthropic/claude-*` → harga anthropic, `google/gemini-*` → harga google); `cline`/`codebuddy-*`/`qoder` tidak ada di models.dev → harga manual/zero.
- **A8 — Token cache ditangkap dan ditagih terpisah.** `parseUsageFromBytes` diperluas mengekstrak `usage.prompt_tokens_details.cached_tokens` (OpenAI) serta `usage.cache_read_input_tokens` + `usage.cache_creation_input_tokens` (Anthropic), termasuk dari chunk usage final pada SSE. Normalisasi billable: `prompt_tokens` OpenAI sudah termasuk cached → uncached = `prompt_tokens - cached_tokens`; `input_tokens` Anthropic sudah exclude. Biaya = uncached×harga input + cached×harga `cache_read` + cache-write×harga `cache_write` + output×harga output. Harga cache tidak ada di entri → fallback ke harga input (konservatif, tidak under-bill). Field baru `CachedReadTokens`/`CacheWriteTokens` di `RequestLog`/`TokenLedger` ikut dipersistensi ke Turso sebagai metering murni (tidak bump `updated_at`).
- **A9 — Budget terlampaui → HTTP 429** (bukan 402): konsisten dengan surface admission/rate-limit yang sudah ada, klien sudah punya semantics retry/backoff untuk 429, dan header `Retry-After` memberi sinyal kapan mencoba lagi. Pesan error eksplisit "tenant budget exceeded"; counter spend tidak berubah karena request ditolak.
- **A10 — Halaman Pricing dedicated + filter di Models.** Halaman baru `pricing` (group CONFIGURATION): tabel seluruh entri harga lokal (model key, provider, harga input/output/cache per 1M, badge source, indikator terpakai oleh catalog Firefly), filter provider/source/status + pencarian, drawer edit harga, tombol *Import from models.dev* (seed-once dengan preview sebelum commit) dan *Refresh catalog*, serta panel browse katalog models.dev yang di-cache in-memory. Halaman Models mendapat filter "Pricing: all / registered / not registered", kolom harga ringkas, dan aksi cepat *Set price*.

### Keputusan per fitur

- **P0**: Route `POST /v1/messages` (plus OPTIONS preflight) berada di belakang `deps.protected` yang sama dengan `/v1/chat/completions` — auth tenant, admission, global limiter, drain guard identik. Validasi ingress mengikuti spec Anthropic: `model` dan `max_tokens` wajib → error memakai envelope `type: "error", error: {type, message}` agar klien Anthropic merender pesannya benar.
- **P1**: Budget guard dievaluasi pre-flight di `forwardEndpoint` (setelah resolusi tenant, sebelum adapter — satu atomic load, murah) dan akumulasi biaya terjadi di titik yang sama dengan `UsedTokens.Add` (`handlers.go:684`) sehingga tidak ada jalur pencatatan ganda. Reset budget masuk ke endpoint `/api/tenants/topup` yang sudah ada. Penolakan memakai HTTP 429 + `Retry-After` (A9).
- **P2**: Sumber event difinstrumentasi di dua titik yang sudah terkonsentrasi: `upstream.HandleKeyOutcome` (`internal/transport/upstream/policy.go`) untuk event Layer-1, dan transisi state di `internal/transport/upstream/breaker.go` untuk event Layer-2. Dedupe flapping breaker dengan minimum-interval per event. URL webhook di-dial dengan guard anti-SSRF (blokir link-local/metadata `169.254.169.254`), dan tanda tangan HMAC-SHA256 (`X-Firefly-Signature`) untuk consumer yang ingin memverifikasi.

## Integration Points (verified in code)

| Concern | Existing code | Pemakaian |
|---|---|---|
| Routing data-plane | `internal/server/router.go:296-307` (`v1Routes` + `deps.protected`) | Tambah `POST /v1/messages` + OPTIONS |
| Forward pipeline | `server.forwardEndpoint` (`internal/server/handlers.go`) | Branch ingress: deteksi route `/v1/messages`, translasi Anthropic→OpenAI, lanjut pipeline yang sama |
| Translasi request | `anthropic.TranslateAnthropicToOpenAI` (`translate.go:176`) | Reuse langsung untuk body masuk |
| Translasi respons (BARU) | — | `TranslateOpenAIToAnthropicResponse` + `TranslateOpenAIChunkToAnthropic` (Task 1) |
| Error envelope | `anthropic.TranslateAnthropicError` (`translate.go:389`) | Reuse + tambah pemetaan error internal → envelope Anthropic |
| Harga (hari ini) | 5 titik `EstimatedCost: float64(tokensIn) * 0.0000025` di `handlers.go:396,436,519,620,710` | Diganti lookup `PricingTable` (Task 5) |
| Ledger biaya | `analytics.RequestLog.EstimatedCost`, `analytics.TokenLedger.EstimatedCostUSD`, telemetry `estimated_cost_usd` | Konsumen hasil standardisasi; agregasi baru di Task 7 |
| Kuota token tenant (pola acuan) | `domain.Tenant.MaxTokens` / `UsedTokens *atomic.Int64`, cek & `Add` di `handlers.go:684`, reset di `server/tenant_topup.go:77` | Pola yang ditiru untuk `BudgetUSD` / `SpentUSD` (Task 6) |
| Event Layer-1 | `upstream.HandleKeyOutcome` (`internal/transport/upstream/policy.go:27`) | Emit `key.*` events (Task 9) |
| Event Layer-2 | Transisi state `internal/transport/upstream/breaker.go` (`StateOpen`/`StateClosed`/`StateHalfOpen`) | Emit `breaker.*` events (Task 9) |
| Settings persistence | `internal/server/settings.go` + turso settings payload + `PinOAuthManagedEndpoints` normalize-before-persist | Section `pricing` dan `notifications` mengikuti pola yang sama |
| Dashboard | `frontend/src/pages/SettingsPage.tsx`, `UsagePage.tsx` (pola halaman + api client) | Section Budget/Notifications (Task 8, 14) |
| models.dev catalog | `https://models.dev/api.json` (eksternal, 5.3 MB, 226 provider) | Di-fetch importer (`internal/pricing/modelsdev.go`), di-parse ke subset, di-cache in-memory untuk browsing dashboard (Task 5) |
| Cached tokens (hari ini) | `parseUsageFromBytes` (`internal/server/handlers.go:97`) hanya ambil prompt/completion | Diperluas ambil cached read/write; `RequestLog`/`TokenLedger` dapat field baru (Task 6) |
| Dashboard pages | `frontend/src/registry.tsx` (PAGES map), `ModelsPage.tsx`, `SettingsPage.tsx` | Halaman `pricing` baru + filter/kolom harga di ModelsPage (Task 10, 11) |

## Delivery Phases (dependency-ordered, vertical slices)

- **Phase P0 (v1.44.0)** — Task 1 → 2 → 3: translator respons Anthropic → route ingress → kompatibilitas & docs. Tiap task meninggalkan sistem tetap hijau.
- **Phase P1 (v1.45.0)** — Task 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11: pricing catalog → models.dev importer (seed-once) → cached-token capture → standardisasi perhitungan → budget guard (429) → API agregasi → halaman Pricing dashboard → filter pricing di Models. Task 4–7 **independen dari P0** (bisa dikerjakan paralel oleh agent/session lain; koordinasikan `handlers.go` di Task 7 vs Task 2 — kerjakan berurutan bila satu agent).
- **Phase P2 (v1.46.0)** — Task 8 → 9 → 10: notifier core → instrumentasi event → API + dashboard + test-send. Task 8 independen; Task 9 butuh hook budget dari Task 6.
- **Task 11** — docs, OpenAPI spec, CHANGELOG, verifikasi penuh (ditutup checkpoint akhir).

Detail task + acceptance criteria: lihat `tasks/todo.md` (sumber kebenaran eksekusi; dokumen ini adalah konteks & keputusan).

## Verification Commands (repo baseline, sudah terbukti hijau sebelum perubahan)

```bash
go build ./...
go vet ./...
go test -count=1 -race ./...          # baseline: 39 paket, 0 gagal
cd frontend && npm run build          # tsc + vite, tanpa eslint (kondisi saat ini)
```

Command fokus per task tercantum di masing-masing item `tasks/todo.md`.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Urutan event SSE Anthropic salah (message_start → content_block_* → message_delta → message_stop) membuat Claude Code gagal render | High | Golden-file test dari stream Anthropic asli yang di-capture; e2e Task 3 sebagai gate sebelum lanjut fase |
| Interaksi urutan rewrite di `forwardEndpoint`: Token Saver → translasi → budget guard pada jalur baru | Medium | Test matriks integrasi (stream × protokol × token-saver on/off); urutan didokumentasikan di kode |
| Drift akumulasi biaya float | Medium | Integer micro-USD atomics (A2); test konkurensi parallel request |
| Alert storm (breaker flapping, burst 429) membanjiri channel | Medium | Dedupe minimum-interval per event type, bounded queue + drop counter (A4) |
| SSRF via URL webhook operator | Medium | Hanya dashboard-admin yang boleh mengatur; dialer memblokir link-local/metadata IP; HMAC signature untuk verifikasi consumer |
| Regresi di file besar (`handlers.go`, `settings.go`, `turso/store.go`) | Medium | Edit kecil bertahap + test fokus setelah tiap task; tanpa refactor drive-by |
| Dispatch notifikasi memblock hot path | High | Async bounded queue saja — invariant, tidak ada dispatch inline di request path |

## Open Questions (assumption dipakai sampai dikonfirmasi)

1. Status HTTP saat budget tenant terlampaui: **429 + `Retry-After`** (keputusan LO, A9).
2. `/v1/messages` dihitung dalam RPS/in-flight tenant yang sama dengan `/v1/chat/completions`? Asumsi: **ya, identik**.
3. Sumber tabel harga: **importer models.dev mode seed-once** (keputusan LO, A7) + override manual via dashboard; importer mengisi entri baru, override operator tidak pernah ditimpa.
4. Header `anthropic-version` pada traffic passthrough: pin `2023-06-01` atau forward milik klien? Asumsi: **forward jika ada, pin jika tidak ada**.
5. Auth webhook generik: cukup HMAC signature + optional static bearer? Asumsi: **ya**.
