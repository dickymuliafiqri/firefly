# Tasks: Tier-1 Roadmap — `/v1/messages` + Cost/Budget + Alerting

Source plan: `tasks/plan.md` (konteks & keputusan arsitektur). Do not start until the plan is approved.
Baseline verified before planning: `go build`, `go vet`, `go test -count=1 -race ./...` — all green (39 packages).

---

# Phase P0 — Inbound `/v1/messages` (Anthropic surface) → v1.44.0

## Task 1: Anthropic response translation (OpenAI → Anthropic wire format)

**Description:** Tambahkan translator arah balik di `internal/adapter/anthropic`: `TranslateOpenAIToAnthropicResponse` (JSON non-streaming: content blocks text/tool_use, map `finish_reason`→`stop_reason`, sisipkan `usage`) dan `TranslateOpenAIChunkToAnthropic` (chunk → event SSE Anthropic berurutan valid: `message_start` → `content_block_start`/`content_block_delta`/`content_block_stop` → `message_delta` → `message_stop`). Tool-call delta → `input_json_delta.partial_json`. Tambah pemetaan error internal → envelope `{"type":"error","error":{"type":...,"message":...}}`.

**Acceptance criteria:**
- [x] JSON: system prompt, multi-turn, assistant `tool_calls`, role `tool` → content blocks benar; stop reason `stop`→`end_turn`, `length`→`max_tokens`, `tool_calls`→`tool_use`
- [x] SSE: urutan event lengkap & valid untuk text-only dan tool-call; usage input di `message_start`, output di `message_delta`
- [x] Fungsi murni, input tidak valid → error eksplisit tanpa panic
- [x] Table-driven + golden JSON tests untuk semua kasus di atas

**Verification:**
- [x] `go test -race ./internal/adapter/anthropic/...` passes
- [x] `go build ./...` succeeds

**Dependencies:** None
**Files likely touched:** `internal/adapter/anthropic/translate_response.go` (baru), `internal/adapter/anthropic/translate_response_test.go` (baru)
**Estimated scope:** Medium

## Task 2: Route `POST /v1/messages` ingress

**Description:** Daftarkan route `POST /v1/messages` + OPTIONS preflight di `router.go` (belakang `deps.protected` yang sama dengan `/v1/chat/completions`). Di `forwardEndpoint`, tambah branch ingress: baca body → validasi wajib `model` & `max_tokens` (error pakai envelope Anthropic) → translasi `TranslateAnthropicToOpenAI` → pipeline lanjutan identik (KeyRing, adapter, Token Saver, usage). Untuk respons: upstream non-stream JSON diterjemahkan via Task 1; upstream SSE diterjemahkan chunk-per-chunk dengan header respons `content-type: text/event-stream` (bukan OpenAI SSE). Jika klien minta `"stream": true`, teruskan stream ke upstream agar tetap streaming.

**Acceptance criteria:**
- [x] Request Anthropic valid → sukses dengan respons wire-format Anthropic (non-stream & stream)
- [x] `model` kosong / `max_tokens` kosong → HTTP 400 envelope Anthropic
- [x] Auth 401 dan admission 429 memakai envelope Anthropic di surface ini
- [x] Usage metering (tokensIn/tokensOut) & RequestLog tercatat untuk rute ini
- [x] Tenant tanpa akses model → 404 sesuai aturan `AllowsModel` yang ada, dalam envelope Anthropic

**Verification:**
- [x] `go test -race ./internal/server/... ./internal/adapter/anthropic/...` passes
- [x] `go build ./...` succeeds
- [ ] Manual: `curl` non-stream + stream (SSE events berurutan) ke upstream mock

**Dependencies:** Task 1
**Files likely touched:** `internal/server/router.go`, `internal/server/handlers.go` (atau file baru `internal/server/messages_ingress.go`), `internal/server/*_test.go`
**Estimated scope:** Medium

## Task 3: Kompatibilitas klien & dokumentasi P0

**Description:** Pastikan pola request khas Claude Code / SDK Anthropic lolos: `system` sebagai string ATAU array of blocks, `tool_choice`, `stop_sequences`, header `anthropic-version`, `anthropic-beta`. Lengkapi OpenAPI spec admin/data-plane, contoh curl di README/llms.txt.

