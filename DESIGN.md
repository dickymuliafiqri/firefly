# DESIGN.md — Bahasa Visual & Arsitektur Frontend Firefly

> **Status**: Core Design System Solid & Locked 🔒  
> **Tech Stack**: React 19 • Vite 5+ • TypeScript 5+ • Tailwind CSS 3+ • Zustand • TanStack Query  
> **Backend Counterpart**: Firefly (Go 1.22+ High-Concurrency AI Reverse Proxy)  
> **Sumber Identitas**: Landing page `firefly-web`. Sistem desain **solid**: tanpa transparansi, tanpa glow, tanpa efek GPU-heavy.

---

## 1. Filosofi Desain

1. **Hutan malam, bukan diskotek.** Gelap yang tenang, satu aksen lime sebagai "cahaya kunang-kunang" — hanya untuk hal yang interaktif/aktif. Lime bukan dekorasi, lime adalah _meaning_.
2. **Solid > transparan.** Permukaan berwarna solid, batas tegas 1px. Tidak ada kaca, tidak ada blur, tidak ada elemen "melayang tembus pandang".
3. **Data dulu, hiasan kemudian.** Elemen langit/hutan hanya ada di area non-data (topbar strip, halaman kosong, overview hero). Di dalam tabel/form/chart: nol dekorasi.
4. **Setiap gerakan punya alasan.** Animasi hanya untuk feedback state (hover, masuk halaman, perubahan status). Tidak ada partikel, tidak ada idle animation di area data.

---

## 2. Design Tokens (Single Source of Truth)

Didefinisikan sebagai CSS variables di `frontend/src/styles/global.css` + `frontend/tailwind.config.ts`. Mockup dan React wajib memakai token yang sama.

### 2.1 Warna

```css
:root {
  /* Langit (background shell) */
  --sky-top: #020617; /* app background atas */
  --sky-mid: #08152a; /* app background bawah */

  /* Permukaan SOLID (pengganti glass) */
  --surface-page: #050b18; /* sidebar, topbar, footer */
  --surface-card: #0b1322; /* kartu, panel */
  --surface-raised: #101a2c; /* header tabel, hover item, tooltip */
  --surface-input: #0d1524; /* input, select, textarea */
  --surface-overlay: #0a1120; /* modal, drawer (full solid) */

  /* Teks */
  --ink: #e8eef9;
  --muted: #93a5c4;
  --faint: #64748b;

  /* Aksen biolum — hanya untuk interaksi/status */
  --biolum: #bef264; /* aktif, link, item menu aktif */
  --biolum-bright: #f0ffb4; /* hover di atas --biolum */
  --biolum-ink: #0a1220; /* teks di atas tombol lime */

  /* Status */
  --ok: #10b981;
  --warn: #f59e0b;
  --danger: #f43f5e;
  --info: #06b6d4;

  /* Garis */
  --line: #1d2c47; /* border standar */
  --line-strong: #2a3a55; /* border hover / divider kuat */
}
```

**Aturan penggunaan aksen:**

- Lime dipakai untuk: item menu aktif (strip kiri 2px + teks), link, tombol primer, indikator "sehat/berjalan", titik data aktif.
- `--ok/--warn/--danger` hanya untuk status (HTTP code, circuit breaker, kuota), bukan tempelan estetika.

### 2.2 Tipografi

| Peran                           | Font                                            | Catatan                                                 |
| ------------------------------- | ----------------------------------------------- | ------------------------------------------------------- |
| Wordmark & judul halaman        | **Fraunces** (medium; wordmark italic-semibold) | Ukuran judul halaman maks 24px — ini app, bukan landing |
| Body, label, UI                 | **Inter**                                       | 14px default; 13px untuk konten padat                   |
| Data: angka, kode, ID, terminal | **JetBrains Mono**                              | Wajib `tabular-nums` untuk kolom angka                  |

Skala: `12 / 13 / 14 / 16 / 20 / 24`. Tidak ada ukuran lain tanpa alasan.  
Huruf kapital kecil + letter-spacing 0.05em hanya untuk label grup/kolom (12px, `--faint`).

### 2.3 Spasi, Radius, Border

