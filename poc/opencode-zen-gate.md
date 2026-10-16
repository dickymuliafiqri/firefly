# OpenCode Zen — mekanisme autentikasi & gating free tier (hasil reverse engineering)

Target: `https://opencode.ai/zen/v1/*` (alias nyata: `https://opencode.ai/inference/v1/*`).
Metode: MITM paket CLI asli → byte-exact replay → bisection per-header/per-field.
Status: **selesai untuk free tier** (PoC jalan), **sebagian untuk tier berbayar/OAuth**.

---

## 1. Jawaban singkat

Free tier OpenCode Zen **tidak melakukan autentikasi kredensial sama sekali**. Ia melakukan
**client attestation berbasis bentuk (shape-based)**: 4 pemeriksaan murah yang tujuannya
"apakah request ini keluar dari binary OpenCode?" — bukan "siapa kamu?".

Tidak ada signature, tidak ada HMAC, tidak ada TLS fingerprinting, tidak ada secret.
Semuanya **forgeable** dengan ~20 baris Python (lihat §5).

`Authorization: Bearer public` adalah token literal yang di-hardcode; nilainya sendiri
bukan diskriminator (req tanpa header itu pun checkout: kalau `public` → jalur free tier,
kalau nilai lain → diperlakukan sebagai API key → `401`).

---

## 2. Empat gerbang (spec lengkap)

### Gate 1 — `Authorization`

| Nilai | Status |
|---|---|
| `public` / `Bearer public` / `bearer public` / `Bearer  public` | **200** |
| (header tidak ada) / `""` | 403 FreeTierError |
| `Bearer` / `Bearer sk-abc123` | 401 Unauthorized (dianggap API key invalid) |

Logika gateway: parse bearer token → kalau token == `"public"` masuk **free-tier path**
(dikenai Gate 2–4); selain itu diperlakukan sebagai API key berbayar dan divalidasi.

### Gate 2 — `User-Agent`

Regex efektif (anchored di akhir, boleh ada prefix apa pun):

```
opencode/(latest/)?\d+\.\d+\.\d+(/\w+)?$
```

Bukti:

| UA | Hasil |
|---|---|
| `opencode/latest/2.0.29/cli` | 200 |
| `opencode/2.0.29` | 200 |
| `opencode/latest/1.18.31/cli` | 200 |
| `opencode/latest/2.0.29/desktop` | 200 |
| `curl/8.7.1 opencode/latest/2.0.29/cli` | 200 (prefix bebas) |
| `opencode/x/cli` | 403 (bukan semver) |
| `opencode/latest/2.0.29/cli/extra` | 403 (4 segmen) |
| `opencode` | 403 |

### Gate 3 — `x-opencode-session-id`

Header ini **wajib**. Menghapus `x-opencode-session`, `x-session-affinity`, `x-session-id`
→ tetap 200. Menghapus `x-opencode-session-id` → 403. (Nilai boleh sama keempatnya.)

Format yang diterima:

```
^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$        dan   (0xFFFFFFFFFFFF - int(hex,16)) & 0xFFF <= 63
```

Artinya:

* prefix `ses_` **case-sensitive** (huruf kecil);
* 12 karakter hex **wajib huruf kecil** (`EC0596228FFE` → 403);
* 14 karakter alfanumerik bebas (huruf besar/kecil/angka);
* **3 digit hex terakhir harus di rentang `[0xfc0, 0xfff]`** — ini encoding counter
  per-milidetik yang di-emit generator OpenCode; gateway memvalidasi counter itu
  masuk akal (toleransi persis 64 nilai: `d = 0…63`).

Hasil bisection (hex `ec03e8b5XXXX`, suffix = `0xFFF - d`):

| d | suffix | hasil |
|---|---|---|
| 0 | `fff` | 200 |
| 1 | `ffe` | 200 ← yang dipakai CLI asli |
| 3 | `ffc` | 200 |
| 50 | `fcd` | 200 |
| 63 | `fc0` | 200 |
| 64 | `fbf` | 403 |
| 100 | `f9b` | 403 |
| 255 | `f00` | 403 |
| 4095 | `000` | 403 |

Nilai timestamp-nya tidak divalidasi — id dengan `Date.now()` tahun 2020 maupun 2030
sama-sama lolos, yang penting struktur low-bit-nya benar.
Case-insensitivity tail menjelaskan kenapa `ses_ec0596228ffett3kokmkku3o1z` (huruf kecil
dari id asli) lolos sementara random 26-char gagal: hex-nya sudah lowercase.

### Gate 4 — bentuk body

| Field | Aturan |
|---|---|
| `stream` | **harus `true`** (stream:false → 403, walau tools lengkap) |
| `tools` | wajib ada tool bernama **`read`** yang `parameters.properties`-nya **tidak kosong** |
| `model` | wajib ada (tanpa model → 500) |
| `messages` | wajib non-kosong (kosong → 400) |
| `stream_options`, `max_completion_tokens` | opsional |
| isi system prompt | tidak diperiksa (dihapus → tetap 200) |

Detail tools: dari 12 tool asli (`edit, glob, grep, question, read, shell, skill, subagent,
webfetch, websearch, write, execute`), **hanya menghapus `read`** yang menjadikan 403.
Jumlah tool tidak penting (1 tool asal `read` → 200; 13 tool tanpa `read` → 403).
Nama properti juga tidak penting (`properties.foo` lolos), tapi `properties: {}` → 403.
Deskripsi `read` tidak penting.

→ Ini sekadar fingerprint "request ini mengiklankan tool baca file seperti coding agent".

---

## 3. Algoritma session id (diambil dari binary)