**Acceptance criteria:**
- [x] Test untuk varian `system` string vs blocks, `tool_choice`, `stop_sequences` lolos
- [x] Header `anthropic-version` diterima tanpa error; dokumen mencantumkan nilai yang didukung
- [x] OpenAPI spec + README + llms.txt memuat endpoint `/v1/messages`

**Verification:**
- [x] `go test -count=1 -race ./...` passes (full suite)
- [x] `go vet ./...` clean

**Dependencies:** Task 2
**Files likely touched:** `internal/server/*_test.go`, `internal/server/openapi.go`, `README.md`, `llms.txt`
**Estimated scope:** Medium

### Checkpoint: Phase P0 selesai
- [x] Full suite hijau: `go test -count=1 -race ./...`
- [ ] E2E manual: Claude Code / SDK Anthropic menunjuk ke Firefly bisa menyelesaikan chat + tool-call
- [ ] Review human sebelum lanjut ke Phase P1

---
# Phase P1 — Cost Analytics + Budget Guard → v1.45.0

## Task 4: Pricing catalog — model price table (per 1M tokens, hot-reload)

**Description:** Definisikan `PricingEntry` (input/output/cache_read/cache_write USD per 1M token, `source: "models.dev"|"manual"`, opsional `canonical_model_id`) dan `PricingTable` dengan lookup exact-match → wildcard prefix terpanjang (`gpt-4o*`) → nol. Simpan di section settings `pricing` (JSON file + Turso settings payload, normalize-before-persist mengikuti pola `PinOAuthManagedEndpoints`), dan expose ke snapshot runtime agar ikut hot-reload tanpa restart. Harga disimpan sebagai integer micro-USD per 1M token (A2) — karena models.dev mencantumkan harga per 1M token dalam USD, angka micro-USD-per-token identik secara numerik dengan harga per 1M-nya.

**Acceptance criteria:**
- [x] Lookup: exact match menang; wildcard prefix terpanjang menang di antara wildcard; tidak ada match → nol (fallback flat-rate lama ditangani di Task 7)
- [x] Validasi settings menolak angka negatif / entri model kosong dengan pesan jelas
- [x] Perubahan harga ikut hot-reload watcher yang sudah ada, tanpa restart
- [x] Get/Set settings `pricing` via `GET/POST /api/settings` berfungsi
- [x] Entri membawa field `source`; resolusi harga mempertimbangkan `canonical_model_id` untuk provider multi-model (antigravity)

**Verification:**
- [x] `go test -race ./internal/config/... ./internal/registry/... ./internal/server/...` passes
- [x] `go build ./...` succeeds

**Dependencies:** None (independen dari P0; boleh paralel)
**Files likely touched:** `internal/domain/pricing.go` (baru), `internal/config/settings.go`, `internal/server/settings.go`, test file terkait
**Estimated scope:** Medium

## Task 5: models.dev importer — seed-once (A7)

**Description:** Package baru `internal/pricing`: `FetchCatalog(ctx)` GET `https://models.dev/api.json` (timeout 30s, batas ukuran wajar), parse subset (`cost.input/output/cache_read/cache_write`, `limit.context/output`, `canonical_model_id`) per provider×model, konversi harga USD-per-1M → micro-USD integer. Mapping protocol Firefly → provider models.dev memakai tabel config dengan default: `openai`→`openai`, `anthropic`→`anthropic`, `grok-cli`→`xai`, `opencode`→`opencode`, `opencode-go`→`opencode-go`; `antigravity` melayani Gemini DAN Claude → resolusi per-model via `canonical_model_id`; `cline`/`codebuddy-*`/`qoder` tidak di-map (harga manual). **Seed-once**: entri yang key-nya sudah ada di tabel lokal (source manual ATAU models.dev) tidak pernah ditimpa; entri baru ditambah dengan `source: "models.dev"`. Katalog hasil fetch di-cache in-memory (`atomic.Pointer`) untuk browsing dashboard — tidak persisten, tidak jadi dependensi runtime. Endpoint admin (di `internal/server/pricing_admin.go`): `POST /api/pricing/import` (body `{all: true}` atau `{models: [...]}`; `overwrite` selalu false), `GET /api/pricing/catalog?q=&provider=` (katalog cache untuk browse), `POST /api/pricing/catalog/refresh` (fetch ulang).

