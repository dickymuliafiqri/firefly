# Rencana Kerja — Firefly sebagai Pemilik Natif `providers` & `api_keys`

> **Status:** DISETUJUI & terimplementasi untuk Fase 0–2c (working tree, **belum di-commit**), plus K3-c (permukaan baca legacy hint-only + dashboard *Bind Provider Keys*), plus kode Fase 3 di repo harvester (belum di-deploy). Fase 4 **belum dimulai** — gerbangnya keputusan O10, O13, O14 dan lama masa observasi.
> **Tanggal:** draf 2026-09-21 · terakhir diperbarui 2026-09-22
> **Lingkup perubahan:** `internal/storage/turso`, `internal/server`, `cmd/firefly`, `frontend/src` (hint-only + bind provider)
> **Di luar lingkup:** jalur data plane (`forwardEndpoint`, `RelaySSE`, `KeyRing`, `Breaker`), DDL tabel Firefly lain, tabel bisnis (`users`, `orders`, `subscriptions`)

---

## 1. Ringkasan

Firefly menjadi **pemilik natif** skema `providers` dan `api_keys`: ia yang membuat tabelnya, ia satu-satunya penulis, dan ia menyediakan API CRUD. Harvester tetap berjalan sebagai **service terpisah**, tetapi kehilangan akses langsung ke database — seluruh operasi tulisnya dilakukan melalui API Firefly.

Transisi ini lebih kecil dari yang terlihat: Firefly **sudah** membaca kedua tabel dan **sudah** menulis `api_keys` untuk metering, revoke, deactivate/delete, dan expired-sweep. Yang masih dipegang harvester hanyalah **pembuatan baris (INSERT)** dan **DDL**. Rencana ini menutup dua jalur terakhir itu.

---

## 2. Kondisi Awal (terverifikasi)

| Aspek | Kondisi hari ini | Bukti |
|---|---|---|
| Baca `providers` + `api_keys` | Sudah native | `store.go:1706` (`ListProviders`), `store.go:1752` (`ListProviderKeys`), `store.go:474` (join provider→keys saat build snapshot) |
| Tulis `api_keys` — metering | Sudah native, **tanpa** bump `updated_at` | `flusher.go:274-286` |
| Tulis `api_keys` — revoke/deactivate/delete | Sudah native | `flusher.go:292-339`, `store.go:1839` (`DeactivateKey`), `store.go:1866` (`DeleteKey`) |
| Tulis `api_keys` — expired sweep | Sudah native | `store.go:1798` (`DeactivateExpiredKeys`) |
| **Buat baris** (`INSERT providers` / `INSERT api_keys`) | **Milik harvester** (service luar repo) | `BUSINESS.md:269-276` |
| **DDL kedua tabel** | **Milik harvester**; Firefly hanya merujuk FK-nya | `schema.go:18` (`upstreams.provider_id → providers`), `schema.go:48` (`upstream_credentials.api_key_id → api_keys`) |
| Permukaan HTTP | Read-only: `GET /api/turso/providers`, `GET /api/turso/providers/{id}/keys`, `GET /api/turso/keys` | `router.go:190-194`, `settings.go:537` (`handleGetTursoProviders`) |
| Sinyal reload katalog | `catalog_revisions` + `GetKeysState` (`MAX(updated_at)`, `COUNT(*)`) dipantau syncer tiap 15 detik | `syncer.go:37`, `syncer.go:95-104`, `syncer.go:123-139` |
| Identitas credential ref | `-key-<api_keys.id>` — bergantung pada stabilitas `id` baris | `flusher.go:380` (`extractAPIKeyID`), `schema.go:48` |
| Pola CRUD admin yang bisa ditiru | Tenant: `tenants_admin.go`, rute di `router.go:180-186`, guard `authorizeAdmin` (`router.go:118`) | — |

**Kesimpulan:** bukan migrasi kepemilikan dari nol. Pekerjaannya = (a) adopsi DDL, (b) endpoint create/update + auth service, (c) migrasi harvester, (d) cabut akses DB.

---

## 3. Keputusan Arsitektur

### 3.1 Sudah diputuskan

- **D1.** Firefly menjadi pemilik natif skema `providers` + `api_keys`.
- **D2.** Harvester tetap berjalan sebagai service terpisah, tanpa akses langsung ke database.
- **D3.** Seluruh CRUD harvester dilakukan melalui API Firefly.
- **D4.** Harvester menjadi API client; Firefly menjadi satu-satunya penulis DB.

### 3.2 Yang masih terbuka pada draf awal — **semuanya disetujui 2026-09-21**

> **Verdict (user, 2026-09-21: "setujui rekomendasi dan lanjut ke Fase 1"):** seluruh rekomendasi di tabel ini diterima apa adanya. Wujudnya di kode hari ini — O1 → `POST /api/harvester/sync`, satu snapshot = satu transaksi = satu bump revisi; O2 → hanya id yang disebut di `deactivate_keys` yang dimatikan; O2b → `ErrKeyProviderMismatch` → HTTP 409; O4 → `FIREFLY_HARVESTER_TOKEN` (`main.go:75`, `:109`, `:512`), kosong → 503; O5 → prefix `/api/harvester/*` (`router.go:221`); O6 → `/api/turso/providers*` lama tetap ada; O8 → `account_metadata` write-only, diuji di kedua permukaan; O9 → normalisasi satuan baris lama ditunda, endpoint selalu tulis milidetik + bump revisi. Yang **benar-benar masih terbuka sekarang** tinggal O10, O13, O14 dan lama masa observasi Fase 4 — lihat §11. (O11 dan O12 ditutup 2026-09-22.)

| # | Pertanyaan | Rekomendasi | Alasan |
|---|---|---|---|
| O1 | Bentuk endpoint: satu snapshot `POST /api/harvester/sync`, atau granular per-item? | **Snapshot tunggal** | Harvester adalah batch writer; satu panggilan = satu transaksi = satu bump revisi. Granular memicu reload berulang. |
| O2 | Bolehkah `deactivate_missing` mematikan key yang dibuat operator lewat dashboard? | **Tidak.** Server hanya mematikan id yang **eksplisit disebut** di payload; kebijakan "hanya key yang pernah saya buat" dijaga di sisi harvester (outbox-nya sendiri) | Tanpa kolom `created_by` (perlu ALTER — ditunda), kepemilikan tidak bisa dibuktikan server-side. Daftar id eksplisit + disiplin klien cukup untuk fase ini. |
| O2b | Key yang sudah ada dikirim ulang dengan `provider` berbeda — pindahkan atau tolak? (temuan Fase 0: `UNIQUE(api_key)` bersifat **global**) | **Tolak** dengan error jelas; perpindahan provider hanya lewat endpoint operator | Upsert yang otomatis-bindah membuat harvester bisa "menarik" key milik provider lain tanpa jejak. |
| O3 | ~~Dedupe `(provider_id, api_key)`~~ | **Tidak berlaku — Fase 0 membuktikan 0 duplikat** (varian `TRIM` pun 0). Fase 1b dibatalkan | Prasyaratnya hilang; `UNIQUE(api_key)` sudah ada sejak awal. |
| O4 | Token service: env var atau tersimpan di `system_settings`? | **Env var** `FIREFLY_HARVESTER_TOKEN`, fail-closed bila kosong | Mengikuti preseden `RouterDeps.AdminToken` (`router.go:31`); rotasi = restart; tidak ada rahasia at-rest tambahan di DB. |
| O5 | Prefix route mesin: `/api/harvester/*` atau `/api/service/*`? | **`/api/harvester/*`** | Eksplisit soal pemanggilnya; memudahkan audit "siapa boleh apa". |
| O6 | Konsolidasi endpoint read-only lama `/api/turso/providers*` dengan CRUD baru? | **Pertahankan keduanya untuk sekarang**; konsolidasi saat fase frontend | Dashboard sudah memakainya; mengganti sekarang menambah risiko tanpa manfaat. |

---

## 4. Invariant (non-negotiable)

- **I1 — `api_keys.id` stabil.** Ref `-key-<id>` (`flusher.go:380`) dan FK `upstream_credentials.api_key_id` (`schema.go:48`) bergantung padanya. Upsert **wajib mempertahankan `id`** baris yang sudah ada; jalur harvester **tidak pernah hard-delete**.
- **I2 — Rahasia tidak pernah bocor.** Respons API dan log hanya memuat hint termask (`sk-***`); `RedactHandler` tetap berlaku; terjadi juga pada pesan error. **Kolom `account_metadata` dikecualikan dari semua respons baca** — Fase 0 membuktikan isinya memuat `privkey`, `mnemonic`, `password`, `access_token`, `refresh_token`, `id_token`, `secondary_api_key`, dan email (Lampiran A). Kolom ini hanya boleh ditulis (pass-through), tidak pernah dibaca-kembali lewat API.
- **I3 — `updated_at` hanya untuk perubahan struktural.** Metering (`total_requests`, `last_used_at`) dilarang menyentuhnya — pelajaran dari reload storm v1.8.2 (CHANGELOG:201-203).
- **I4 — Satu penulis per tabel pada satu waktu.** Tidak boleh ada jendela di mana harvester dan Firefly sama-sama menulis `api_keys`.
- **I5 — Jalur data plane tidak tersentuh.** Endpoint ini bukan jalur traffic; tidak mengubah `forwardEndpoint`, `RelaySSE`, `KeyRing`, atau `Breaker`.
- **I6 — Fail-closed.** Token service tidak dikonfigurasi → endpoint menolak (503), bukan mengizinkan.
- **I7 — Harvester hanya upsert + deactivate.** Hard-delete tetap hak operator (konsisten dengan filosofi flag `Manage*`).

---

## 5. Skema Folder & File

```
docs/
  harvester-ownership-plan.md      BARU  — dokumen ini

internal/server/
  service_auth.go                  BARU  — ServiceToken di RouterDeps + authorizeService()
  harvester_api.go                 BARU  — POST /api/harvester/sync (audiens: harvester)
  harvester_api_test.go            BARU
  providers_admin.go               BARU  — CRUD operator /api/providers[...] (audiens: dashboard)
  providers_admin_test.go          BARU
  settings.go                      UBAH  — preflight CORS mengiklankan PATCH+DELETE (dibutuhkan rute operator baru); sejak 2026-09-22 juga: permukaan baca key legacy jadi hint-only (`legacyProviderKey` + `maskSecret`)
  turso_manager.go                 UBAH  — simpan referensi syncer + TriggerSync(ctx)
  router.go                        UBAH  — registrasi route + field ServiceToken
  openapi.go                       UBAH  — embed + serve /api/openapi-harvester.yaml
  openapi/openapi-harvester.yaml   BARU  — spec mesin (service token)
  openapi/openapi-admin.yaml       UBAH  — tambah path /api/providers*
  openapi_harvester_test.go        BARU  — drift test (regex dibatasi ^/api/harvester)
  openapi_admin_test.go            UBAH  — lebarkan regex ke ^/api/(tenants|providers|keys)

internal/storage/turso/
  provider_store.go                BARU  — upsert/sync + pindahan read helper domain ini
  provider_store_test.go           BARU
  models.go                        UBAH  — ProviderRecord + DTO sync (request/result)
  schema.go                        UBAH  — DDL providers + api_keys masuk ke schemaDDL
  schema_test.go                   BARU  — fresh-deploy / idempoten / tabel milik pihak lain tidak di-ALTER
  store.go                         UBAH  — −101 baris: isi Fase 2c dipindah keluar (bukan hanya "pindah" — file asalnya)
  store_test.go                    UBAH  — DDL tiruan providers/api_keys dihapus; seed melengkapi kolom NOT NULL
  errors.go                        UBAH  — sentinel baru: ErrInvalidSyncPayload / ErrKeyProviderMismatch / ErrProviderUnknown / ErrInvalidPayload / ErrProviderExists / ErrProviderInUse
  syncer.go                        UBAH  — mutex yang mensekuensialkan SyncOnce (ticker periodik & TriggerSync berbagi cursor lastKnown*) — tidak ada di draf awal
  syncer_test.go                   BARU  — invarian fail-closed reload (2026-09-22): snapshot lama tetap melayani saat LoadCatalogSnapshot gagal, cursor tidak maju

cmd/firefly/main.go                UBAH  — wire FIREFLY_HARVESTER_TOKEN → RouterDeps

frontend/src/services/schema.ts    UBAH  — TursoKeyDTO/TursoKeysResponse → TursoKeyHintDTO/TursoKeyHintsResponse (2026-09-22)
frontend/src/services/api.ts       UBAH  — fetchTursoProviderKeyHints menggantikan pasangan fetchTursoProviderKeys/useFetchTursoKeysMutation
frontend/src/modules/2-upstreams/UpstreamModal.tsx
                                   UBAH  — "Import from Database" → "Bind Provider Keys" (server-side pooling; nol secret ke browser)
frontend/src/modules/8-providers/
  ProvidersView.tsx                BARU  — tab Providers (daftar provider + tabel key per provider; CRUD provider, batch upsert, patch, delete)
  KeyPatchModal.tsx                BARU  — patch status/routability/kedaluwarsa + rotasi secret in-place
frontend/src/modules/registry.tsx  UBAH  — entri `providers` (order 8, lazy + preload)
frontend/src/core/layout/Header.tsx UBAH — TabId `providers` + entri TABS
frontend/src/hooks/useKeyboardShortcuts.ts
                                   UBAH  — hotkey `8` → tab Providers
```

> **Koreksi 2026-09-22:** draf §5 ini hanya mencantumkan file yang *sengaja* akan disentuh. Enam file lain ternyata ikut berubah dan baru dicatat setelah implementasi: `settings.go`, `store.go`, `store_test.go`, `errors.go`, `syncer.go`, plus `schema_test.go` (test Fase 1 yang belum terdaftar di draf) — semuanya cocok dengan `git status` hari ini. `syncer.go` khususnya bukan kosmetik: tanpa mutex-nya, `TriggerSync` dari endpoint harvester bisa tumpang tindih dengan tick periodik dan menjalankan dua siklus reload di atas cursor yang sama.

**Alasan ringkas:**