Dari payload Bun `opencode` pada offset `51828302`:

```js
var F = 26, I = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", y = 0, d = 0;
function Ys()  { return EY(false) }   // ascending
function PAe() { return EY(true) }    // descending  ← dipakai "ses_" + PAe()
function EY(e, n = Date.now()) {
    if (n !== y) y = n, d = 0;
    d++;
    let r = BigInt(n) * 0x1000n + BigInt(d),           // ms << 12 | counter
        a = e ? ~r : r,
        c = Array.from({length: 6}, (l, D) => Number(a >> BigInt(40 - 8*D) & 0xffn)
                  .toString(16).padStart(2, "0")).join(""),
        s = crypto.getRandomValues(new Uint8Array(F - 12));
    return c + Array.from(s, (l) => I[l % 62]).join("");
}
```

* 12 hex = **48 bit rendah** dari `~((ms << 12) | counter)` (big-endian).
* 14 char = 14 byte acak `mod 62` dari alfabet `0-9A-Za-z`.
* Mode `ascending` (`Ys`) **ditolak** gateway → wajib varian `descending`.

Konsekuensinya: id dari instalasi/mesin mana pun sah, selama strukturnya benar —
gateway tidak bisa (dan tidak mencoba) membedakan id klien asli dari id buatan.

---

## 4. Hal-hal yang ternyata BUKAN diskriminator

Sudah diuji dan dinyatakan tidak relevan (semua tetap 200 saat dihilangkan/diubah):

* TLS fingerprint / JA3 — request diteruskan lewat Python+OpenSSL dan tetap 200.
* `b3` & `traceparent` (Zipkin/W3C tracing).
* `x-opencode-project` (diubah jadi random hex, all-zero, uppercase → 200).
* `x-opencode-client`, `x-opencode-session`, `x-session-affinity`, `x-session-id`,
  `Connection`, `Accept`, `Accept-Encoding`.
* Isi system prompt, `stream_options`, `max_completion_tokens`.
* Nilai timestamp di dalam session id.
* Endpoint: `zen/v1/...` dan `inference/v1/...` adalah alias yang sama.

---

## 5. PoC (jalan, terbukti)

`/tmp/zen_free_poc.py` — klien Python minimal tanpa OpenCode sama sekali:

```
session id : ses_ec037fda9ffeBBMQsicAYEVrA8
status     : HTTP/1.1 200 OK
assistant  : 'PONG'
```

Inti request:

```
POST /inference/v1/chat/completions HTTP/1.1
Host: opencode.ai
Authorization: Bearer public
User-Agent: opencode/latest/2.0.29/cli
Content-Type: application/json
x-opencode-session-id: ses_<12 hex lowercase><1..3 digit hex 0xfc0-0xfff><14 alnum>
Content-Length: <n>

{"model":"big-pickle",
 "messages":[{"role":"user","content":"hi"}],
 "stream":true,
 "tools":[{"type":"function","function":{"name":"read",
           "parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}
```

---

## 6. Sisi lain: login/berbayar (bukan `public`)

* OAuth 2.0 **authorization code + PKCE**, public client (`token_endpoint_auth_method: none`),
  redirect `http://127.0.0.1/callback` / `http://localhost/callback`.
  Client metadata: `https://opencode.ai/oauth/opencode/client.json`
  (RFC 7591; `client_id` = URL itu sendiri). Tanpa discovery metadata (`.well-known` → 404).
* Kredensial v2 (`2.0.x`) disimpan di **SQLite**, bukan `auth.json`:
  `~/.local/share/opencode/opencode.db`, tabel `account`, `account_state`, `control_account`,
  `credential` (semuanya kosong di instalasi ini → semua traffic jatuh ke free tier).
* Env `OPENCODE_API_KEY` dipakai sebagai API key. Tier `opencode-go` (subscription) memakai
  `x-opencode-session` dan menolak dengan `MissingSessionID` bila absen.
* Konfigurasi runtime bisa diarahkan lewat `provider.opencode.options.baseURL`, tapi
  daemon `opencode serve --service` meng-cache config di memori (harus dimatikan dulu).

---

## 7. Implikasi untuk Firefly

`internal/adapter/opencode` saat ini mengirim `x-opencode-client: desktop` dan
`EnforceFreeSessionPayload` (pad tool `bash`/`read`). Yang kurang agar free tier lolos:

1. `User-Agent` harus berakhiran `opencode/<latest|semver>/<semver>[/client]`
   (bukan `opencode/1.18.31 (…)`).
2. `x-opencode-session-id` wajib — di-generate per request dengan generator §3
   (12 hex lowercase + 14 base62, suffix hex ∈ `[fc0,fff]`), 4 header session di-set sama.
3. Body free tier wajib `stream: true` dan punya tool bernama `read` dengan
   `parameters.properties` non-kosong (nama properti bebas).
4. `Authorization: Bearer public` — dan jangan pakai nilai lain, karena selain `public`
   gateway akan menafsirkannya sebagai API key → `401`.

---

## 8. Artefak

| File | Isi |
|---|---|
| `/tmp/zen_free_poc.py` | PoC final (minimal, jalan) |
| `/tmp/ocbisect.py` | bisection header (drop satu per satu) |
| `/tmp/ocbisect3.py` | UA/auth/session-id/tools matrix |
| `/tmp/ocbisect4.py` | nama tool + struktur session id |
| `/tmp/ocbisect5.py` | konsistensi 4 header session + schema `read` |
| `/tmp/req_raw_*.bin` | request asli CLI (byte-exact, replay → 200) |
| `/tmp/mitm.py`, `/tmp/mitm_ca.pem` | proxy MITM + CA untuk capture |