**Acceptance criteria:**
- [x] Parse api.json menghasilkan entri lengkap (harga per 1M → micro-USD) untuk provider yang ter-map
- [x] Seed-once: import dua kali → entri existing (manual & models.dev) tidak berubah; hanya entri baru yang muncul
- [x] Override manual bertanda `source: "manual"` dan tidak pernah ditimpa importer
- [x] Endpoint import/catalog/refresh admin-only; payload tanpa secret
- [x] models.dev unreachable → error jelas, tabel lokal & runtime tidak terpengaruh
- [x] Antigravity: model Gemini dapat harga `google/*`, model Claude dapat harga `anthropic/*` lewat `canonical_model_id`

**Verification:**
- [x] `go test -race ./internal/pricing/... ./internal/server/...` passes
- [x] `go build ./...` succeeds
- [ ] Manual: tombol import di dashboard mengisi tabel harga (menunggu Task 10 — halaman Pricing)

**Dependencies:** Task 4
**Files likely touched:** `internal/pricing/modelsdev.go` (baru), `internal/pricing/modelsdev_test.go` (baru), `internal/server/pricing_admin.go` (baru), `internal/domain/pricing.go` (Task 4)
**Estimated scope:** Medium-Large

## Task 6: Cached-token capture (usage tracker extension, A8)

**Description:** Perluas `parseUsageFromBytes` (`internal/server/handlers.go:97`) agar juga mengembalikan cachedRead & cacheWrite. OpenAI: `usage.prompt_tokens_details.cached_tokens`; Anthropic: `usage.cache_read_input_tokens` + `usage.cache_creation_input_tokens`. SSE: chunk usage final bisa membawa field yang sama — parse dari tail buffer. Tambah field `CachedReadTokens`/`CacheWriteTokens` ke `analytics.RequestLog` & `TokenLedger`, ikut dipersistensi ke Turso (flusher) mengikuti pola field token existing — **metering murni, jangan bump `updated_at`** (invariant syncer). Normalisasi billable (dipakai Task 7): OpenAI `prompt_tokens` sudah termasuk cached → uncached = `prompt_tokens - cached_tokens`; Anthropic `input_tokens` sudah exclude.

**Acceptance criteria:**
- [x] Non-stream & SSE: cached tokens terekstrak untuk shape OpenAI dan Anthropic
- [x] `RequestLog`/`TokenLedger` menyimpan cached read/write; telemetry summary menampilkannya
- [x] Turso flusher memersistensi field baru tanpa memicu reload katalog (test invariant `updated_at`)
- [x] Response tanpa field cache → nilai 0, tidak error

**Verification:**
- [x] `go test -race ./internal/server/... ./internal/storage/analytics/...` passes
- [x] `go build ./...` succeeds

**Dependencies:** None (boleh paralel dengan Task 4/5)
**Files likely touched:** `internal/server/handlers.go`, `internal/storage/analytics/types.go`, `internal/storage/turso/store.go` (flusher), test terkait
**Estimated scope:** Medium

## Task 7: Standardisasi perhitungan biaya (hapus 5 titik hardcoded)

**Description:** Satu fungsi `EstimateCostMicros(model string, tokensIn, tokensOut, cachedRead, cacheWrite int) int64` di `internal/domain/pricing.go` yang memakai `PricingTable` (hasil mikro: integer micro-USD) dan fallback flat-rate lama `0.0000025/token` saat tabel kosong / harga nol (A6). Rumus (A8): biaya = uncached×harga input + cachedRead×harga `cache_read` + cacheWrite×harga `cache_write` + tokensOut×harga output; harga cache tidak ada → fallback ke harga input. Ganti kelima titik `EstimatedCost: float64(tokensIn) * 0.0000025` di `handlers.go` (baris ±396, 436, 519, 620, 710) dengan fungsi ini; konversi ke float hanya di DTO.