- `providers_admin.go` dipisah dari `harvester_api.go` — dua audiens, dua mekanisme auth. Digabung berarti guard rawan tertukar; itu kelas bug paling mahal di area ini.
- `authorizeService` di `service_auth.go` mengikuti preseden `authorizeAdmin` (`router.go:118`, method `RouterDeps`), bukan middleware di `internal/transport/httpx/` — folder itu khusus middleware jalur data plane.
- Spec OpenAPI ketiga karena spec harvester punya `securitySchemes` berbeda (service token vs session admin); drift test yang ada bekerja per-prefix (`openapi_admin_test.go` dibatasi `/api/tenants*`).
- `provider_store.go` mengikuti preseden `oauth_store.go`; `store.go` sudah ~1.900 baris. Orchestrasi sync harus di store karena wajib satu transaksi dengan `BumpCatalogRevision` (`store.go:177`).
- DTO kontrak di `turso/models.go` bersama record (preseden `ProviderSummary`/`ProviderKeyDTO`); handler tetap tipis: decode → validasi → panggil store → encode.

---

## 6. Fase Implementasi

### Fase 0 — Introspeksi & Pembekuan Kontrak (read-only, tanpa kode)

**Tujuan:** mengunci DDL nyata dan mengukur masalah duplikat sebelum satu baris kode pun ditulis.

**Langkah:**
1. Query read-only ke DB: `PRAGMA table_info(providers)`, `PRAGMA table_info(api_keys)`, `PRAGMA index_list(...)`, `PRAGMA foreign_key_list(...)` untuk kedua tabel.
2. Hitung: total baris; duplikat `(provider_id, api_key)`; baris `expires_at` terlewat tapi `is_active = 1`; baris `secret`/`api_key` kosong.
3. Susun matriks operasi harvester → endpoint (insert keys, prune, upsert providers) dan finalisasi kontrak §6.2.

**Output:** angka + DDL hasil introspeksi menjadi isi `schema.go` di Fase 1; kontrak API final.

**Verifikasi:** hasil query dilampirkan; **nol** perubahan file.

**Gate:** butuh kredensial DB read-only dari Anda. Tidak menyentuh data.

---

### Fase 1 — Adopsi DDL

**Status:** diimplementasikan 2026-09-21 — kode ada di working tree (belum di-commit), test lulus `go test -race ./internal/storage/turso/...`. DB live belum tersentuh: `CREATE TABLE IF NOT EXISTS` hanya dieksekusi saat proses berikutnya connect.

**Tujuan:** Firefly yang membuat kedua tabel; deploy baru tidak lagi bergantung pada harvester.

**Langkah:**
1. Adopsi DDL **persis** hasil introspeksi (Lampiran A) ke `schemaDDL` sebagai `CREATE TABLE IF NOT EXISTS`:
   - `providers`: `id`, `name` (UNIQUE), `base_url`, `description`, `is_active`, `created_at`, `updated_at`.
   - `api_keys`: `id`, `provider_id` (FK → `providers`, `ON DELETE CASCADE`), `api_key` (UNIQUE **global**), `status`, `is_active`, `expires_at`, `last_used_at`, `total_requests`, `account_metadata JSON`, `created_at`, `updated_at`.
   - Index yang sudah ada di DB: `idx_provider_active_keys (provider_id, is_active, status, last_used_at)` — adopsi apa adanya. **Tidak perlu index baru**: usulan awal `idx_api_keys_provider_active` sudah tercakup, dan `idx_api_keys_updated_at` tidak dibutuhkan pada 2.801 baris.
2. **Larangan:** tidak ada `ALTER`/`DROP`. `UNIQUE(api_key)` dan `UNIQUE(providers.name)` sudah ada di DB live — definisikan identik, jangan berbeda.
3. Test: DB file baru → tabel + index + FK ada; jalankan DDL dua kali → idempoten; fixture dengan tabel yang sudah ada → tidak error.

**File:** `internal/storage/turso/schema.go` (DDL), `internal/storage/turso/schema_test.go` (BARU — test fresh-deploy/idempoten/tabel-pra-ada). `store_test.go` disesuaikan: tabel tiruan `providers`/`api_keys` di `setupTestDB` dihapus (sekarang dimiliki `MigrateSchema`) dan seed INSERT melengkapi kolom `NOT NULL` tanpa default (`last_used_at`, `total_requests`, `providers.is_active`).

**Verifikasi:** `go test -race ./internal/storage/turso/...`

**Gate:** deploy menyentuh DB live (meski `IF NOT EXISTS` aman) — minta persetujuan saat rilis.

**Rollback:** revert commit. Tabel tetap ada di DB live; additive, tidak destruktif.

---

### Fase 1b — ~~Dedupe + `UNIQUE(provider_id, api_key)`~~ **DIBATALKAN**

Fase 0 membuktikan **tidak ada duplikat sama sekali** (0 grup, varian `TRIM` pun 0) dan `UNIQUE(api_key)` **sudah ada** di DB live. Tidak ada yang perlu dimigrasikan. Sisa relevan dari fase ini hanya keputusan **O2b** (penolakan perpindahan provider pada upsert).

---

### Fase 2a — Endpoint Mesin + Auth Service

**Tujuan:** `POST /api/harvester/sync` hidup, idempoten, dan aman.

**Kontrak:**

```
POST /api/harvester/sync
Authorization: Bearer <FIREFLY_HARVESTER_TOKEN>
Content-Type: application/json

{
  "providers": [
    {"name": "openai", "base_url": "https://api.openai.com", "is_active": true}
  ],
  "keys": [
    {"provider": "openai", "api_key": "sk-...", "status": "active", "expires_at": 1730000000000}
  ],
  "deactivate_keys": [42, 43]        // opsional; id eksplisit saja (keputusan O2)
}

200 OK
{
  "created": 12,
  "updated": 3,
  "unchanged": 140,
  "deactivated": 2,
  "key_ids": {"openai": [1, 2, 3, "<semua id aktif provider ini>"]},
  "revision": 87,
  "pushed": true,
  "reloaded": true
}
```