- Spasi: kelipatan 4 → `4 8 12 16 20 24 32 48`.
- Radius: `6` (input, badge), `10` (kartu, panel), `12` (modal, drawer). Tidak ada pill kecuali tombol filter segmented.
- Border: selalu `1px solid var(--line)`; bukan bayangan.
- `box-shadow` dibatasi: `0 1px 2px rgba(0,0,0,.4)` untuk overlay/modal saja. **Tidak ada glow, tidak ada spread besar.**

---

## 3. Struktur Shell & Navigasi

### 3.1 Topbar

- Tinggi 56px, solid `--surface-page`, border-bottom `--line`.
- Kiri: tombol lipat sidebar (mobile: buka drawer) + wordmark "Firefly" (Fraunces italic) + nama halaman saat ini.
- Kanan: indikator status backend (dot `--ok` + "healthy"), jam lokal (mono, opsional).
- **Tanpa blur.** Saat scroll, topbar tetap solid — tidak transparan.

### 3.2 Sidebar

- Lebar 240px terbuka / 56px terlipat (ikon saja + tooltip). Mobile: drawer dengan backdrop **solid** `rgba(0,0,0,.6)` (tanpa blur).
- Solid `--surface-page`, border-right `--line`.
- Grup menu berlabel: `MONITORING`, `LAYANAN`, `KONFIGURASI`, `ALAT`.
- Item aktif: teks `--ink` terang + strip lime 2px di tepi kiri + latar `--surface-raised`. Item tidak aktif: teks `--muted`, hover jadi `--ink` + latar `--surface-raised`.
- Bawah sidebar: versi gateway (mono, faint).

### 3.3 Konten

- Padding konsisten 24px (mobile 16px), lebar konten maks 1200px, rata kiri (bukan center) agar mudah dipindai.
- Setiap halaman diawali **PageHeader**: judul Fraunces 20-24px + satu kalimat deskripsi `--muted` + tombol aksi utama di kanan. Tidak ada dua baris aksi.
- Konten tersusun atas grid kartu 12 kolom; kartu tidak boleh menumpuk lebih dari 2 tingkat kecuali pada tab detail.

---

## 4. Komponen Dasar (Kontrak Visual)

| Komponen                 | Aturan kunci                                                                                                                                                                                         |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Button**               | Primer: lime solid, teks `--biolum-ink`, hover `--biolum-bright` (tanpa translate-y). Sekunder: solid `--surface-raised` + border. Ghost: teks saja. Disabled: opacity 0.45 + `cursor: not-allowed`. |
| **Card**                 | Solid `--surface-card`, radius 10, border `--line`. Header kartu: judul 14px semibold + aksi kanan. Tanpa hover-lift; status hanya lewat border/teks.                                                |
| **Table**                | Header: `--surface-raised`, label 12px kapital faint. Baris: border-bottom `--line`, hover `--surface-raised`. Angka mono `tabular-nums` rata kanan. Status = badge, bukan warna sel.                |
| **Badge**                | Pill kecil, latar status dengan alpha 12% di atas `--surface-card`, teks status solid, dot 6px di kiri. Tanpa glow.                                                                                  |
| **Modal**                | Overlay solid `rgba(0,0,0,.65)`, panel solid `--surface-overlay`, radius 12, shadow standar. Maks tinggi 85vh, body scroll. Escape = tutup.                                                          |
| **Drawer**               | Panel kanan 420px solid `--surface-overlay`, sama aturannya dengan modal.                                                                                                                            |
| **Field**                | Label 13px muted di atas; input solid `--surface-input` + border `--line`; focus: border `--biolum` (bukan glow ring). Error: border `--danger` + teks bantu 12px.                                   |
| **Toast**                | Bawah kanan, solid `--surface-raised` + border kiri 3px status. Auto-tutup 4s.                                                                                                                       |
| **EmptyState**           | Ilustrasi hutan sederhana (SVG statis) + judul + satu tombol aksi. Satu-satunya tempat dekorasi boleh lebih besar.                                                                                   |
| **Tabs** (dalam halaman) | Segmented: border 1px, item aktif latar `--surface-raised` + teks ink; bukan garis bawah mengambang.                                                                                                 |

---