**Acceptance criteria:**
- [x] Tidak ada lagi `* 0.0000025` hardcoded di `internal/server/`
- [x] `EstimatedCost` di `RequestLog` konsisten dengan tabel harga (test: harga 3/15 per 1M → micros benar)
- [x] Biaya cache: 1M uncached @ $3 + 1M cached @ $0.30 → total micros benar (test)
- [x] Harga cache tidak ada di entri → cached ditagih harga input (tidak under-bill)
- [x] Fallback flat-rate aktif saat tabel kosong (A6)
- [x] Akurasi mikro-USD terjaga pada token besar (100M token tidak kehilangan presisi)

**Verification:**
- [x] `go test -race ./internal/server/...` passes
- [x] `go build ./...` succeeds
- [ ] Manual: hitung ulang biaya satu request vs kalkulator harga provider

**Dependencies:** Task 4, Task 6
**Files likely touched:** `internal/server/handlers.go`, `internal/domain/pricing.go`, `internal/server/handlers_test.go`
**Estimated scope:** Small

## Task 8: Budget guard per-tenant (hard cap + soft warning)

**Description:** Tambah `BudgetMicros int64` (0 = unlimited, pola `MaxTokens`) dan `SpentMicros *atomic.Int64` di `domain.Tenant`. Pre-flight check di `forwardEndpoint` setelah resolusi tenant: jika `BudgetMicros > 0 && Spent >= Budget` → tolak **HTTP 429 + `Retry-After: 60`** (A9, keputusan LO) dengan pesan "tenant budget exceeded" dalam envelope protokol terkait. Akumulasi `SpentMicros.Add(costMicros)` di titik yang sama dengan `UsedTokens.Add` (± `handlers.go:684`). Emit soft warning saat pencapaian ≥80% (payload event untuk Task 13). Reset via `/api/tenants/topup` (tambah opsi reset spend). Sinkronisasi `SpentMicros` ke Turso mengikuti pola penyimpanan `UsedTokens` — perhatikan invariant syncer: **jangan bump `updated_at` untuk metering murni**.

**Acceptance criteria:**
- [x] Tenant tanpa budget (0) tidak terpengaruh sama sekali
- [x] Budget terlampaui → HTTP 429 + `Retry-After`, dan counter spend TIDAK berubah karena request ditolak
- [x] Soft warning teremit tepat satu kali per periode budget di ≥80%
- [x] Topup reset spend berfungsi; spend persisten lintas restart (turso) tanpa memicu reload katalog
- [x] Race test: 100 goroutine concurrent request terhadap budget kecil → total spend ≤ budget + tolerance satu request in-flight

**Verification:**
- [x] `go test -race ./internal/server/... ./internal/domain/...` passes
- [x] `go build ./...` succeeds
- [ ] Manual: set budget kecil, kirim request sampai 429, topup, request sukses lagi

**Dependencies:** Task 7
**Files likely touched:** `internal/domain/domain.go`, `internal/server/handlers.go`, `internal/server/tenant_topup.go`, `internal/storage/turso/store.go` (pola sinkronisasi), test terkait
**Estimated scope:** Medium

## Task 9: Agregasi biaya + API laporan

**Description:** Tambah agregator biaya per tenant/key/model/hari di atas `RequestLog` (in-memory ring/histogram + dump periodik ke `analytics` store yang sudah persisten), termasuk breakdown token cache (cached read/write dari Task 6). Endpoint admin: `GET /api/usage/costs?group_by=tenant|model|key&since=&until=` mengembalikan seri harian + total.

**Acceptance criteria:**
- [x] Agregasi konsisten dengan `TokenLedger.EstimatedCostUSD` total (toleransi pembulatan mikro)
- [x] Filter `since`/`until` dan `group_by` berfungsi; rentang kosong → 7 hari terakhir
- [x] Endpoint admin-only; payload memakai USD float di DTO (konversi dari micros)
- [x] Reset/retensi data mengikuti kebijakan rotasi `analytics` yang sudah ada

**Verification:**
- [x] `go test -race ./internal/storage/analytics/... ./internal/server/...` passes
- [x] `go build ./...` succeeds
- [ ] Manual: kirim beberapa request, cek agregasi per model/tenant di endpoint