- **Idempotensi:** natural key provider = `name`; key = **`api_key` saja** (temuan Fase 0: `UNIQUE(api_key)` global) → `INSERT ... ON CONFLICT(api_key) DO UPDATE`. Retry payload yang sama **tidak** membuat baris baru dan **tidak** mengubah `id` (I1). Bila `api_key` sudah ada dengan `provider_id` berbeda → **tolak** (O2b).
- **`account_metadata`** diterima apa adanya (pass-through) dan **tidak pernah dikembalikan** di respons apa pun (I2).
- **Satuan waktu wajib milidetik** (`time.Now().UnixMilli()`) untuk `created_at`/`updated_at`/`expires_at` baris yang ditulis endpoint ini — DB live saat ini campur detik/milidetik (Lampiran A §A.4). **Ditegakkan di boundary:** `expires_at` di bawah `100_000_000_000` ditolak dengan 400, karena nilai berskala detik akan terbaca "sudah kedaluwarsa" secara permanen dan key-nya tidak pernah bisa dirutekan. Aturan yang sama berlaku untuk PATCH/batch jalur operator.
- **Validasi bentuk seluruh batch sebelum menulis apa pun** (all-or-nothing): `validateProviderShape` + `validateKeyShape` dijalankan sebelum transaksi dibuka, sehingga satu entri cacat = 400 dan **nol** baris berubah, bukan sebagian ter-apply. Yang diperiksa: lebar kolom yang diadopsi dari DDL (`name` ≤ 64, `base_url`/`description` ≤ 255 — SQLite tidak menegakkan `VARCHAR(n)`, jadi penulis yang harus menegakkannya), `status` ≤ 20 karakter tanpa control character atau spasi di ujung, `account_metadata` ≤ 64 KiB (`turso.MaxKeyMetadataBytes`, satu sumber kebenaran untuk kedua jalur tulis), dan `provider`/`api_key` non-kosong.
- **`status` di jalur sync = pass-through dengan validasi bentuk, bukan allowlist** (Lampiran B.3 hazard #2). Nilai asing yang bentuknya baik (`dead`, `cooldown`) tetap diterima dan menjadi **inert** (`is_active = 0`), sementara `key_ids` di respons hanya berisi key yang benar-benar dirutekan — sehingga salah ketik status tetap terdeteksi dari respons. Allowlist `keyStatuses` tetap dipakai di jalur operator (POST batch + PATCH).
- **Respons tidak pernah memuat rahasia** — hanya hint termask (I2).
- **Error:** 400 payload invalid · 401 token salah/absen · 413 body terlalu besar (`maxRequestBodyBytes = 32 << 20`, `handlers.go:29`) · 503 token service tidak dikonfigurasi (I6) atau store tidak tersedia. Semua lewat `openai.WriteError`.
- **`pushed`** melaporkan hasil `PushLocked` agar harvester tahu perubahan sudah/akan tereplikasi ke cloud (relevan untuk multi-node).
- **`reloaded`** melaporkan apakah katalog di node penulis sudah di-build ulang sebelum respons ditulis. `false` **bukan** error — batch tetap ter-commit dan `catalog_revisions` sudah naik, sehingga syncer periodik publishes-nya — tapi harvester tahu key barunya belum pasti dirutekan detik itu juga.

**Langkah:**
1. `service_auth.go` — `RouterDeps.ServiceToken` + `authorizeService` (`subtle.ConstantTimeCompare`; kosong → 503).
2. `provider_store.go` — `SyncProviderKeys(ctx, req)` dalam **satu transaksi**: upsert provider by `name`; upsert key `ON CONFLICT(api_key)` mempertahankan `id`; `deactivate_keys` terbatas id eksplisit; `bumpCatalogRevisionLocked` di tx yang sama — **bump ini wajib** karena ia yang membuat reload deterministik dan melepaskan ketergantungan pada heuristik `MAX(updated_at)` yang rapuh (Lampiran A §A.4); `PushLocked` setelah commit.
3. `harvester_api.go` — handler tipis: batas 32MB, batas jumlah item per batch, batas ukuran `account_metadata`, lalu panggil store. Validasi bentuk per-entri hidup di `provider_store.go` (bukan hanya di handler) supaya jalur operator dan jalur sync tidak bisa berbeda aturan.
4. `turso_manager.go` — simpan referensi syncer (`startSyncerLocked`, `turso_manager.go:74`) dan tambah `TriggerSync(ctx)`: panggil `SyncOnce` non-blocking agar **node penulis langsung reload** (bukan menunggu tick 15 detik). Node lain tetap konvergen lewat tick masing-masing.
5. `router.go` — daftarkan rute (tanpa OPTIONS/CORS; ini server-to-server).
6. `openapi.go` + `openapi/openapi-harvester.yaml` + `openapi_harvester_test.go`.
7. `cmd/firefly/main.go` — wire `FIREFLY_HARVESTER_TOKEN`.

**Test wajib:**
- Auth: tanpa token → 401; token salah → 401; token benar → 200; server tanpa token → 503 (fail-closed).
- Idempotensi: payload sama 2× → `created` hanya di panggilan pertama; `COUNT(*)` tidak bertambah; `id` tidak berubah; test khusus "id stabil".
- Konflik provider (O2b): `api_key` yang sudah ada dikirim dengan `provider` berbeda → ditolak, baris lama tidak berubah.
- Masking: respons & log tidak memuat `api_key` mentah **maupun isi `account_metadata`** (uji dengan metadata berisi `privkey`/`password` → pastikan tidak muncul di body).
- `deactivate_keys`: id di luar daftar tidak tersentuh.
- Validasi batch: satu entri cacat (nama > 64, `base_url` > 255, `status` > 20 / berbuntut spasi / ber-control-char, `expires_at` berskala detik, metadata > 64 KiB, `api_key` kosong) → 400 **dan** `COUNT(*)` provider/key tidak berubah serta revisi tidak naik.
- Status asing: `status: "dead"` → 200, baris tersimpan dengan `is_active = 0`, dan id-nya **tidak** ada di `key_ids`.
- Reload: revisi naik dan snapshot generation bertambah (hot-swap terjadi).
- Fail-closed: `LoadCatalogSnapshot` gagal → snapshot lama tetap melayani **dan** cursor revisi tidak ikut maju (diimplementasikan `syncer.go:135-138`, diuji `syncer_test.go:TestSyncOnce_PreservesPreviousSnapshotWhenReloadFails`).

**Verifikasi:** `go test -race ./internal/server/... ./internal/storage/turso/...`

**Rollback:** revert commit; endpoint hilang; harvester masih mode DB (belum berubah sampai Fase 3).

---

### Fase 2b — Endpoint Operator (Dashboard)

**Tujuan:** `providers` (dan `api_keys`) punya penulis resmi lewat dashboard — menutup lubang "`providers` tanpa penulis".

**Rute (guard `authorizeAdmin`):**

```
GET|POST         /api/providers
GET|PUT|DELETE   /api/providers/{id}
GET              /api/providers/{id}/keys
POST             /api/providers/{id}/keys      (batch upsert)
PATCH            /api/keys/{id}                (status / is_active / expires_at)
DELETE           /api/keys/{id}                (hard delete — hak operator, I7)
```

**Test:** mengikuti pola `tenants_admin_test.go` (`newTenantsTestServer` → `newProvidersTestServer`, dst.), termasuk `TestProvidersAdmin_RequiresAdmin`.

**OpenAPI:** perluas `openapi-admin.yaml`; lebarkan regex drift test ke `^/api/(tenants|providers)`.

**Frontend:** tidak di fase ini. UI menyusul sebagai `frontend/src/modules/N-providers/` + klien bertipe di `frontend/src/services/api.ts`, supaya endpoint diverifikasi lewat test API dulu.

---

### Fase 2c — Store Move (murni pindah)

Pindahkan `ListProviders` (`store.go:1706`), `ListProviderKeys` (`store.go:1752`), `ProviderSummary`, `ProviderKeyDTO` dari `store.go` ke `provider_store.go`. Commit **"move only"**, nol perubahan perilaku, dibuktikan test yang sudah ada tetap hijau.

---

### Fase 3 — Migrasi Harvester ke API

**Lokasi:** repo harvester (di luar repo ini). Urutan wajib: **endpoint siap dulu, baru pindah** — selama Fase 2 belum live, harvester tetap satu-satunya penulis INSERT, jadi tidak pernah ada dua penulis bersamaan (I4).

**Langkah:**
1. Klien API + konfigurasi `FIREFLY_API_URL`; mode transisi: tetap baca DB untuk state internal, tetapi menulis **hanya** lewat API.
2. **Outbox durabel + backoff + jitter.** Firefly kini dependency keras harvesting: siklus harvest yang gagal = *defer*, bukan crash; upsert yang belum terkirim disimpan lokal dan di-retry (idempoten, jadi aman diulang).
3. Matikan jalur tulis DB.

**Verifikasi end-to-end (runbook):**
- Snapshot `SELECT id, provider_id, api_key FROM api_keys ORDER BY id` sebelum & sesudah siklus → id lama identik, tidak ada duplikat.
- `COUNT(*)` kenaikan == `created` dari respons.
- Key baru langsung terpakai routing (reload terpicu, cek generation).
- Matikan Firefly sesaat → harvester defer + retry, tidak crash, tidak kehilangan data.
- Multi-node: harvester diarahkan ke **satu** node primary (bukan node yang sedang drain); node kedua konvergen ≤ 2 interval sync.

**Rollback:** flag di harvester kembali ke DB-write selama kredensial DB belum dicabut.

**Status (2026-09-21):** kode selesai di repo harvester — `core/firefly_api.py` (klien + outbox durabel), routing di `core/database.py`, flush backlog di `cli.py`/`cron_runner.py`, tes `tests/test_firefly_api.py` (16 hijau), dan dokumentasi operasional di `README.md` harvester §4. Detail deviation: §9.1.

**Cara mengaktifkan per node (urutan yang disarankan):**

```bash
# 1. Set kredensial service di node Firefly (bukan kredensial DB)
FIREFLY_HARVESTER_TOKEN=<token-service>     # wajib; tanpa ini endpoint balas 503

# 2. Set di node harvester
FIREFLY_API_URL=http://<node-firefly>:<port>
FIREFLY_HARVESTER_TOKEN=<token-service>
# FIREFLY_WRITE_MODE dibiarkan `auto` -> langsung mode API karena URL+token terisi

# 3. Jalankan satu tick manual, lalu periksa
#    - log: "[Firefly API] N key tersinkron (revision=..., reloaded=true, batch=1)"
#    - respons `created` == kenaikan COUNT(*) api_keys
#    - snapshot id sebelum/sesudah identik
#    - health-check outbox: pending == 0, dead == 0

# Rollback (selama kredensial DB belum dicabut)
FIREFLY_WRITE_MODE=db
```

Catatan: entri yang menunggu di outbox saat rollback **tidak** dipindahkan ke DB otomatis — jalur DB hanya menangani hasil panen berikutnya. Kirim dulu backlog (`flush_pending()`), baru rollback.

---

### Fase 4 — Cabut Akses DB Harvester

1. Cadangan penuh DB + tabel terkait.
2. Revoke kredensial **read-write** Turso harvester. Langkah ini **mengakhiri jalur rollback tulis**: `FIREFLY_WRITE_MODE=db` hanya berfungsi selama kredensial tulis masih ada. Yang boleh tersisa adalah token **read-only** — ia mempertahankan kemampuan **baca** harvester selama masa observasi (usulan 7 hari — angka menunggu keputusan Anda), bukan sebagai jalur tulis cadangan. Bila O14 belum diputuskan, baca ini masih dibutuhkan, jadi jangan cabut lebih dulu.
3. Hapus token read-only — **hanya** setelah O14 terjawab (harvester tidak lagi butuh baca langsung, atau sudah ada endpoint baca berbasis service-token).

**Gate:** persetujuan eksplisit — operasi ireversibel dari sisi akses pada DB live bersama.

**Prasyarat baru (temuan Fase 3, dirinci di Lampiran B):** cabut akses DB hanya sah setelah penulis lain ikut bermigrasi/dipensiunkan — W2 `bai/cron_worker.py`, W3 `tokenharbor/sync_to_vault.py`, W4 `scripts/conduit_auth.py`, W5 `scripts/backfill_grok_cli_tokens.py` (Lampiran B.0/B.5). W5 semula **tidak dapat** bermigrasi penuh tanpa kapabilitas rotate-secret; blocker **O11 sudah ditutup 2026-09-22** (`PATCH /api/keys/{id}` menerima `api_key`, id bertahan, dan secret di-mirror ke `upstream_credentials`), jadi yang tersisa untuk W5 hanyalah keputusan operasional (B.5). Tambahkan pengecekan **O13**: jalur restore `scripts/db.py` / `scripts/restore_hermes.sh` bisa tetap menulis ke DB yang sama dengan kredensial dari `.env`, sehingga pencabutan token harvester saja tidak cukup. Selama ini belum beres, token read-only pun sebaiknya tetap dipertahankan lebih panjang dari 7 hari — dan token **read-write** yang tertanam di `token_vault/client.py:14–21` wajib dirotasi terlepas dari hasil Fase 4 (Lampiran B.1b).

**Rollback:** pulihkan kredensial dari cadangan.

---

## 7. Pemetaan Commit (satu fase = commit/PR yang bisa diverifikasi mandiri)

| # | Commit | File inti |
|---|---|---|
| 1 | Fase 1 — adopsi DDL | `schema.go`, test fresh-deploy |
| 2 | Fase 1b (jika disetujui) — dedupe + unique index | migrasi + laporkan dampak |
| 3 | Fase 2a — endpoint mesin + auth service | `service_auth.go`, `harvester_api.go`(+test), `turso_manager.go`, `router.go`, `openapi.go`, `openapi-harvester.yaml`(+test), `main.go` |
| 4 | Fase 2b — endpoint operator | `providers_admin.go`(+test), `openapi-admin.yaml`, `openapi_admin_test.go` |
| 5 | Fase 2c — store move | `provider_store.go`, `store.go` (pindah saja) |
| 6 | Fase 3 — migrasi harvester | repo harvester: `core/firefly_api.py`(baru), `core/database.py`, `cli.py`, `cron_runner.py`, `tests/test_firefly_api.py`(baru), `README.md` |
| 7 | Fase 4 — cabut akses DB | operasional (tanpa kode di repo ini); prasyarat: W2/W3/W5 bermigrasi atau pensiun + rotasi kredensial `token_vault` (Lampiran B.5, B.1b) |

### 7.1 Split nyata yang bisa di-build (2026-09-22)

Draf per-fase di atas **tidak bisa** dipecah persis menjadi commit 3–5: `internal/server/router.go` memuat rute Fase 2a **dan** 2b, dan `internal/storage/turso/provider_store.go` adalah file baru yang berisi transaksi sync (2a), store operator (2b), sekaligus kode hasil pindah (2c). Memaksanya menjadi 3 commit berarti menulis ulang file baru itu tiga kali dan tiap commit antara harus tetap compile. Jadi split nyata yang lolos verifikasi mandiri:

| # | Commit | File | Verifikasi |
|---|---|---|---|
| 0 | **Format** — `gofmt -w` atas drift yang sudah ada di HEAD (K13) | 52 file di luar cakupan migrasi (mis. `cmd/loadtest/*`, `internal/adapter/**`, `internal/integration/**`, `internal/security/oauth/**`, `internal/transport/{httpx,upstream}/**`, `internal/watch/watch.go`); 2 file sisa (`internal/server/{router,settings}.go`) ikut ter-format di commit 2/2d karena diff-nya memang sudah disentuh di sana | `gofmt -l .` (di luar `node_modules`) kosong; `go build ./...` + `go vet ./...` + `go test -count=1 -race ./...` hijau — murni gaya, nol perubahan semantik |
| 1 | **Fase 1** — adopsi DDL | `internal/storage/turso/schema.go`, `schema_test.go`(baru), `store_test.go` (DDL tiruan dihapus + seed kolom NOT NULL) | berdiri sendiri: `go test -race ./internal/storage/turso/...` |
| 2 | **Fase 2a+2b+2c** — permukaan tulis provider/key | baru: `service_auth.go`, `harvester_api.go`+`_test.go`, `providers_admin.go`+`_test.go`, `provider_store.go`+`_test.go`, `syncer_test.go`, `openapi/openapi-harvester.yaml`, `openapi_harvester_test.go` · diubah: `cmd/firefly/main.go`, `internal/server/{router,openapi,settings,turso_manager}.go`, `openapi/openapi-admin.yaml`, `openapi_admin_test.go`, `internal/storage/turso/{store,errors,models,syncer}.go` | `go test -count=1 -race ./internal/server/... ./internal/storage/turso/...` + drift test |
| 2d | **Permukaan baca legacy hint-only + *Bind Provider Keys*** (K3-c) | `internal/server/settings.go` + `providers_admin_test.go` (+1 tes; baris `settings.go` sudah tercakup commit 2), `frontend/src/services/{schema,api}.ts`, `frontend/src/modules/2-upstreams/UpstreamModal.tsx` | `go test -race ./internal/server/...` + `npx tsc --noEmit` + `npm run build`; boleh berdiri sendiri atau menempel commit 2 |
| 2e | **Dashboard provider/key** (K4) | baru: `frontend/src/modules/8-providers/{ProvidersView,KeyPatchModal}.tsx` · diubah: `frontend/src/{modules/registry.tsx,core/layout/Header.tsx,hooks/useKeyboardShortcuts.ts}` | `npx tsc --noEmit` + `npm run build` + verifikasi browser terhadap stub lokal (bukan DB live); boleh menempel commit 2 |
| 2f | **Perketat permission file kredensial + satu penulis katalog bersama** (K16) | baru: `internal/config/files.go`+`files_test.go`, `internal/server/catalog_files.go`, `internal/storage/turso/client_test.go` · diubah: `internal/config/source.go`, `internal/server/{settings,tenants_admin}.go`+`{settings,tenants_admin}_test.go`, `internal/storage/turso/client.go` | `go test -count=1 -race ./internal/config/... ./internal/server/... ./internal/storage/turso/...`; berdiri sendiri |
| 2g | **DRY util frontend + `maskSecret` idempoten** | baru: `frontend/src/lib/secret.ts` · diubah: `frontend/src/modules/2-upstreams/{UpstreamModal,KeyRingSlotList}.tsx` | `npx tsc --noEmit` + `npm run build` + verifikasi browser; boleh menempel commit 2d |
| 3 | **Dokumen** | `docs/harvester-ownership-plan.md` (termasuk Lampiran A & B); entri `CHANGELOG.md` ditulis saat release, bukan di sini | read-only |
| — | Fase 3 | **di luar repo ini** — workspace harvester tidak punya git, jadi "commit"-nya = salinan berkas + runbook §Fase 3 | 16/16 tes lokal via shim |

Fakta siap-commit (2026-09-22): `go test -count=1 -race ./...` **nol kegagalan** (flake throughput `TestKeyRing_ThroughputExceeds10MillionOps` lolos pada run ini), `go vet ./...` bersih, 16/16 tes harvester hijau.

**Commit dieksekusi (2026-09-22, menutup K15):** split di tabel atas dijalankan apa adanya, dengan satu penyesuaian yang dipaksa aturan "tiap commit antara harus tetap compile": `internal/config/files.go` dan `internal/server/catalog_files.go` ikut **commit 2** (bukan 2f), karena `settings.go` di commit 2 sudah memanggil `writeCatalogFiles` yang memakai `config.WriteSecretFile`. `internal/config/files_test.go` tetap di 2f supaya commit 2 tidak membawa test yang meng-assert perilaku `EnsureConfigFiles` (bagian dari 2f).

| Baris | Commit | Isi |
|---|---|---|
| 0 | `81b52c9` `style: apply gofmt to pre-existing formatting drift` | 50 file — 52 kandidat dikurangi `internal/server/{router,settings}.go`, yang format-nya menempel di commit 2 karena diff-nya memang ditulis ulang di sana |
| 1 | `1bc3fab` `feat(turso): create the providers and api_keys tables in Firefly's schema` | Fase 1 — DDL + `schema_test.go` + `store_test.go` |
| 2 | `8a6f792` `feat(server): provider/key write surfaces and the harvester sync endpoint` | Fase 2a+2b+2c, permukaan baca hint-only (K3-c), penulis katalog bersama, `config/files.go` |
| 2d+2g | `31a09bc` `feat(frontend): bind provider keys server-side and share the duplicated helpers` | Bind Provider Keys + `lib/{format,datetime,secret}.ts` + DTO operator |
| 2e | `18cf0cf` `feat(frontend): providers dashboard for the native credential catalog` | Tab Providers (K4) |
| 2f | `c14c51a` `fix(security): keep credentials owner-only on disk` | K16 — `0600`/`0700` + satu penulis katalog untuk kedua jalur simpan |
| 3 | *(commit dokumen ini)* | `docs/harvester-ownership-plan.md`, `README.md` (rute + `-harvester-token`), `AGENTS.md` (peta paket + rute) |
| rilis | *(commit rilis `feat: release v1.13.0 - …`)* | entri `CHANGELOG.md` + bump `frontend/src/core/constants.ts` dan `frontend/package.json` |

Tiap commit diverifikasi pada pohon kerja commit itu sendiri, bukan hanya di akhir: baris 0 `gofmt -l` bersih; baris 1 `go test -count=1 -race ./internal/storage/turso/...`; baris 2 `go build ./...` + `go vet ./...` + `go test -count=1 -race ./internal/server/... ./internal/storage/turso/... ./internal/config/...`; baris 2d+2g & 2e `npx tsc --noEmit` + `npm run build`; baris 2f `gofmt -l` + `go build ./...` + `go vet ./...` + test tiga paket yang sama.

**Drift `gofmt` — diputuskan & sudah dibereskan (K13, 2026-09-22):** `gofmt -l` menandai **54 file**, termasuk `internal/server/{router,settings}.go`. Drift-nya sudah ada di HEAD (CI hanya menjalankan `go test -race`, tanpa gate format) dan isinya murni gaya: sortir ulang blok import + baris kosong di ujung file, nol perubahan semantik (contoh: `internal/watch/watch.go` `fsnotify` pindah urutan dalam grup yang sama). Keputusan: **perbaiki**, bukan biarkan — jadi commit format tersendiri (baris 0 pada tabel di atas) supaya commit migrasi berikutnya bebas bising gaya. Diterapkan dengan `gofmt -w` atas 54 file itu; `gofmt -l .` kini **kosong**; `go build ./...` ok, `go vet ./...` bersih, `go test -count=1 -race ./...` hijau seluruh paket.

**Kesehatan test — diputuskan dibiarkan (K14, 2026-09-22):** dua flake report-only, keduanya timing/lingkungan dan bukan bug fungsional — (1) penyegar OAuth dan (2) `TestKeyRing_ThroughputExceeds10MillionOps` bila dijalankan di bawah `./...` (bersaing CPU dengan paket lain; lolos saat paketnya dijalankan sendiri). Keputusan: **biarkan**, tidak diinvestigasi pada fase ini; suite tetap hijau pada run normal dan tak satu pun menyentuh jalur migrasi. Dicatat sebagai risiko yang diketahui, bukan blocker commit.

**K15 — commit ditunda (2026-09-22):** split di tabel atas sudah final dan terverifikasi, tetapi commit **belum dijalankan**: tidak ada satu pun commit dibuat, working tree dibiarkan apa adanya (semua perubahan tetap uncommitted). Tabel di atas berlaku sebagai instruksi kapan pun commit diputuskan; sampai itu terjadi, verifikasi dijalankan ulang lebih dulu (`gofmt -l .` kosong → `go build ./...` → `go vet ./...` → `go test -count=1 -race ./...`).

**K15 ditutup (2026-09-22, atas permintaan eksplisit "update dokumentasi, commit, dan push tag"):** verifikasi dijalankan ulang lebih dulu di working tree (`gofmt -l .` kosong, `go build ./...` ok, `go vet ./...` bersih, `go test -count=1 -race ./...` hijau), lalu kedelapan baris dieksekusi berurutan dengan verifikasi per commit — lihat tabel "Commit dieksekusi" di atas untuk hash dan buktinya. Rilis ditandai sebagai **v1.13.0** (minor, bukan patch): perubahan ini menambah permukaan API baru (`/api/providers*`, `POST /api/harvester/sync`), tab dashboard baru, dan kepemilikan DDL — bukan sekadar perbaikan. Tag anotasi `v1.13.0` + `main` di-push ke `origin`, lalu artifact rilis diverifikasi (checksum + `--version`).

---

## 8. Risiko & Mitigasi

| Risiko | Dampak | Mitigasi |
|---|---|---|
| Jendela dua penulis | Baris duplikat / race | Urutan keras: endpoint live (Fase 2) sebelum harvester berhenti menulis DB (Fase 3); verifikasi hitung baris |
| `id` tidak stabil saat upsert | Ref `-key-<id>` rusak, KeyRing salah petakan | Upsert by natural key mempertahankan `id`; test khusus; larangan hard-delete dari harvester (I1) |
| Rahasia bocor | Kebocoran kredensial | Respons hanya hint; `RedactHandler`; test masking (I2) |
| Reload storm | KeyRing rebuild berulang, rotasi kolaps | `updated_at` hanya untuk perubahan struktural (I3); endpoint batch, bukan per-key |
| Firefly jadi SPOF harvesting | Pasokan key berhenti | Outbox durabel + backoff di harvester; siklus gagal = defer; alarm bila outbox menumpuk |
| Duplikat `(provider_id, api_key)` | `UNIQUE` index gagal dibuat | Fase 0 menghitung lebih dulu; dedupe terpisah dengan persetujuan (O3) |
| Node lain tidak konvergen | Key baru tidak terpakai di node lain | `pushed` di respons; `TriggerSync` di node penulis; sync 15 detik di node lain; uji 2 node |
| Body/batch besar | Tekanan memori & lock store | Batas 32MB; batas jumlah item; `Mode merge` hanya |
| Satuan timestamp campur (detik/milidetik) di `api_keys` | Sinyal reload `MAX(updated_at)` tidak andal untuk update in-place baris aktif | Endpoint baru **selalu** bump `catalog_revisions` (reload deterministik) + tulis ms; normalisasi baris lama opsional (O9) |
| `account_metadata` memuat kredensial sensitif | Kebocoran privkey/mnemonic/password lewat API atau log | Tidak pernah dibaca-kembali lewat API (I2); hanya ditulis; test masking wajib |
| Regresi jalur data plane | Trafik produksi terganggu | I5: endpoint tidak menyentuh `forwardEndpoint`/`RelaySSE`; `go test -race ./...` wajib hijau |

---

## 9. Definisi Selesai

- [x] `providers` + `api_keys` dibuat oleh `schema.go`; deploy baru tidak butuh harvester untuk bootstrap. *(Fase 1)*
- [x] `POST /api/harvester/sync` idempoten: retry tidak mencetak baris, `id` tidak berubah, respons tanpa rahasia. *(Fase 2a — `harvester_api_test.go`, `provider_store_test.go`)*
- [x] Auth service fail-closed, terpisah dari `authorizeAdmin`, teruji (401/503). *(Fase 2a)*
- [x] CRUD operator `/api/providers*` hidup dengan drift test OpenAPI hijau. *(Fase 2b)*
- [x] Tidak ada penulis yang bisa menghapus kedaluwarsa baris milik orang lain secara diam-diam (`expires_at` tiga-intent: absent/null/number di **ketiga** permukaan tulis), dan rotasi secret operator mempertahankan `id` sambil mem-mirror `upstream_credentials`. *(O11 + O12 — `provider_store_test.go`, `providers_admin_test.go`, assert prose di kedua spec OpenAPI)*
- [x] Nilai berbentuk hint mask ditolak di permukaan tulis operator — hint adalah satu-satunya bentuk credential yang pernah dilihat pemanggilnya. *(I2 perluasan)*
- [x] Tidak ada permukaan baca Firefly yang mengembalikan secret provider: `/api/turso/providers/{id}/keys` & `/api/turso/keys` hanya mengembalikan `api_key_hint`, dan dashboard memakai **Bind Provider Keys** (pooling di server) sebagai pengganti "Import from Database". *(K3-c — `TestLegacyProviderKeysSurfaceReturnsHintsOnly`, `providers_admin_test.go`; `fetchTursoProviderKeyHints` + `handleBindProviderKeys` di frontend)*
- [x] Dashboard punya UI penuh untuk mengelola provider & key tanpa menyentuh DB dan tanpa pernah memegang secret mentah (K4): tab **Providers** (order 8, hotkey `8`) dengan daftar provider + tabel key per provider, buat/ubah/hapus provider, batch upsert key, patch status/routability/kedaluwarsa, dan rotasi secret in-place — semuanya lewat `/api/providers*` + `/api/keys/{id}`. *(Fase 2e — `ProvidersView.tsx`, `KeyPatchModal.tsx`; verifikasi browser terhadap stub lokal)*
- [ ] Harvester: nol kredensial DB; semua CRUD lewat API; outbox durabel terbukti saat Firefly dimatikan.
      *Kode Fase 3 SELESAI + teruji lokal (16/16); yang belum: pembuktian end-to-end di staging dan pencabutan kredensial DB (Fase 4).*
- [ ] Verifikasi: `go test -race ./...` hijau; snapshot `id` sebelum/sesudah identik; konvergensi 2 node teruji.
      *`go test -race` hijau untuk paket tersentuh; snapshot `id` + konvergensi 2 node butuh staging.*
- [ ] Dokumen ini diperbarui ke status **DONE** dengan tanggal & catatan deviation.
      *Menunggu Fase 4 selesai.*

### 9.1 Status per fase (diperbarui 2026-09-22)

| Fase | Status | Bukti |
|---|---|---|
| 0 — introspeksi | Selesai | Lampiran A (read-only, DB live tidak diubah) |
| 1 — adopsi DDL | Selesai · `1bc3fab` | `schema.go`, `schema_test.go` (3 tes), `go test -race` hijau |
| 1b — dedupe | Dibatalkan | 0 duplikat (Lampiran A.4) |
| 2a — endpoint mesin | Selesai · `8a6f792` | `service_auth.go`, `harvester_api.go` (+8 tes), `provider_store.go` (+17 tes), `syncer_test.go`, drift test |
| 2b — endpoint operator | Selesai · `8a6f792` | `providers_admin.go` (+14 tes), `openapi-admin.yaml` |
| 2b+ — rotate secret (O11) & intent `expires_at` (O12) | Selesai · `8a6f792` | `provider_store.go` (3 tes baru), guard hint-termask di `providers_admin.go` (batch + PATCH), 2 tes HTTP di `providers_admin_test.go`, 2 tes spec OpenAPI, prose `openapi-admin.yaml`/`openapi-harvester.yaml` |
| 2c — store move | Selesai · `8a6f792` | `provider_store.go` vs `store.go` (pindah saja, `store.go` −101 baris) |
| 2d — permukaan baca legacy jadi hint-only (K3-c) | Selesai · backend `8a6f792`, frontend `31a09bc` | `settings.go` (`legacyProviderKey` + `maskSecret`), test `TestLegacyProviderKeysSurfaceReturnsHintsOnly`; frontend: `schema.ts`/`api.ts`/`UpstreamModal.tsx` (`Bind Provider Keys`), `tsc --noEmit` + `npm run build` hijau |
| 2e — dashboard provider/key (K4) | Selesai · `18cf0cf` | `modules/8-providers/{ProvidersView,KeyPatchModal,ProviderFormModal,KeyUpsertModal,ProviderKeyTable}.tsx` + registrasi tab (order 8, hotkey `8`); `tsc --noEmit` + `npm run build` hijau; verifikasi browser (stub lokal, bukan DB live): state kosong/loading/503+Retry, batch upsert menolak hint-termask (0 POST), create/delete provider termasuk 409 |
| 2f — permission file kredensial (K16) | Selesai · `c14c51a` | `config/files.go`, `server/catalog_files.go`, `turso/client.go` (`ensureLocalDir`), test mode di `files_test.go`/`settings_test.go`/`tenants_admin_test.go`/`client_test.go`; verifikasi live instance terisolasi |
| 2g — DRY util frontend + `maskSecret` idempoten | Selesai · `31a09bc` | `lib/{format,datetime,secret}.ts` dipakai lintas modul; `maskSecret` identik dengan server dan idempoten |
| 3 — migrasi harvester | Kode selesai, belum deploy | `core/firefly_api.py`, routing `core/database.py`, 16 tes lokal hijau |
| 4 — cabut akses DB | **Belum dimulai** | Gate: persetujuan eksplisit (ireversibel) + O10, O13, O14 |

**Checklist "Test wajib" Fase 2a (status 2026-09-22):** auth matrix · replay idempoten (metering utuh) · 409 provider mismatch · masking respons **dan** log · deaktivasi eksplisit · reload (revisi → generation) · 413 body · batch cacat all-or-nothing · status asing jadi inert — **semua ada**. Yang sempat **tidak** dibuat adalah bullet terakhir ("`LoadCatalogSnapshot` gagal → snapshot lama tetap melayani"); kini ditutup oleh `internal/storage/turso/syncer_test.go` (paket itu sebelumnya tidak punya test sama sekali). Test-nya sudah diverifikasi punya gigi: memindahkan `s.lastKnownRevision = currentRev` ke sebelum pemanggilan load membuat test gagal dengan pesan "the repaired catalog was never applied".

**Deviation Fase 3 (yang berbeda dari draf rencana):**

1. **Bukan hanya harvester yang menulis DB.** Ditemukan **empat** penulis lain di luar `core/database.py`: `tokenharbor/sync_to_vault.py`, `bai/cron_worker.py`, `scripts/conduit_auth.py`, dan `scripts/backfill_grok_cli_tokens.py`. Draf rencana mengasumsikan harvester adalah satu-satunya pembuat baris. Konsekuensi: Fase 4 wajib mencakup keempatnya (migrasi / pensiun), atau kredensial DB tidak boleh dicabut. **Inventaris lengkap + verdict ekspresibilitas: Lampiran B.**
2. **`refresh_key_tokens()` tidak punya padanan di kontrak sync.** Natural key endpoint adalah `api_key`; mengganti nilai secret = baris baru di server. Mode API menolak operasi ini secara eksplisit (bukan gagal senyap). Fungsi ini sudah tidak dipanggil di jalur hidup. **Ditutup 2026-09-22:** kapabilitas rotate-secret id-preserving sudah ada di permukaan operator (`PATCH /api/keys/{id}` dengan `api_key`, **O11**; kasus nyata yang membutuhkannya: W5, Lampiran B.2). Sisi harvester tetap belum memanggilnya — itu pekerjaan Fase 3 lanjutan, bukan blocker Firefly.
3. **Mode tulis tiga-nilai (`auto`/`api`/`db`)**, bukan boolean. `auto` = perilaku lama selama URL+token belum diisi, sehingga penerapan bisa bertahap per node; `db` = flag rollback yang diminta rencana; `api` = fail-closed (tanpa fallback senyap ke DB) supaya I4 "satu penulis per tabel" tidak bocor lewat jalur darurat.
4. **`expires_at` dinormalkan ke milidetik di klien.** `HarvestResult` mendokumentasikan detik, tetapi sebagian provider (grok/qoder) mengisi milidetik, dan baris DB existing memakai milidetik. Ambang normalisasi `10**11` dipakai supaya kedua satuan aman.
5. **Metadata >64 KiB di-omit per key**, bukan membatalkan batch: key tetap tersimpan (nilai yang penting), blob metadata dilewati dengan peringatan berhint. Alternatif "tolak seluruh batch" akan menahan key yang sehat.
6. **Outbox tanpa klaim/lock.** Dua proses yang flush bersamaan dapat mengirim entri yang sama dua kali; endpoint-nya idempoten sehingga itu tidak berbahaya. Ini disengaja: lebih sederhana dan tidak ada risiko entri "tersangkut" karena pemilik klaim mati.
7. **Repo harvester bukan git repository.** Tidak ada jaring rollback VCS, jadi seluruh perubahan Fase 3 dibuat aditif + di balik flag (`FIREFLY_WRITE_MODE=db` mengembalikan jalur lama tanpa mengubah kode).

---

## 10. Perintah Verifikasi

```bash
# 1. Test suite penuh dengan race detector
go test -count=1 -race ./...

# 2. Paket yang tersentuh (loop iterasi cepat)
go test -race ./internal/server/... ./internal/storage/turso/...

# 3. Fresh-deploy: pastikan DDL benar-benar membuat tabel
go test -race -run 'TestSchema|TestFresh' ./internal/storage/turso/...

# 4. Verifikasi manual (staging, kredensial read-only)
#    Snapshot id sebelum & sesudah satu siklus harvester:
#    sqlite3 <replica.db> "SELECT id, provider_id, api_key FROM api_keys ORDER BY id"
```

---

## 11. Status Keputusan

### 11.1 Sudah diputuskan & terimplementasi (O1–O9 disetujui 2026-09-21; O11–O12 2026-09-22)

| # | Keputusan | Wujud di kode |
|---|---|---|
| O1 | snapshot tunggal, bukan granular | `SyncProviderKeys` — satu batch = satu transaksi = satu bump revisi |
| O2 | deaktivasi hanya lewat id eksplisit | `req.DeactivateKeys`; tidak ada `deactivate_missing` di server |
| O2b | tolak perpindahan provider pada upsert | `ErrKeyProviderMismatch` → 409; jalur operator tetap bisa dengan `"reassign": true` |
| O3 | ~~dedupe~~ **batal** — Fase 0 membuktikan 0 duplikat | natural key tetap `UNIQUE(api_key)` global |
| O4 | token service di env var, fail-closed | `FIREFLY_HARVESTER_TOKEN` (`main.go:75`, `:109`, `:512`); kosong → 503 |
| O5 | prefix `/api/harvester/*` | `router.go:221`; tanpa CORS/OPTIONS (server-to-server) |
| O6 | pertahankan `/api/turso/providers*` lama | tetap read-only; konsolidasi ditunda ke fase frontend. **Sejak 2026-09-22 permukaan key-nya hint-only** (`api_key_hint`, `maskSecret`): dashboard tidak lagi butuh nilai mentah karena pooling berjalan di server (`store.go:473-500`), dan "Import from Database" diganti aksi **Bind Provider Keys** |
| O8 | `account_metadata` tidak pernah diekspos | write-only di kedua jalur, diafirmasi test di `harvester_api_test.go` & `providers_admin_test.go` |
| O9 | normalisasi timestamp baris lama ditunda | tulis milidetik; `expires_at` berskala detik **ditolak di boundary** sejak 2026-09-22 |
| O11 | endpoint operator "rotate secret, pertahankan `id`" | `PATCH /api/keys/{id}` menerima `api_key`: `UPDATE api_keys SET api_key` **dan** mirror ke `upstream_credentials` dalam transaksi yang sama (loader `store.go:504-527` memprioritaskan salinan `uc.secret`, jadi rotation tanpa mirror akan terus melayani secret lama); secret milik baris lain → `ErrAPIKeyTaken` → 409. Test: `TestPatchProviderKeyRecord_RotatesSecretInPlace`, `TestPatchProviderKeyRecord_RefusesTakenOrMalformedSecret`, `TestProvidersAdmin_RotateSecretKeepsIdentityAndMasksHint` |
| O12 | semantik tiga-intent `expires_at` di **ketiga** permukaan tulis | field jadi `json.RawMessage`; `expiryIntent()` memisahkan *absent* (pertahankan) / `null` (hapus) / *number* (ganti). Dulu *absent* = hapus → hazard B.3.1 hilang. Test: `TestUpsertProviderKeyRecords_ExpiryIntentIsThreeWay`, `TestProvidersAdmin_BatchExpiryIntentSurvivesTheWire` |

**Kontrak yang menyusul bersamanya (2026-09-22):** `maxKeySecretLen = 4096` menjadi satu sumber batas lebar secret; jalur sync/operator **batch** hanya menegakkan lebar (menolak satu nilai legacy cacat akan membekukan batch hingga 10.000 key), sedangkan **PATCH** menegakkan bentuk penuh (`validateKeySecret`: kosong / >4096 / padding / control char). Kedua permukaan tulis operator menolak nilai yang **berbentuk hint mask** (`isMasked`) dengan 400 — permukaan itu hanya pernah menampilkan `api_key_hint`, jadi menerima hint berarti menyimpan string tampilan sebagai kredensial yang bisa dirutekan. Jalur harvester sengaja tidak diberi guard ini: ia satu-satunya penulis yang memegang secret asli dan tidak pernah melihat hint.

### 11.2 Masih terbuka (memblokir Fase 4)

| # | Pertanyaan | Rekomendasi |
|---|---|---|
| Masa observasi | berapa hari token read-only dipertahankan setelah revoke | 7 hari — **naikkan** bila O10/O13/O14 belum beres |
| O10 | nasib penulis legacy W2/W3/W5 (Lampiran B.5) | W2 pensiunkan, W3 route ke outbox Fase 3, W5 kini **bisa** bermigrasi (O11 sudah ada) — tinggal keputusan operasional |
| O13 | apakah `scripts/db.py` / `scripts/restore_hermes.sh` menunjuk DB Turso Firefly yang sama? | konfirmasi di server; bila ya, rotasi kredensial harus mencakup `.env` (B.3.3) |
| O14 | **kapabilitas baca pengganti belum ada di jalur API** | Sync adalah jalur **tulis**; tidak ada endpoint baca berbasis service-token. Selama kredensial read-only harvester masih ada itu tidak tampak sebagai masalah, tapi begitu ia dicabut (Fase 4 butir 3) tidak ada lagi jalan bagi harvester untuk membaca balik barisnya sendiri — audit, rekonsiliasi natural key, dan `acquire_lru_key`/`record_feedback` yang hari ini vestigial (B.3) tidak akan pernah bisa dihidupkan lewat API. Permukaan baca Firefly yang ada hanyalah `GET /api/providers/{id}/keys` (admin) dan `GET /api/turso/providers/{id}/keys` (legacy, admin, **hint-only sejak 2026-09-22** — temuan "raw `api_key`" di Riwayat Revisi 2026-09-22 sudah ditutup). Putuskan sebelum token read-only dicabut: tambah read endpoint service-token (dengan hint, bukan secret), atau nyatakan resmi harvester tidak butuh baca |

---

## Lampiran A — Hasil Fase 0 (2026-09-21, read-only)

**Metode:** tiga batch query via libSQL HTTP `/v2/pipeline` ke `libsql://firefly-dickymuliafiqri.aws-eu-west-1.turso.io` (SQLite/libSQL 3.47.0). Hanya `SELECT` dan `sqlite_master`/`pragma_*` — nol statement tulis. Kredensial dibaca dari `configs/turso.json` dan tidak pernah ditampilkan. Tidak ada nilai `api_key` mentah yang di-query; agregat duplikat memakai `GROUP BY` tanpa `SELECT` nilai key.

### A.1 DDL persis (dari `sqlite_master`)

```sql
CREATE TABLE providers (id INTEGER NOT NULL, name VARCHAR (64) NOT NULL, base_url VARCHAR (255) NOT NULL,
  description VARCHAR (255), is_active INTEGER NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  PRIMARY KEY (id), UNIQUE (name))

CREATE TABLE api_keys (id INTEGER NOT NULL, provider_id INTEGER NOT NULL, api_key TEXT NOT NULL,
  status VARCHAR (20) NOT NULL, is_active INTEGER NOT NULL, expires_at BIGINT, last_used_at BIGINT NOT NULL,
  total_requests BIGINT NOT NULL, account_metadata JSON, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  PRIMARY KEY (id), FOREIGN KEY (provider_id) REFERENCES providers (id) ON DELETE CASCADE, UNIQUE (api_key))
```

### A.2 Index & FK

| Objek | Definisi |
|---|---|
| Index | `idx_provider_active_keys ON api_keys (provider_id, is_active, status, last_used_at)` |
| Index implisit | `sqlite_autoindex_api_keys_1` (`UNIQUE(api_key)`), `sqlite_autoindex_providers_1` (`UNIQUE(name)`) |
| FK masuk | `api_keys → providers` (CASCADE) · `upstreams → providers` (sudah ada di Firefly) · `upstream_credentials → api_keys` (sudah ada di Firefly) |
| Trigger/view | Tidak ada |

### A.3 Angka kunci

| Metrik | Nilai |
|---|---|
| Providers | 14 total, 14 aktif (`bai, grok, atria, orcarouter, unorouter, tokenharbor, aihubmix, dahl, unikey, outlook, vyce, qoder, nodeloc, tokentable`) |
| `api_keys` | 2.801 total · 2.563 aktif · 238 expired+inactive |
| Status | hanya `active` dan `expired` |
| **Duplikat** | **0 grup** (`(provider_id, api_key)` maupun varian `TRIM`) |
| `provider_id` NULL/orphan | 0 |
| `expires_at` terlewat tapi masih aktif | 0 |
| `api_key` kosong/NULL | 0 |
| `account_metadata` | terisi 2.801/2.801 (maks 28.552 byte) |
| `upstream_credentials` | 430 baris · **0** ter-link ke `api_key_id` (FK belum pernah dipakai; identitas berjalan lewat string `ref`) |
| `catalog_revisions` | revision = 137 |

### A.4 Temuan (berdampak ke rencana)

1. **`UNIQUE(api_key)` bersifat global**, bukan per-provider → natural key upsert = `api_key` saja. Keputusan baru **O2b**: key yang dikirim dengan provider berbeda harus ditolak, bukan dipindahkan diam-diam.
2. **Nol duplikat** → Fase 1b (dedupe) **dibatalkan seluruhnya**.
3. **Index existing sudah cukup** (`idx_provider_active_keys` mencakup `(provider_id, is_active, status)`); usulan index baru di draf awal dibatalkan. `updated_at` tanpa index — tidak masalah pada 2.801 baris.
4. **Satuan timestamp campur:** `created_at` seluruhnya **detik** (2.801 baris), `updated_at` **1.396 detik / 1.405 milidetik**, `expires_at` seluruhnya **milidetik** (286 baris non-NULL), `providers.created_at/updated_at` seluruhnya detik. Konsekuensi: `MAX(updated_at)` (sinyal reload `GetKeysState`) didominasi nilai milidetik; baris baru ber-satuan detik **tidak menaikkannya** — insert masih tertangkap lewat `COUNT(*)`, tetapi update in-place baris aktif tidak. Mitigasi: endpoint baru **selalu** bump `catalog_revisions` dalam transaksi yang sama (reload deterministik), dan menulis milidetik. Normalisasi baris lama = **O9** (opsional, ditunda).
5. **`account_metadata` adalah vault kredensial**, bukan metadata biasa. Nama key JSON yang terisi: `privkey` (1.578 baris), `mnemonic` (1.578), `password` (653), `access_token` (397), `secondary_api_key` (402), `refresh_token`/`id_token` (238), `wallet_address` (1.581), `email` (635), `username`, `fingerprint`, dst. → memperluas invariant **I2**: kolom ini hanya ditulis (pass-through), tidak pernah di-return API mana pun.
6. **`providers.base_url` bersih** — 0 dari 14 memuat `@` (tidak ada kredensial tertanam di URL).
7. `providers.description` (kolom yang tidak saya perkirakan di draf awal) ada dan harus ikut diadopsi di DDL.
8. Tabel bisnis (`users`/`orders`/`subscriptions` dari BUSINESS.md) **tidak ada** di DB ini — isinya murni tabel Firefly + harvester.

### A.5 Catatan eksekusi

Skrip introspeksi bersifat sekali-pakai (di `/tmp`, tidak masuk repo). Untuk mengulang: ambil query dari §A.1–A.3 dan jalankan ulang dengan cara yang sama — semuanya `SELECT` murni.

---

## Lampiran B — Inventaris penulis langsung `providers`/`api_keys` (2026-09-22, read-only)

**Metode:** grep menyeluruh di cerminan workspace `~/hermes` (`/Users/dickymuliafiqri/.hermes/hermes_workspace`) atas pola `from token_vault`, `ensure_provider(`, `insert_key(`, `= get_engine(`, `ApiKey(`, `Provider(`, `session.add`/`session.commit`, `flag_modified`, dan SQL mentah (`INSERT|UPDATE|DELETE` ke `api_keys`/`providers`). Tidak ada file yang diubah; **tidak ada satu pun query ke DB live** pada lampiran ini.

**Hasil:** himpunan tertutup — **5 modul** Python (+1 salinan cadangan) membuat engine `token_vault`, dan **nol SQL mentah** di sisi harvester: semua tulis DB lewat dua primitif ORM di `token_vault/client.py`.

### B.0 Ringkasan

| # | Penulis | Baris tulis | Jenis | Penjadwalan (bukti lokal) | Ekspresibel via `POST /api/harvester/sync`? | Opsi migrasi |
|---|---|---|---|---|---|---|
| W1 | `firefly_harvester/core/database.py` | `:96 :102 :109` (single) · `:214 :220 :228 :229` (batch) · `:162–185` (refresh) | insert + update | `scripts/atria_cron_worker.sh` → `cron_runner.py:125,178`; `run_atria_farm.sh` → `atria_farm.py:464`; `cli.py:103` | **Ya** — sudah dirutekan Fase 3; kecuali `refresh_key_tokens` (padanan operator **sudah ada** lewat O11, tetapi klien Fase 3 masih menolaknya secara eksplisit) | Siap `FIREFLY_WRITE_MODE=api` (tugas Fase 3 selesai) |
| W2 | `automations/bai/cron_worker.py` | `:58 :66 :87` | insert-only | **Tidak ada satu pun referensi scheduler** di workspace | Ya (insert key + metadata wallet) | **Pensiunkan** (tercakup `cron_runner.py --provider bai`) |
| W3 | `automations/tokenharbor/sync_to_vault.py` | `:27 :56 :72` | insert-only dari berkas teks | dipanggil `tokenharbor.py:739` (akhir run CLI) | Ya (insert + metadata) | **Route** ke `firefly_api`, atau jalankan sekali via API lalu hapus |
| W4 | `scripts/conduit_auth.py` (+ salinan di `scripts_backup_email_migration_20260919_011958/`) | `:132 :138 :149` | insert-only sekali-pakai | manual (signup) | Ya | Tidak aksi; dokumentasikan sebagai one-off |
| W5 | `firefly_harvester/scripts/backfill_grok_cli_tokens.py` | `:106–120` (fix-units) · `:169–174` (mark dead) · `:180–197` (**rename secret in-place**) · `:203 :206` | update in-place | manual (`--limit/--dry-run`), runbook satu-kali | **Ya sepenuhnya** sejak 2026-09-22 — lihat B.2 | Rename lewat `PATCH /api/keys/{id}` + `api_key` (**O11** sudah tersedia); mark-dead & fix-units lewat sync |

### B.1 Pustaka tulis bersama — `token_vault/`

| Elemen | Lokasi | Efek ke DB |
|---|---|---|
| Engine replika | `client.py:23–45` (replika `/tmp/turso_vault_<worker>.db` di `:32`, string koneksi di `:33`) | `PRAGMA foreign_keys=ON`, WAL, busy_timeout 5s |
| `sync_push` / `sync_pull` | `client.py:47` / `client.py:53` | dorong/tarik perubahan ke Turso |
| `ensure_provider` | `client.py:59–71` (add `:69`, commit `:70`) | **INSERT-only** `providers`; nama sudah ada → baris lama dikembalikan, `base_url` **tidak pernah dikoreksi** |
| `insert_key` | `client.py:73–95` (add `:93`, commit `:94`) | **INSERT-only** `api_keys`; `api_key` sudah ada → dikembalikan tanpa UPDATE (`:80–83`) |
| `acquire_lru_key` | `client.py:97–130` (commit `:122`) | tulis `last_used_at`/`total_requests` — **nol caller** (vestigial) |
| `record_feedback` | `client.py:132–150` (commit `:150`) | tulis `status` (`cooldown`/`dead`) — **nol caller** (vestigial) |

**B.1a — satuan timestamp adalah sidik jari penulis.** `models.py:27,49` men-stamp `created_at`/`updated_at` dengan `int(time.time())` (**detik**); Firefly menulis `time.Now().UnixMilli()` (**milidetik**). Ini menjelaskan Lampiran A.4 (`created_at` 100% detik; `updated_at` 1.396 detik vs 1.405 milidetik). **Pakaiannya:** satu agregat `SELECT` yang membucket `updated_at` per satuan memperkirakan baris mana yang masih disentuh penulis Python — cara paling murah membuktikan W2/W3/W5 benar-benar aktif. Tidak saya jalankan sekarang (butuh persetujuan; lihat B.6).

**B.1b — kredensial DB read-write masih tertanam di pustaka.** `client.py:14–21` berisi URL default DB Firefly **dan** JWT default ber-klaim tulis (`"a":"rw"`); token yang sama masuk string koneksi di `:33` sehingga dapat muncul di pesan error SQLAlchemy. Fase 4 wajib memuat **rotasi** token ini, bukan sekadar berhenti memakainya — dan rotasi tetap perlu walau Fase 4 batal, karena nilai itu sudah terekspos di berkas. (Nilai token sengaja tidak disalin ke dokumen ini.)

### B.2 Rincian W5 — apa yang tidak bisa diungkapkan kontrak sync

Natural key endpoint adalah `api_key` (`provider_store.go:280–284`; `UNIQUE(api_key)` per Lampiran A.2), sehingga mengganti nilai secret = **baris baru dengan `id` baru** dan memutus ref `<upstream>-key-<id>` di `upstreams`/`upstream_credentials` (juga `extractAPIKeyID` di flusher). Yang **tetap** bisa diungkapkan:

- `--fix-units` (`expires_at` detik→ms + revival) → kirim key dengan `expires_at` ms.
- mark-dead (`status="dead"`, `is_active=0`) → kirim key dengan status apa pun; jalur sync **tidak punya allowlist `status`** (yang ditegakkan hanya bentuk: ≤20 karakter, tanpa control char/spasi tepi — lihat B.3.2), jadi `dead` lolos dan tetap tak-routable. Allowlist `keyStatuses` hanya berlaku di permukaan operator.

Pilihan untuk rename secret: (a) endpoint operator **rotate-secret id-preserving** (**O11** — PATCH `api_key_id` + secret baru, mirror ke `upstream_credentials`) — **tersedia sejak 2026-09-22**; (b) backfill tetap pakai akses DB sebagai runbook satu-kali dengan persetujuan; (c) tulis baris baru + matikan yang lama (`id` berubah, perlu re-bind upstream manual).

### B.3 Hazard lintas penulis (temuan baru; relevan untuk semua fase setelah ini)

1. **`expires_at` bisa ter-wipe diam-diam.** Di `upsertKeyTx`, `account_metadata` yang tidak dikirim berarti "pertahankan blob tersimpan" (`:317–318`), tetapi `expires_at` yang tidak dikirim berarti **`SET NULL`** (`:313–314`, `:323–338`). Klien Fase 3 hanya menyertakan `expires_at` bila truthy (`firefly_api.py:418–420`). Belum merusak jalur panen hari ini (key baru ⇒ baris baru), tapi menjadi jebakan begitu W2/W3/W5 migrated — penulis yang update baris milik penulis lain tanpa membawa expiry akan menghapus kedaluwarsanya. Kandidat **O12**: semantik "omit = keep" untuk `expires_at` dengan sentinel `clear_expiry` eksplisit (pola sudah ada di operator PATCH, `ProviderKeyPatch.ClearExpiry` `:471`).
   **Sudah ditangani (2026-09-22) sebagai O12.** `expires_at` di ketiga permukaan tulis kini `json.RawMessage` dan didekode oleh `expiryIntent()`: **absent = pertahankan nilai tersimpan**, `null` = hapus, *number* = ganti. Tidak ada lagi `SET NULL` implisit di `upsertKeyTx` — jalur sync (`SyncProviderKeys`), batch operator (`UpsertProviderKeyRecords`), dan PATCH (`PatchProviderKeyRecord`) memakai satu fungsi yang sama, jadi satu konvensi berlaku di mana-mana. Yang **tidak** berubah: `status` tetap otoritatif saat dihilangkan (default `active`), dan `account_metadata` tetap omit=keep. Bukti: `TestUpsertProviderKeyRecords_ExpiryIntentIsThreeWay` (level sync) dan `TestProvidersAdmin_BatchExpiryIntentSurvivesTheWire` (level HTTP) — test terakhir itu perlu karena hanya marshalling JSON yang bisa membedakan "field tidak ada" dari "field bernilai null".
2. **`status` tidak divalidasi di jalur sync.** Efek ganda: inilah yang membuat mark-dead W5 ekspresibel (ia menulis `status="dead"`), tapi sekaligus membuat typo status (mis. `"Active"`) menghasilkan baris tak-routable tanpa error. Bandingkan dengan operator: `keyStatuses` (`:478–483`) = `{active, deactivated, expired, revoked}` — **`dead` dan `cooldown` tidak termasuk**, padahal DB live hari ini (Lampiran A.3) hanya memuat `active`/`expired`. Kalau validasi disamakan ke jalur sync, nilai `dead` harus diputuskan lebih dulu (masuk allowlist, atau dipetakan ke `revoked`). Rekomendasi kecil pra-Fase-4: validasi + dokumentasikan pemetaannya.
   **Sudah ditangani (2026-09-22), dengan alasan tidak membuat allowlist di jalur sync.** Yang ditegakkan adalah **bentuk**, bukan himpunan nilai: `validateStatusShape` menolak status > 20 karakter (lebar kolom hasil adopsi DDL — SQLite sendiri tidak menegakkannya, dan baris berlebih membuat operator PATCH menolak menyentuh baris itu), berisi kontrol karakter, atau ber-spasi di depan/belakang. Nilai asing yang bentuknya baik (`dead`, `cooldown`, dst.) tetap **pass-through** dan tetap tak-routable (hanya `active` yang routable): menolak nilai tak-dikenal akan membekukan **seluruh batch** — termasuk 10.000 key lain — hanya karena Firefly belum pernah melihat satu state, dan itu lebih buruk daripada typo. Typo tetap tertangkap di sisi klien: `key_ids` hanya memuat key routable, jadi `created == 1` tapi `key_ids` kosong adalah sinyal visual bahwa status tidak diterima. Allowlist `keyStatuses` tetap **khusus operator** (PATCH dan batch upsert) karena di sana yang ditulis adalah perintah lifecycle, bukan snapshot milik penulis lain — sekarang keduanya menegakkan allowlist yang sama, bukan hanya PATCH.
3. **Penulis di luar `token_vault`: jalur restore Hermes.** `scripts/db.py` (`HermesDB`, replika `data/hermes.db`; kredensial dari `TURSO_DATABASE_URL`/`TURSO_AUTH_TOKEN` environment) dan `scripts/restore_hermes.sh:69–82` menjalankan `init_schema()` + `flush_sync()`. Tabel milik `db.py` bukan `providers`/`api_keys`, **tetapi** jika URL-nya menunjuk DB Turso Firefly yang sama, `unzip -qo` replika lama + push = jalur tulis ke **semua** tabel, dan ia **tidak terpengaruh oleh pencabutan token harvester**. Saya tidak memeriksa isi `.env` (kredensial) — ini harus Anda konfirmasi sendiri (**O13**).
4. **Tidak ada penulis Python yang men-deaktivasi provider.** Semua jalur memakai `ensure_provider` (`is_active=1`), jadi `providers.is_active=0` hanya bisa datang dari Firefly. Konsekuensi lain: `base_url` yang salah dari penulis lama beku selamanya sampai dikoreksi lewat API/operator.

### B.4 Ko-penulis di dalam Firefly (bukan pelanggaran I4 — Firefly adalah pemilik baru)

| Lokasi | Yang ditulis | Catatan |
|---|---|---|
| `flusher.go:270–281` | `total_requests`, `last_used_at` **tanpa** `updated_at` | sesuai I3 (metering tidak memicu reload) |
| `flusher.go:288–300`, `:305–336` | `status='revoked'`/`'deactivated'`, hard `DELETE api_keys` | struktural ⇒ bump `updated_at` |
| `store.go:1696–1715` | sweep `DeactivateExpiredKeys` → `expired` | ms |
| `store.go:1736–1763`, `:1765–1780` | `DeactivateKey`, `DeleteKey` | + mirror `upstream_credentials` |
| `provider_store.go:104–121` | deaktivasi eksplisit dari payload sync | |
| `provider_store.go:667`, `:757`, `:845–848`, `:1065`, `:1131` | CRUD operator (`POST/PUT/DELETE /api/providers`, `PATCH/DELETE /api/keys`) | hard delete tersedia untuk operator |

**Implikasi:** I4 "satu penulis per tabel" baru benar-benar terpenuhi setelah W2/W3/W5 berhenti menulis DB; hari ini tabel masih punya 5 penulis eksternal + writer internal Firefly.

### B.5 Rekomendasi (bahan keputusan — belum ada aksi)

1. **W1** — sudah siap; tinggal deploy mode `api` sesuai runbook Fase 3.
2. **W2** — **pensiunkan** `bai/cron_worker.py`. Fungsinya tercakup `cron_runner.py --provider bai`; baris lamanya mudah dikenali dari `account_metadata.harvested_by = "cron_worker_5m"`.
3. **W3** — **route**: ganti blok tulis `:22–72` dengan `firefly_api.enqueue_results(...)` + `flush_pending()` (dua-duanya sudah ada dari Fase 3), atau jalankan satu kali terhadap endpoint lalu hapus skripnya.
4. **W4** — tidak ada aksi; catat sebagai one-off (hapus salinan `scripts_backup_email_migration_*/` saat pembersihan).
5. **W5** — blocker **O11 sudah tertutup** (2026-09-22): rename secret in-place kini sah lewat `PATCH /api/keys/{id}` dengan `api_key` (id bertahan, `upstream_credentials` di-mirror). Yang tersisa hanyalah memindahkan skripnya ke jalur API (pekerjaan sisi harvester, bukan Firefly); kalau skrip itu memang tidak akan pernah dijalankan lagi, nyatakan pensiun.
6. **Gate Fase 4** — revisi: jangan cabut akses DB sebelum butir 2, 3, 5 diputuskan **dan** B.3.3 (jalur restore) terkonfirmasi; kalau `db.py` menunjuk DB yang sama, rotasi token juga harus menyasar `.env` server, bukan hanya `client.py`. Butuh keputusan **O14** lebih dulu: token read-only tidak boleh dihapus selama harvester masih membaca `api_keys` langsung.

### B.6 Batasan bukti

- Direktori harvester **bukan git repository** → tidak ada riwayat yang membuktikan siapa menjalankan apa, kapan.
- `cron/jobs.json` lokal kosong dan seluruh path di skrip `.sh` menunjuk `/root/...` → **penjadwalan sebenarnya (crontab/systemd timer di server) belum terbukti dari lokal**. Yang bisa saya buktikan lokal hanyalah: tidak ada satu pun file di workspace yang mereferensikan `bai/cron_worker.py` maupun `backfill_grok_cli_tokens.py` selain dirinya sendiri.
- Perintah konfirmasi (semuanya read-only, jalankan di server): `crontab -l`; `systemctl list-timers --all`; `ps -eo pid,lstart,cmd | grep -E "cron_runner|sync_to_vault|backfill_grok"`; dan agregat bucket satuan `updated_at` menurut B.1a.

---

## Riwayat Revisi

| Tanggal | Perubahan |
|---|---|
| 2026-09-21 | Draf awal — kondisi awal terverifikasi, skema folder/file, fase 0–4, risiko, definisi selesai |
| 2026-09-21 | **Fase 0 dijalankan** — Lampiran A ditambahkan; Fase 1b dibatalkan (0 duplikat); natural key key = `api_key` (UNIQUE global); Fase 1 disesuaikan ke DDL persis + index existing; I2 diperluas ke `account_metadata`; O2b/O8/O9 ditambahkan |
| 2026-09-21 | **Fase 1 diimplementasikan** — DDL `providers`/`api_keys` + `idx_provider_active_keys` masuk `schemaDDL` (urutan deklarasi dipindah ke sebelum `upstreams` agar semua FK menunjuk tabel yang sudah ada); `schema_test.go` BARU (fresh DB file, idempoten + baris terjaga, tabel pra-ada tidak di-ALTER); tabel tiruan di `setupTestDB` dihapus. Verifikasi: `go test -race ./...` hijau. Belum di-commit; DB live belum tersentuh |
| 2026-09-21 | **Fase 2a diimplementasikan** — `service_auth.go` (token service terpisah dari `authorizeAdmin`, fail-closed 503 bila belum dikonfigurasi, verifikasi konstanta-waktu), `harvester_api.go` (`POST /api/harvester/sync`: validasi envelope + batas 500 provider/10.000 key/20.000 deaktivasi/64 KiB metadata, pemetaan error 400/409/500, respons `{ProviderSyncResult, reloaded}`), `turso.SyncProviderKeys` (satu transaksi: upsert provider by name, upsert key by `UNIQUE(api_key)` dengan id stabil, deaktivasi eksplisit + mirror ke `upstream_credentials`, bump `catalog_revisions`), `TursoManager.TriggerSync`, wiring `FIREFLY_HARVESTER_TOKEN` di `main.go`, spec `openapi-harvester.yaml` + drift test. Verifikasi: `go test -race ./internal/server/... ./internal/storage/turso/...` hijau |
| 2026-09-21 | **Fase 2b diimplementasikan** — `providers_admin.go` (CRUD `/api/providers*` + `/api/keys/{id}`, guard `authorizeAdmin`, `account_metadata` tidak pernah dibaca-kembali, hint termask dari secret), store operator (`ListProviderRecords`, patch key, batch upsert dengan `allowReassign` operator-only + pembersihan `upstream_credentials` lama saat reassign), `openapi-admin.yaml` diperluas + regex drift test dilebarkan. Frontend sengaja belum (sesuai rencana). Verifikasi: `go test -race ./internal/server/...` hijau |
| 2026-09-21 | **Fase 2c diimplementasikan** — `ListProviders`/`ListProviderKeys` + tipe DTO pindah dari `store.go` ke `provider_store.go` (murni pindah, nol perubahan perilaku); test lama tetap hijau tanpa diubah |
| 2026-09-21 | **Fase 3 diimplementasikan (sisi harvester)** — `core/firefly_api.py` BARU: klien `POST /api/harvester/sync` (stdlib saja) + outbox SQLite durabel (WAL, 0600, partial unique index per natural key, state pending/sent/dead, backoff 30s→1 jam ±20% jitter, `requeue_dead()`, `stats()`), `write_mode()` = `auto`/`api`/`db` (default `auto` → DB selama URL+token belum diisi = nol perubahan perilaku; `api` fail-closed; `db` = flag rollback). `core/database.py` merutekan `persist_harvest_result`/`persist_harvest_batch` ke API bila mode `api` (kegagalan jaringan = defer, bukan exception), `refresh_key_tokens` menolak di mode API. `cli.py`/`cron_runner.py` flush backlog outbox tiap tick. Test BARU `tests/test_firefly_api.py` (16 tes). Verifikasi lokal: 16/16 hijau via shim stdlib (pytest tidak terpasang di venv harvester) |
| 2026-09-21 | **Temuan Fase 3 (memengaruhi Fase 4)** — penulis `providers`/`api_keys` **tidak hanya harvester**: `tokenharbor/sync_to_vault.py`, `bai/cron_worker.py`, dan `scripts/backfill_grok_cli_tokens.py` juga menulis langsung ke DB (yang terakhir melakukan rename `api_key` in-place + tandai mati + perbaikan satuan — **tidak dapat diungkapkan** lewat kontrak sync karena `api_key` adalah natural key). Repo harvester **bukan git repository** (tidak ada jaring rollback VCS) → semua perubahan Fase 3 bersifat aditif + di balik flag. Kredensial DB read-write Turso masih tertanam di `token_vault/client.py:20` |
| 2026-09-22 | **Lampiran B ditambahkan (read-only, nol perubahan kode, nol query ke DB live)** — inventaris himpunan-tertutup 5 penulis `providers`/`api_keys` (`file:line`, jenis tulis, bukti penjadwalan, verdict ekspresibilitas, opsi migrasi). Fakta baru: satuan timestamp = sidik jari penulis (detik Python vs milidetik Go); `expires_at` yang tidak dikirim **di-SET NULL** oleh `upsertKeyTx` (sedangkan `account_metadata` di-pertahankan) → hazard saat penulis legacy dimigrasikan; `status` tidak divalidasi di jalur sync sementara allowlist operator (`keyStatuses`) tidak memuat `dead`/`cooldown`; jalur restore `scripts/db.py` + `restore_hermes.sh` adalah penulis potensial di luar token harvester (O13); `acquire_lru_key`/`record_feedback` vestigial (nol caller). §11 bertambah **O10–O13**; prasyarat Fase 4 dan baris commit #7 diperbarui merujuk Lampiran B |
| 2026-09-22 | **Pass kesiapan commit (§7.1)** — `go test -count=1 -race ./...` nol kegagalan, `go vet ./...` bersih, 16/16 tes harvester hijau. Split commit nyata ditulis: Fase 2a/2b/2c **tidak dapat** dipecah jadi tiga commit mandiri karena `router.go` dan `provider_store.go` dipakai bersama → digabung jadi satu commit terverifikasi; Fase 3 tetap di luar repo (harvester tanpa git). Temuan report-only baru: drift `gofmt` pada 54 file **sudah ada di HEAD** (CI tidak punya gate format) → di luar cakupan migrasi ini |
| 2026-09-22 | **Audit planned-vs-actual + penutupan gap** — satu "Test wajib" Fase 2a ternyata belum pernah dibuat: fail-closed reload. Ditutup oleh `internal/storage/turso/syncer_test.go` BARU (paket itu sebelumnya tidak punya test sama sekali) — memetakan katalog tidak ter-compile + bump revisi → `SyncOnce` error, snapshot lama tetap melayani, dan siklus berikutnya tetap menerapkan perbaikan **tanpa** bump baru (membuktikan cursor tidak maju saat gagal). Punya gigi: mutasi `s.lastKnownRevision = currentRev` ke sebelum load membuat test gagal. Artifact disinkronkan ke kenyataan: header status (bukan lagi "belum ada kode"), §3.2 + §11 sekarang memisahkan O1–O9 (disetujui & terimplementasi) dari O10–O13 (terbuka), §5 diperluas dengan 5 file yang benar-benar berubah (`settings.go`, `store.go`, `store_test.go`, `errors.go`, `syncer.go`) termasuk alasan mutex syncer, §9.1 diperbarui + daftar status test wajib, §7.1 menyertakan `syncer_test.go`. Temuan report-only yang **tetap terbuka**: legacy `GET /api/turso/providers/{id}/keys` masih mengembalikan `api_key` mentah (`settings.go:622` → `ProviderKeyDTO.APIKey`), ditunda bersama konsolidasi frontend (O6) |
| 2026-09-22 | **Validasi bentuk payload ditegakkan di boundary (Firefly saja)** — di `provider_store.go`: `validateProviderShape`/`validateKeyShape`/`validateStatusShape`/`validateExpiry` dipanggil sebagai pre-pass **sebelum** transaksi dibuka (satu entri cacat = 400, nol baris berubah), plus batas lebar kolom DDL pada POST/PUT provider. Aturan milidetik yang sama kini berlaku di jalur operator (batch upsert + PATCH), bukan hanya di sync. `MaxKeyMetadataBytes` pindah ke store sebagai satu sumber batas 64 KiB untuk kedua jalur tulis. **Keputusan: bentuk, bukan allowlist**, untuk `status` di jalur sync (lihat Lampiran B.3 hazard #2); `keyStatuses` kini ditegakkan di **POST batch juga**, bukan hanya PATCH. Spec `openapi-harvester.yaml` + `openapi-admin.yaml` diperbarui (`maxLength`, `minimum: 100000000000`, prose all-or-nothing). Test baru: batch cacat (11 kasus) dan status-asing-inert di kedua level. Verifikasi: `go test -count=1 -race ./internal/storage/turso/... ./internal/server/...` hijau + `go test -count=1 -race ./...` dua kali tanpa kegagalan. Belum di-commit; DB live belum tersentuh |
| 2026-09-22 | **O11 + O12 ditutup (Firefly saja)** — (O12) `expires_at` menjadi `json.RawMessage` di ketiga permukaan tulis dan didekode satu fungsi `expiryIntent()`: absent = pertahankan, `null` = hapus, number = ganti; hazard B.3.1 (wipe diam-diam) hilang, dan jalur sync kini memakai konvensi yang sama dengan PATCH operator. (O11) `PATCH /api/keys/{id}` menerima `api_key`: roteksi in-place yang **mempertahankan `id`** (satu-satunya alasan jalur ini ada — ref `<upstream>-key-<id>` turunan id), `UNIQUE(api_key)` milik baris lain → `ErrAPIKeyTaken` → 409, dan secret di-mirror ke `upstream_credentials` pada transaksi yang sama karena loader `store.go:504-527` memprioritaskan `uc.secret` di atas join `api_keys` (rotasi tanpa mirror = secret lama tetap melayani). Metering (`total_requests`, `last_used_at`) tidak tersentuh; `updated_at` tetap bump. **Guard keamanan baru yang ditemukan saat menulis test:** permukaan operator hanya pernah menampilkan `api_key_hint`, jadi nilai berbentuk hint (`sk-...wxyz`, `[REDACTED]`) kini ditolak 400 di batch **dan** PATCH — kalau tidak, string tampilan tersimpan sebagai kredensial yang bisa dirutekan; jalur harvester sengaja tidak diberi guard (ia pemegang secret asli). Lebar secret dipusatkan di `maxKeySecretLen = 4096`: batch hanya menegakkan lebar, PATCH menegakkan bentuk penuh (`validateKeySecret`). Test baru: 3 di level store, 2 di level HTTP (termasuk pembuktian bahwa beda absent/null **hanya** terlihat setelah marshalling), 2 assert prose di spec OpenAPI; `openapi-admin.yaml`/`openapi-harvester.yaml` dikoreksi (sebelumnya menyatakan "omitted clears" = salah), PATCH kini mendaftarkan 409 + `api_key`. Verifikasi: `go vet ./...` bersih, `go test -count=1 -race ./internal/storage/turso/... ./internal/server/...` hijau. §11 dirombak: O11/O12 pindah ke 11.1, **O14** (tidak ada jalur baca pengganti untuk harvester) ditambahkan, Fase 4 butir 2 dikoreksi (token read-only **bukan** jalur rollback tulis), W5 menjadi ekspresibel penuh. **Belum di-commit; DB live belum tersentuh** |
| 2026-09-22 | **Permukaan baca legacy jadi hint-only + pengganti impor sisi server (K3-c)** — temuan report-only 2026-09-22 ("legacy `GET /api/turso/providers/{id}/keys` masih mengembalikan `api_key` mentah") **ditutup**: `handleGetTursoProviderKeys` kini memetakan tiap baris ke `legacyProviderKey` dan hanya mengirim `api_key_hint` hasil `maskSecret` (field `api_key` hilang dari respons), dengan `ListProviderKeys` diberi catatan bahwa nilai kembaliannya adalah secret asli yang **wajib** dimask di setiap permukaan HTTP. Pengganti jalur impor tidak perlu endpoint baru: `store.go:473-500` sudah mem-pool key provider aktif di sisi server begitu upstream punya `provider_id`, jadi dashboard hanya butuh `id` provider + daftar id key aktif. Frontend: `TursoKeyDTO`/`TursoKeysResponse` → `TursoKeyHintDTO`/`TursoKeyHintsResponse`, `fetchTursoProviderKeys` + `useFetchTursoKeysMutation` → `fetchTursoProviderKeyHints`, dan "Import from Database" → **Bind Provider Keys** (`handleBindProviderKeys`): menyetel `provider_id`, memakai `base_url` provider bila belum diisi, membuang entri lokal yang ref-nya (`-key-<id>`) sudah tidak aktif di DB, dan tidak pernah menyalin secret ke browser. Aman terhadap sanitizer form (`settings.go:855-945`): entri termask di-rehidrasi dari KeyRing berdasarkan ref, jadi tidak ada string tampilan yang tersimpan sebagai kredensial. Test: `TestLegacyProviderKeysSurfaceReturnsHintsOnly` (kedua rute legacy, assert 200 + absennya secret + `api_key_hint` + `provider_id`); verifikasi `tsc --noEmit` bersih, `npm run build` sukses. O6/O14 di §11 disinkronkan: O14 tetap terbuka untuk jalur **service-token**, temuan raw-key ditandai closed. **Belum di-commit; DB live belum tersentuh** |
| 2026-09-22 | **Dua observasi report-only dari kerja O11/O12 (belum ada aksi)** — (1) `internal/config/builder.go:25` `upstreamNameRe = ^[a-z0-9][a-z0-9\-_]{0,63}$`, sehingga nama fixture tes `"upA"` (huruf besar) membuat snapshot **tidak dapat di-compile**; test lama yang memakainya (`TestProvidersAdmin_PatchMirrorsRoutabilityOntoBoundCredentials`, `TestProvidersAdmin_KeyReassignPolicy`) diam-diam mendapat `reloaded:false` dan tidak pernah mengeceknya. Test baru saya pakai nama yang sah (`pool-a`) + assert `reloaded:true`. (2) Syncer memanggil `DeactivateExpiredKeys` tiap reload (log `turso syncer deactivated expired keys`) → baris kedaluwarsa ditulis ulang jadi `status=expired, is_active=0` **dan** `updated_at` bump, jadi entri batch yang menghilangkan `status` pada baris expired sah melapor `updated:1`; test tidak boleh mengasumsikan `unchanged` untuk baris semacam itu (kolom `expires_at` sendiri tetap terjaga). Keduanya temuan perilaku, bukan bug produksi |
| 2026-09-22 | **K4 dikerjakan: dashboard provider/key (fase 2e)** — tab **Providers** baru (`modules/8-providers/`, order 8, hotkey `8`, sengaja di akhir agar tidak ada direktori modul/shortcut yang di-renumber) berisi daftar provider + tabel key per provider, CRUD provider, batch upsert key, patch status/routability/kedaluwarsa, dan rotasi secret in-place — semuanya lewat API operator yang sudah ada, browser **tidak pernah** memegang secret mentah. Dua bug nyata ketemu & dibereskan saat verifikasi browser (stub lokal, DB live tidak disentuh): (a) efek penjaga seleksi mereset `selectedId` ke `providers[0]` selagi refetch invalidasi masih terbang → panel provider yang baru dibuat balik sendiri; diperbaiki dengan `seenIdsRef` yang membedakan "belum pernah terdaftar" dari "terdaftar lalu dihapus"; (b) dialog hapus provider menghitung `active_keys` alih-alih `keys.length` sehingga menulis "0 credential" padahal ada 2 key. Verifikasi: `tsc --noEmit` + `npm run build` hijau, browser bebas error React, termasuk jalur 409 `Provider In Use` dan penolakan hint-termask di sisi klien (0 POST sampai ke stub). Artifact disinkronkan: baris §7.1 **2e**, butir §9, baris §9.1. **Belum di-commit; DB live belum tersentuh** |
| 2026-09-22 | **K13 diputuskan: drift `gofmt` diperbaiki** — temuan report-only "54 file ditandai `gofmt -l`, tidak saya ubah" ditutup: `gofmt -w` diterapkan atas 54 file itu (murni sortir ulang blok import + baris kosong ekor; nol perubahan semantik). Dampak pada split commit: `internal/server/{router,settings}.go` sudah dipakai commit 2/2d, jadi format keduanya menempel di sana dan **hanya 52 file** yang bisa menjadi commit format mandiri (dicatat sebagai baris **0** di tabel §7.1 — ditaruh paling depan agar commit migrasi bebas bising gaya). Verifikasi: `gofmt -l .` kosong, `git diff --stat -- '*.go'` = 64 file (54 format + 10 file migrasi), `go build ./...` ok, `go vet ./...` bersih, `go test -count=1 -race ./...` hijau seluruh paket. **Belum di-commit; DB live belum tersentuh** |
| 2026-09-22 | **K14 diputuskan: dua flake dibiarkan** — (1) penyegar OAuth dan (2) `TestKeyRing_ThroughputExceeds10MillionOps` di bawah `./...` dinilai flake timing/lingkungan (paket lain berebut CPU; lolos saat dijalankan sendiri), bukan bug fungsional, dan tak satu pun menyentuh jalur migrasi. Tidak ada perubahan kode; dicatat sebagai risiko diketahui di §7.1 — bukan blocker commit |
| 2026-09-22 | **K15 diputuskan: commit ditunda** — split commit (§7.1 baris 0/1/2/2d/2e/3) sudah final & terverifikasi, tapi **belum dieksekusi**: nol commit dibuat, seluruh perubahan tetap di working tree. Kapan pun commit dijalankan, ulangi verifikasi lebih dulu (`gofmt -l .` kosong → `go build ./...` → `go vet ./...` → `go test -count=1 -race ./...`). DB live tetap belum tersentuh |
| 2026-09-22 | **Audit pasca-implementasi: empat bug backend diperbaiki + test pengunci** — (1) **Batch status tidak bercermin ke `upstream_credentials`.** `upsertKeyTx` hanya menulis `api_keys`, padahal loader `store.go:504-527` **memprioritaskan `uc.secret`** di atas join `api_keys`, dan mirror yang ada baru menangani rotasi (`:1369-1372`) dan PATCH — sehingga key yang di-deaktivasi lewat sync/batch tetap melayani pool lewat baris salinan. Kini `mirrorCredentialDeactivationTx` (`:453-472`) dipanggil di `upsertKeyTx:350-355` **sebelum** short-circuit "unchanged" (itulah yang membuat replay payload sama bisa meng-konvergensi baris yang ditinggalkan penulis lama, bukan melapor no-op kosong), mencocokkan **dua bentuk ikatan**: `api_key_id = ?` **atau** `ref LIKE '%-key-<id>'` — bentuk kedua adalah salinan pool dari settings-save yang membawa ref tanpa FK. Guard `(is_active = 1 OR status <> ?)` membuat replay tidak meng-churn `updated_at`. (2) **PATCH no-op tetap menulis.** Patch yang hasilnya identik dengan nilai tersimpan dulu tetap menjalankan `UPDATE` + bump `catalog_revisions` → seluruh instance me-rebuild snapshot tanpa perubahan; kini `rotating`/`dirty` (dirty = ada perubahan nyata: status/active/expiry/metadata/pindah provider) menggerbangi penulisan vs `catalogRevisionTx` (baca revisi tanpa bump) dan `Pushed`. (3) **`expires_at: 0` / negatif di PATCH.** Handler memang mengubah angka apa pun jadi `*int64`, tapi store memperlakukan nilai tak-positif sebagai "hapus kedaluwarsa" — kini nilai itu dijangkau `validateExpiry` (`minExpiryMillis`) sehingga 400 dan `expires_at` tersimpan utuh; hanya `null` eksplisit yang menghapus. (4) **Pesan parse JSON membocorkan body.** `describeJSONError` melaporkan `json.SyntaxError.Offset` ("malformed JSON at byte offset N") alih-alih mengutip byte/isi body — body di permukaan ini memuat secret. Test pengunci: `TestUpsertProviderKeyRecords_StatusChangeRetiresBoundCredentials` (pool 3 ref → 1 ref, replay `Unchanged == 1` + `updated_at` tak tersentuh, reaktivasi mengembalikan lewat jalur 2a sementara baris salinan tetap pensiun), `TestPatchProviderKeyRecord_NoOpPatchLeavesChangeSignalsAlone` (revisi & `updated_at` tak bergerak untuk patch no-op, bergerak untuk transisi nyata), `TestProvidersAdmin_PatchRejectsNonPositiveExpiryWithoutClearing`, `TestProvidersAdmin_MalformedJSONNamesTheOffsetNotTheBody`. **Asimetri sisa (report-only):** `flusher.go:314`/`:324-327` dan `store.go:1752-1756`/`:1769` (`DeactivateKey`, `DeleteKey`) mencocokkan `upstream_credentials` **hanya** lewat `api_key_id`, jadi baris salinan ber-ref telanjang tidak ikut dipensiunkan di jalur itu — tetapi baris salinan itu lahir dari settings-save tanpa FK, dan jalur-jalur tersebut adalah revoke/delete atas key yang punya baris `api_keys`; keduanya tidak pernah bertemu. Verifikasi: `gofmt -l internal/... ` kosong, `go vet` bersih, `go test -count=1 -race ./...` seluruh paket hijau. **Belum di-commit; DB live belum tersentuh** |
| 2026-09-22 | **K16 diputuskan: permission file kredensial diperketat (0600/0700)** — temuan saat verifikasi browser: `configs/upstreams.json` (kunci provider mentah) dan `configs/tenants.json` (kunci gateway tenant) ditulis `0644` oleh `internal/server/settings.go:473/:483` dan oleh template startup `internal/config/source.go:119`, sementara `auth.json`/`turso.json`/`oauth.json` sudah `0600`; `os.WriteFile` **tidak** mengubah mode file yang sudah ada, jadi file yang terlanjur world-readable (termasuk `configs/upstreams.json` di repo ini, 162.577 byte) tetap begitu selamanya. Kelas yang sama ditemukan di replika lokal: `data/firefly.db` + `-wal` (105 MB) `0644` di dalam direktori `0755`, padahal replika mencerminkan `api_keys.api_key`. Perbaikan: helper bersama `internal/config/files.go` (`SecretFileMode`, `WriteSecretFile` = WriteFile + `chmod` eksplisit, `tightenSecretFile` toleran-`ENOENT`) dipakai di **kedua** jalur simpan katalog — `settings.go` **dan** `tenants_admin.go` (penulis kedua yang menyimpan `upstreams.json`/`tenants.json` dari CRUD tenant, ditemukan saat audit lanjutan) — lewat `internal/server/catalog_files.go` (`writeCatalogFiles`), **dan** di `EnsureConfigFiles` (kolom perm per file + pass pengetatan yang tidak menyentuh isi; `models/combos/tls/tokensaver` sengaja tetap `0644` karena bukan pembawa kredensial); `internal/storage/turso/client.go` membuat/mengetatkan direktori replika ke `0700` lewat `ensureLocalDir` (libSQL yang menentukan mode file sqlite, jadi direktori adalah lapisan yang bisa dijamin Firefly) plus `chmod 0600` best-effort atas file replika setelah `Connect`. Test pengunci: `internal/config/files_test.go` (buat, ketatkan, `EnsureConfigFiles` dua kali), `TestSettingsGetAndPost` (mode `0600` pasca-simpan), `TestSettingsTenantAPIKeyPlaintext` (seed `0644` → `0600` pasca-simpan), `TestTenantsAdmin_CRUDRoundTrip` (**penulis kedua**: CRUD tenant juga menulis `tenants.json` `0600`), `internal/storage/turso/client_test.go` (`0700` untuk direktori baru maupun warisan `0755`). Karena dua jalur simpan (settings & CRUD tenant) menduplikasi lima penulisan file yang sama, blok itu dipromosikan ke `internal/server/catalog_files.go` (`catalogFileSet` + `writeCatalogFiles`, field `nil` dilewati sehingga TokenSaver yang tak dikirim tetap tak tersentuh) dan kedua pemanggil memakai pesan galat yang identik seperti sebelumnya. Verifikasi live pada instance terisolasi: restart mengetatkan `upstreams.json`/`tenants.json` tanpa mengubah file non-secret, dan round-trip `POST /api/settings` menulis ulang file sebagai `0600` dengan **4/4 secret mentah identik** serta nol placeholder bermasker tersimpan. Remediasi lokal di luar kode (mengikuti perilaku baru): `configs/{upstreams,tenants}.json` → `0600`, `configs/data/` → `0700` + `firefly.db*` → `0600`. Verifikasi akhir: `gofmt -l .` kosong, `go vet ./...` bersih, `go test -count=1 -race ./...` seluruh paket hijau. **Belum di-commit; DB live belum tersentuh** |
| 2026-09-22 | **2g dikerjakan: util frontend di-DRY + `maskSecret` idempoten** — tiga kelompok helper yang disalin per modul dipromosikan ke `frontend/src/lib/`: `format.ts` (`formatNumber`/`formatCurrency`/`formatCompact`, menggantikan salinan di `StatsFooter.tsx`, `OverviewView.tsx`, `TenantTable.tsx`), `datetime.ts` (`toMillis`/`toDateInputValue`/`endOfDayMs` — `toMillis` menormalkan epoch detik warisan penulis lama, yang bila dibaca sebagai milidetik jatuh ke 1970 dan tampil "kedaluwarsa permanen"), dan `secret.ts`. Temuan saat promosi: `maskSecret` sisi frontend **berbeda bentuk** dari server (`slice(0,4)...slice(-3)` vs `slice(0,3)...slice(-4)`) **dan** tidak idempoten — `KeyRingSlotList` memask nilai dari dua provenance berbeda (secret mentah dari snapshot KeyRing vs `api_key` yang sudah dimask server di DTO settings), dan memask ulang `[REDACTED]` (10 byte > ambang 8) menghasilkan `[RE...TED]`. Sekarang `lib/secret.ts` identik dengan `maskSecret` server dan mengembalikan input yang sudah termask apa adanya; `looksMasked()` juga dipakai modul Providers untuk menolak nilai berbentuk hint di sisi klien. `UpstreamModal.tsx` kehilangan jalur "Import from Database" (lihat baris K3-c) dan `KeyRingSlotList.tsx` kehilangan masker lokalnya. Verifikasi: `tsc --noEmit` bersih, `npm run build` sukses. **Belum di-commit; DB live belum tersentuh** |
| 2026-09-22 | **K15 ditutup: commit dieksekusi, dokumen diperbarui, rilis v1.13.0 ditandai** — atas permintaan eksplisit ("update dokumentasi, commit, dan push tag"). Verifikasi diulang di working tree lebih dulu (`gofmt -l .` kosong, `go build ./...`, `go vet ./...` bersih, `go test -count=1 -race ./...` hijau), lalu tujuh commit dibuat berurutan sesuai tabel §7.1 dengan verifikasi **per commit** (`81b52c9` format → `1bc3fab` DDL → `8a6f792` permukaan tulis → `31a09bc` frontend service/util → `18cf0cf` dashboard → `c14c51a` permission kredensial → dokumen ini). Satu penyesuaian split: `config/files.go` + `server/catalog_files.go` naik ke commit 2 karena `settings.go` di commit itu sudah memanggilnya, dan `config/files_test.go` tetap di 2f agar commit 2 tidak meng-assert `EnsureConfigFiles` yang belum berubah. Dokumen sisi-pembaca diperbarui untuk permukaan baru: `README.md` (rute `/api/providers*` + `/api/keys/{id}` + `POST /api/harvester/sync`, flag `-harvester-token`/`FIREFLY_HARVESTER_TOKEN`, tab Providers) dan `AGENTS.md` (peta paket `internal/server/` + daftar rute). Versi dipilih **1.13.0** (minor): ada permukaan API baru, tab dashboard baru, dan kepemilikan DDL — bukan sekadar perbaikan. Rilis mengikuti proses tetap: entri `CHANGELOG.md` (`## [1.13.0]`) + bump **kedua** field versi frontend (`frontend/src/core/constants.ts` `APP_VERSION` + `frontend/package.json`) dalam satu commit rilis, tag anotasi `v1.13.0` dengan pesan identik judul commit rilis, lalu `main` + tag di-push dan artifact diverifikasi (ekstraksi catatan rilis disimulasikan lebih dulu, `checksums.txt` + `./firefly --version`). DB live tetap belum tersentuh; Fase 3 (deploy harvester) dan Fase 4 (cabut akses DB) tetap terbuka |