## 5. Density & Hierarki

1. **Satu layar = satu pertanyaan.** Judul halaman menjawab "apa yang saya lihat di sini".
2. **Ringkasan dulu, detail menyusul.** Setiap halaman: maksimal 1 baris kartu ringkasan (3–4 metrik kunci) → konten utama (tabel/daftar/chart) → detail hanya saat diklik (drawer/modal). Dilarang menampilkan seluruh detail di layar pertama.
3. **Maksimum 4 metrik ringkasan per halaman.** Sisanya masuk tab atau detail.
4. **Progressive disclosure:** tabel menampilkan 6–8 kolom terpenting; sisanya ada di baris detail (drawer). Filter dan pencarian selalu di atas tabel, satu baris.
5. **Whitespace adalah bagian dari desain.** Jarak antar-seksi 32px; jangan menghemat spasi untuk memaksakan "semua terlihat".

---

## 6. Motion & Parallax (Aturan Murah-GPU)

Efek yang **diizinkan** — semua hanya `transform` + `opacity`, tanpa `filter`, tanpa `blur`, tanpa canvas:

| Efek                   | Aturan                                                                                                                                                                                           |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Parallax langit**    | Hanya elemen Sky (bintang, bulan, horizonlight) — maksimum 3 lapis. Loop rAF tunggal dengan lerp (pola `ParallaxScript.astro`), `translate3d`, `will-change: transform` hanya pada 3 elemen itu. |
| **Sticky scenery**     | Strip hutan pinus (SVG, `pointer-events: none`) hanya di: bawah halaman Overview dan EmptyState. Di halaman data: tidak ada — menjaga area tabel bersih.                                         |
| **Bintang berkelip**   | CSS keyframe opacity, maks 20 titik, hanya di lapisan Sky.                                                                                                                                       |
| **Transisi interaksi** | `150ms ease` untuk warna/border; `200ms ease` untuk panel. Tanpa spring/bounce.                                                                                                                  |
| **Masuk halaman**      | Konten fade+rise 8px, 250ms, sekali. Tanpa stagger berantai.                                                                                                                                     |

**Wajib:** seluruh efek mati saat `prefers-reduced-motion: reduce`; loop parallax berhenti saat `document.hidden`.  
**Dilarang total:** kunang-kunang canvas, shooting star, aurora bergerak, `background-attachment: fixed` di app (mahal saat repaint panjang), `backdrop-blur`/`filter: blur()` dalam bentuk apa pun, `box-shadow` glow/neon, animasi `box-shadow`/`width`/`height`/`top`/`left`.

---

## 7. Peta Penggunaan Dekorasi

| Area                      | Sky (fixed, subtle)         | Scenery (pohon) | Parallax |
| ------------------------- | --------------------------- | --------------- | -------- |
| Sidebar / Topbar          | – (permukaan solid menutup) | –               | –        |
| Area konten semua halaman | ✔                           | –               | ✔        |
| Overview                  | ✔                           | ✔ dasar section | ✔        |
| EmptyState / onboarding   | ✔                           | ✔               | ✔        |
| Modal / Drawer            | – (berada di atas overlay)  | –               | –        |

Aturan praktis: **Sky adalah atmosfer latar, bukan konten.** Dirender sebagai satu instance global di level shell (fixed, z rendah, di belakang semua konten, maksimum 3 elemen `[data-depth]`), sehingga identitas terasa di mana-mana tanpa menempel pada konten mana pun. Scenery hanya boleh menempel pada Overview dan EmptyState. Di dalam kartu/tabel/form: tetap nol dekorasi.

---

## 8. Aksesibilitas

- Kontras minimal 4.5:1 untuk teks (uji `--muted` di atas `--surface-card`).
- Focus selalu terlihat: `outline: 2px solid var(--biolum); outline-offset: 2px`.
- Semua aksi ikon-only wajib `aria-label`. Sidebar terlipat: item tetap punya nama via `aria-label`/tooltip.
- Navigasi penuh dengan keyboard: sidebar `Tab`/`Enter`, modal `Escape` + focus trap, tabel bisa dilalui.
- Status tidak boleh disampaikan hanya lewat warna (badge selalu bertuliskan teks).