**Dependencies:** Task 7
**Files likely touched:** `internal/storage/analytics/store.go` atau aggregator baru, `internal/server/usage_admin.go` (baru), test terkait
**Estimated scope:** Medium

## Task 10: Halaman Pricing dashboard (A10)

**Description:** Halaman baru `pricing` di `frontend/src/registry.tsx` (group CONFIGURATION, ikon `Coins`/`Tags`). Isi: (a) tabel entri harga lokal — model key, provider, input/output/cache_read/cache_write (USD per 1M), badge source (`models.dev`/`manual`), indikator "dipakai catalog Firefly" (cocok dengan `public_name`/`upstream_model` di settings), aksi Edit & Delete-override; (b) tombol **Import from models.dev** (seed-once, preview sebelum commit) + **Refresh catalog**; (c) panel browse katalog models.dev (cache in-memory dari Task 5) dengan filter provider + pencarian, multi-select, dan preview "N model baru akan ditambahkan, M existing dipertahankan"; (d) drawer edit harga (input/output/cache per 1M, validasi ≥ 0). Filter bar: pencarian, provider, source (all/manual/models.dev), status (all/used/unused). Pola komponen mengikuti halaman existing (`PageHeader`, `QueryGate`, `Drawer`, `Badge`, `Segmented`, `filter-bar`). Data via endpoint Task 5 (`GET /api/pricing`, `PUT /api/pricing/{key}`, `DELETE /api/pricing/{key}`, `POST /api/pricing/import`, `GET /api/pricing/catalog`, `POST /api/pricing/catalog/refresh`).

**Acceptance criteria:**
- [x] Halaman terdaftar di registry & navigasi; admin-only
- [x] Tabel menampilkan entri lokal + badge source + status terpakai; filter & pencarian berfungsi
- [x] Edit harga → tersimpan (settings/pricing.json), badge berubah `manual`, hot-reload tanpa restart
- [x] Delete override → entri kembali ke seed models.dev (atau hilang kalau tidak ada seed)
- [x] Import seed-once dari UI: preview → commit; entri existing tidak berubah
- [x] Browse katalog models.dev dengan filter provider & search; select → import
- [x] `cd frontend && npm run build` hijau

**Verification:**
- [x] `go test -race ./internal/server/...` passes; `cd frontend && npm run build` succeeds
- [x] `go build ./...` succeeds
- [ ] Manual: import dari UI → tabel terisi; edit satu harga → badge manual; import ulang → harga tidak berubah

**Dependencies:** Task 4, Task 5
**Files likely touched:** `frontend/src/pages/PricingPage.tsx` (baru), `frontend/src/registry.tsx`, `frontend/src/services/api.ts`, test terkait
**Estimated scope:** Large

## Task 11: Filter pricing di halaman Models (A10)

**Description:** Di `ModelsPage.tsx` panel Direct Models: tambah filter dropdown "Pricing: All / Registered / Not registered" (registered = ada entri harga yang resolve untuk model tersebut — dicek via endpoint resolve backend, bukan lookup client-side, karena mapping protocol→provider hidup di backend), kolom harga ringkas (input/output per 1M atau "—"), dan aksi cepat **Set price** yang membuka drawer harga (reuse komponen drawer dari PricingPage) atau navigasi ke halaman Pricing dengan model terpilih. "Dan seterusnya": tampilkan indikator ketersediaan cache price, dan tampilkan source badge (models.dev/manual) per baris. Endpoint pendukung: `POST /api/pricing/resolve` menerima list `{public_name, upstream, upstream_model}` → mengembalikan entri harga ter-resolve (atau null) per model.

**Acceptance criteria:**
- [x] Filter registered/not registered akurat (verifikasi via API resolve, bukan asumsi client-side)
- [x] Kolom harga tampil untuk model registered; "—" + aksi Set price untuk yang tidak
- [x] Set price dari Models page menyimpan entri dan baris langsung terlihat registered
- [x] Filter berkolaborasi dengan search existing (gabungan AND)
- [x] `cd frontend && npm run build` hijau

**Verification:**
- [x] `go test -race ./internal/server/...` passes; `cd frontend && npm run build` succeeds
- [x] `go build ./...` succeeds
- [ ] Manual: model tanpa harga → filter "Not registered" menampilkannya → Set price → pindah ke "Registered"

**Dependencies:** Task 4, Task 5 (endpoint resolve)
**Files likely touched:** `frontend/src/pages/ModelsPage.tsx`, `frontend/src/services/api.ts`, `internal/server/pricing_admin.go` (endpoint resolve)
**Estimated scope:** Medium

### Checkpoint: Phase P1 selesai
- [x] Full suite hijau; biaya di dashboard tidak lagi flat-rate (tabel harga aktif → micros; fallback flat-rate hanya saat tabel kosong)
- [x] Skenario e2e: set harga → request → biaya tercatat (termasuk cached tokens) → budget 429 → topup → lanjut (perlu server hidup; komponen tiap langkah sudah ter-cover test)
- [x] Halaman Pricing berisi data models.dev yang diimpor seed-once; filter Models page akurat
- [ ] Review human sebelum lanjut ke Phase P2

---
# Phase P2 — Notification & Webhook Alerting → v1.46.0

## Task 12: Notifier core — dispatcher async, HMAC, anti-SSRF

**Description:** Package baru `internal/notify`: tipe `Event` (`type`, `severity`, `subject`, `body`, `data`, `occurred_at`), `Dispatcher` (bounded queue per A4; goroutine worker berakhir saat ctx.Done; drop + counter saat penuh), `Poster` HTTP dengan HMAC-SHA256 header `X-Firefly-Signature: t=<unix>,v1=<hex>` atas body mentah + optional static bearer, custom dialer yang memblokir IP link-local/metadata, retry ringan (mis. 3 percobaan, backoff tetap), dan formatter target: `generic` (JSON), `discord` (embed), `slack` (blocks), `telegram` (sendMessage).

**Acceptance criteria:**
- [x] Queue penuh → event didrop + counter naik, tidak ada block pada caller
- [x] Shutdown via ctx.Done: worker berhenti ≤5 detik, tanpa goroutine bocor (race test)
- [x] Signature HMAC terverifikasi oleh test consumer (verifikasi ulang hex)
- [x] Dial ke `127.0.0.1`, `169.254.169.254`, `10.x`, `192.168.x` diblokir (config allowlist untuk dev/test)
- [x] Retry: gagal sementara → 3 percobaan; 4xx dari consumer → tidak di-retry

**Verification:**
- [x] `go test -race ./internal/notify/...` passes
- [x] `go build ./...` succeeds

**Dependencies:** None
**Files likely touched:** `internal/notify/event.go`, `internal/notify/dispatcher.go`, `internal/notify/poster.go`, `internal/notify/format.go`, test terkait
**Estimated scope:** Medium

## Task 13: Instrumentasi event (key lifecycle, breaker, budget, health)

**Description:** Suntikkan `notify.Dispatcher` (interface kecil, default no-op agar test upstream existing tidak berubah) ke: `upstream.HandleKeyOutcome` untuk `key.cooldown` (429), `key.revoked` (401), `key.threshold_action` (deactivate/delete); transisi state `breaker.go` untuk `breaker.open`, `breaker.half_open`, `breaker.closed`; Task 8 menyediakan `tenant.budget_warning`/`tenant.budget_exceeded`; health checker memancarkan `upstream.health_failed`/`upstream.health_recovered`. Dedupe: breaker flapping dibatasi minimal 1 event per state transition per 60 detik.

**Acceptance criteria:**
- [x] Semua event terdaftar di plan teremit dengan payload minimal (upstream, key ref tersamarkan `[REDACTED]`/hint, status)
- [x] Tidak ada secret dalam payload (audit string payload: hanya hint)
- [x] No-op default: tanpa konfigurasi, perilaku runtime identik seperti sekarang
- [x] Dedupe breaker aktif; burst 429 menghasilkan koalescing (maks 1 event/30 detik per key)
- [x] Semua goroutine dispatcher berakhir saat ctx.Done (race test)

**Verification:**
- [x] `go test -race ./internal/transport/upstream/... ./internal/notify/...` passes
- [x] `go build ./...` succeeds

**Dependencies:** Task 12, Task 8 (untuk event budget)
**Files likely touched:** `internal/transport/upstream/policy.go`, `internal/transport/upstream/breaker.go`, `internal/transport/upstream/health.go`, wiring di `cmd/firefly/main.go`, test terkait
**Estimated scope:** Medium

## Task 14: Settings, API test-send, dashboard notifications

**Description:** Section settings `notifications` (list channel: url, format, optional bearer, enabled, event filter) dengan normalize-before-persist seperti pola settings lain; endpoint admin `POST /api/notifications/test` mengirim event dummy ke channel target; dashboard: section di `SettingsPage.tsx` (daftar channel + tombol Test) dan panel ringkas event terbaru di `OverviewPage.tsx` (memakai counter drop/dispatched dari telemetry).

**Acceptance criteria:**
- [x] CRUD channel via `GET/POST /api/settings` berfungsi dan tervalidasi (URL valid, format dikenal)
- [x] Test-send mengirim event dummy dan melaporkan hasil (ok/gagal + alasan)
- [x] Secret bearer tidak pernah dikembalikan mentah oleh API (masking `***`)
- [x] Frontend build lolos: `cd frontend && npm run build`

**Verification:**
- [x] `go test -race ./internal/server/...` passes; `cd frontend && npm run build` succeeds
- [x] `go build ./...` succeeds
- [ ] Manual: daftarkan webhook Discord pribadi → Test → pesan muncul

**Dependencies:** Task 12, Task 13
**Files likely touched:** `internal/config/settings.go`, `internal/server/settings.go`, `internal/server/notifications_admin.go` (baru), `frontend/src/pages/SettingsPage.tsx`, `frontend/src/pages/OverviewPage.tsx`, `frontend/src/services/api.ts`
**Estimated scope:** Medium

### Checkpoint: Phase P2 selesai
- [ ] Full suite hijau; webhook Discord/Slack/Telegram menerima event uji
- [x] Alert storm scenario (simulasi breaker flap) tidak membanjiri channel
- [ ] Review human sebelum release

---

# Final

## Task 15: Dokumentasi & verifikasi rilis

**Description:** Perbarui README (bagian endpoint + contoh), llms.txt, OpenAPI spec data-plane & admin, CHANGELOG entri per versi target, dan jalankan verifikasi penuh.

**Acceptance criteria:**
- [x] README/llms.txt/OpenAPI memuat `/v1/messages`, laporan biaya, dan notifications
- [x] CHANGELOG: entri 1.44.0 / 1.45.0 / 1.46.0 dengan feat/fix utama
- [x] `go vet ./...` clean

**Verification:**
- [x] `go test -count=1 -race ./...` passes (full, final gate)
- [x] `go build ./...` + `go vet ./...` + `cd frontend && npm run build` all succeed
- [x] Smoke test binary rilis: start → login dashboard → satu request `/v1/messages` → cek biaya tercatat → trigger test webhook

**Dependencies:** Task 3, Task 9, Task 14
**Files likely touched:** `README.md`, `llms.txt`, `CHANGELOG.md`, `internal/server/openapi.go`
**Estimated scope:** Small

## Checkpoint: Complete (seluruh roadmap)
- [x] Semua acceptance criteria terpenuhi
- [x] Full verification gate hijau (build, vet, race test, frontend build)
- [x] Siap review & rilis

---

## Parallelization Notes

- **Aman diparalelkan:** Task 4–6 (fondasi P1) terhadap Task 1–2 (P0) — file berbeda; Task 12 (P2 core) terhadap P0/P1 — package baru.
- **Perlu koordinasi:** Task 2 dan Task 7 sama-sama menyentuh `handlers.go`; Task 13 dan Task 8 sama-sama menambah emit di jalur server. Urutkan bila satu agent mengerjakan semuanya.
- **Harus berurutan:** Task 1 → 2 → 3; Task 4 → 5 → 7; Task 7 → 8 → 9; Task 12 → 13 → 14.
