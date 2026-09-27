# Firefly — Landing Page

Landing page publik untuk project Firefly (bukan bagian dari aplikasi/frontend dashboard).
Halaman statis murni — HTML + CSS + vanilla JS, tanpa build step.

## Cara membuka

Buka langsung `index.html` di browser, atau serve lokal agar font & aset termuat sempurna:

```bash
# dari folder repo
python -m http.server -d landing 8090
# atau
npx serve landing
```

Lalu akses `http://localhost:8090`.

## Struktur

```
landing/
├── index.html                # Seluruh halaman (satu file, section jelas)
├── assets/
│   ├── css/style.css         # Design tokens + seluruh styling
│   ├── js/fireflies.js       # Canvas kunang-kunang (swarm + constellation lines)
│   ├── js/forest.js          # Parallax berlapis + animasi jalur terbang kunang-kunang
│   ├── js/main.js            # Data provider, tab, rotasi diagram fitur, tombol copy
│   ├── img/brands/*.svg      # Logo provider untuk strip "Speaks the wire of"
│   └── img/orn-*.svg         # Ornamen hutan hasil generator (lihat bawah)
└── tools/                    # Perkakas regenerate aset (opsional)
    ├── captures/*.webp       # Master capture mentah 2400x1350 (arsip pipeline screenshot)
    ├── crop-shots.sh         # Pemotong capture → assets/img/shot-*.webp (parkir, lihat bawah)
    ├── gen-forest.mjs        # Generator ornamen SVG (pinus, semak, pakis, jamur, dahan)
    ├── ornament-preview.html # Halaman inspeksi lapisan ornamen
    ├── vite.config.demo.ts   # Dev server frontend yang mem-proxy ke backend demo
    ├── mock-upstream.mjs     # Mock upstream OpenAI-compatible di :9091
    ├── gen-traffic.mjs       # Generator trafik demo
    └── demo-configs/         # Config Firefly demo (data fiktif)
```

## Diagram fitur (menggantikan screenshot)

Section fitur **tidak lagi memakai screenshot**. Tujuh crop dashboard sudah dicabut dari
halaman: diperkecil ke lebar kolom, tipografi dashboard-nya tak lagi terbaca, dan satu
gambar utuh menceritakan dua belas hal sekaligus. Sekarang tiap fitur **digambar** — satu
poin per panel, dalam kosakata yang sama dengan konektor `client → firefly → provider` di
section providers di atasnya, sehingga seluruh halaman terbaca sebagai satu tangan.

Kosakata gambarannya hidup di `style.css` (blok *feature diagrams*) dan dipakai lewat
`class="diagram"` di `index.html`:

| Kelas | Peran |
| :--- | :--- |
| `.diagram` | piring gambar: border tipis + latar tembus pandang, `--dg-step: 1.9s`, `--dg-cycle: 3 × --dg-step` |
| `.wire-node`, `.wire-node--ff` | kotak tahap (dipakai ulang dari section providers) |
| `.wire-link`, `.dg-link-v` | hop; `--v` membaca vertikal, `--mid` memusatkannya |
| `.dg-tree`, `.dg-row`, `.dg-row--key` | percabangan: satu rel rambut di kiri, tiap baris menggantung padanya lewat stub 22 px |
| `.dg-chip`, `.dg-chip--ok` | status: garis putus-putus = dilewati, garis penuh + wash lime = jalur yang dipakai |
| `.dg-attempt--reject/--accept` | animasi dua percobaan berurutan pada satu `--dg-cycle` |
| `.dg-ticks--in/--out` | aliran token: jarak antar-tick yang berbeda = aliran yang menipis |
| `.dg-topo`, `.dg-host`, `.dg-fly` | peta upstream di panel Dashboard, kunang-kunangnya ikut mengembara |
| `.dg-metric`, `.dg-pip`, `.dg-log` | metrik Prometheus, pip health, satu baris log ter-redaksi |

### Hop: garis yang menyentuh kotak, bukan menggantung di tengah gap

Hop (`.wire-link`, `.dg-link-v`) **tidak** digambar sepanjang kotaknya sendiri lalu dibiarkan
menggantung di tengah gap grid. Cara itu membuat garisnya berhenti 14 px sebelum kotak
berikutnya — terlihat seperti terhalang padding/margin. Hop justru direntangkan **dari border
ke border**: kotaknya dipanjangkan sebanyak dua gap yang mengapitnya, lalu ditarik kembali
menutupi kedua gap itu dengan margin negatif. Ruang yang dikonsumsi tetap sama persis
(flex-basis bertambah 2 × gap, margin luar berkurang 2 × gap), jadi tak satu pun elemen lain
bergeser.

| Variabel | Di mana | Arti |
| :--- | :--- | :--- |
| `--wire-gap` | `.wire` | gap wire providers (14 px) |
| `--hop` | `.wire` | panjang hop providers saat mendatar (36 px; 26 px di HP, saat wire jadi kolom) |
| `--dg-gap` | `.diagram` | gap grid piring (14 px; `.dg-saver` menurunkannya ke 12 px untuk tumpukan yang lebih rapat) |
| `--dg-hop` | `.diagram` | panjang hop piring (30 px; 26 px di HP) |
| `--hop-travel` | hop | `--hop + 2 × gap` — jarak terbang paket, dibaca `@keyframes wire-flow` / `wire-flow-y` supaya paket selalu keluar lewat border jauh, sepanjang apa pun hop-nya |

Gradien garisnya berhenti di alpha `0.22` (bukan `transparent`) supaya ujungnya benar-benar
menyentuh border, dan baru menyala `0.45` di tengah. Karena hop kini menutupi gap di atasnya,
`.dg-tree::before` cukup mulai di `top: 0` ketika ada hop tepat di atasnya
(`.dg-link-v + .dg-tree::before`); kalau tidak, 14 px yang sama digambar dua kali oleh hop dan
rel, dan sendinya jadi lebih terang dari sisa garis.

Tujuh panel itu, berurutan: Load balancing (rel ke tiga key, sorotan berputar), Resilience
(429 lalu 200, breaker tetap `closed`), Token saver (empat valve, aliran masuk padat keluar
jarang), Tenants (dua key dengan kuota/expiry/rps/models), Dashboard (konstelasi upstream),
Tools (lane chat dengan celah TTFT + bar benchmark), Observability (metrik, pip, log).

Dua perilaku hidup di `main.js`, keduanya **berhenti sendiri** saat panelnya ada di tab lain
(`offsetParent === null`) dan tidak berjalan sama sekali di bawah `prefers-reduced-motion`:

- `[data-keys]` — merotasi `data-active` dan angka in-flight satu langkah per **`--dg-step`
  yang dibaca dari CSS**, bukan durasi yang ditulis ulang di JS, supaya sorotan tidak pernah
  lepas dari paket yang berjalan di langkah yang sama.
- `[data-tick]` — penghitung Prometheus; hanya naik, tidak pernah turun, karena counter asli
  juga begitu.

Kontrak `prefers-reduced-motion` menambahkan semua animasi `.dg-*` ke blok `animation: none`
dan mengganti arti gerak dengan keadaan diam yang tetap benar: baris `attempt 2` tetap
menyala (jalur yang dipakai), paket yang melompat disembunyikan, kedua aliran token jadi
deret ajek, dan key yang melayani tetap ter-highlight.

## Pipeline screenshot (diarsipkan)

Crop di `assets/img/shot-*.webp` sudah **tidak dipakai halaman** dan tidak lagi disimpan di
repo; `tools/captures/` + `crop-shots.sh` tetap ada kalau suatu saat butuh gambar dashboard
asli lagi (misal untuk posting rilis). Screenshot diambil dari dashboard asli yang dijalankan
dengan data demo — bukan mockup — lalu **dipotong** supaya yang tampil hanya bagian yang
ingin ditonjolkan. Untuk memakai ulang: `bash landing/tools/crop-shots.sh` menulis ulang
ketujuh berkas itu, lalu rujuk balik dari `index.html`.

**Geometri capture.** Semua master adalah jendela dashboard **1600×900 CSS px pada DPR 1.5×**,
sehingga file mentahnya **2400×1350**. Artinya:

```
source px = css px × 3 / 2
```

Karena itu setiap angka window di `crop-shots.sh` harus **genap** — supaya perkalian 3/2
menghasilkan bilangan bulat dan tidak ada baris piksel yang bergeser.

**Skala tampil.** `assets/css/style.css` tidak melakukan zoom apa pun. Skala relatif terhadap
dashboard murni ditentukan lebar kolom: crop 1000 CSS px yang ditampilkan ~1000 px tampil 1:1,
jadi teks di dalam screenshot tetap seukuran aslinya. Itu sebabnya panel fitur memakai grid
`5fr / 7fr` plus trik *bleed* di bawah — screenshot memang butuh lebar.

**Memotong.** Setiap window di `crop-shots.sh` dipilih agar batasnya jatuh di **sela antar kartu**
(gutter), bukan memotong baris teks. Verifikasi cepat setelah regenerate: buka hasilnya dan
pastikan tidak ada glyph yang terbelah di tepi atas/bawah.

```bash
# butuh ffmpeg dengan libwebp; default output ke landing/assets/img/
bash landing/tools/crop-shots.sh
```

Untuk membuat ulang dari nol (misal setelah UI dashboard berubah):

```bash
# 1. Build & jalankan backend demo (port 8088, jangan sentuh instance asli di :8080)
go build -o "$TMP/firefly-demo/firefly.exe" ./cmd/firefly
"$TMP/firefly-demo/firefly.exe" -config-dir=landing/tools/demo-configs \
  -addr=127.0.0.1:8088 -admin-addr=127.0.0.1:9095 -admin-token=demo

# 2. Jalankan mock upstream (port 9091)
node landing/tools/mock-upstream.mjs

# 3. Bangkitkan trafik demo
node landing/tools/gen-traffic.mjs

# 4. Jalankan dev server frontend dengan proxy demo (port 3003)
cd frontend && bunx vite --config ../landing/tools/vite.config.demo.ts

# 5. Capture jendela 1600x900 pada DPR 1.5 → simpan sebagai WebP ke tools/captures/
#    (Playwright + Edge; beri nama sesuai halaman: virtual-combos, upstreams, dst.)

# 6. Potong untuk halaman
bash landing/tools/crop-shots.sh
```

## Ornamen hutan & parallax

Seluruh ilustrasi hutan adalah SVG prosedural, bukan gambar tangan:

```bash
node landing/tools/gen-forest.mjs   # tulis ulang assets/img/orn-*.svg
```

Generator memakai PRNG ber-seed (`mulberry32`) sehingga hasilnya deterministik —
ubah angka seed di skrip untuk variasi bentuk tanpa mengubah gaya. Lapisan yang dihasilkan:

| Berkas | Peran | Parallax `scroll,mouse` |
| :--- | :--- | :--- |
| `orn-mist-far.svg` | kabut punggungan paling jauh | `22,5` |
| `orn-pines-far.svg` | pinus jauh | `34,8` |
| `orn-pines-near.svg` | pinus dekat | `48,12` |
| `orn-floor.svg` | lantai hutan (semak, rumput, jamur bercahaya) | `62,15` |
| `orn-canopy-left/right.svg` | dahan menggantung di sudut atas hero | `40,12` |
| `orn-grove-right.svg` | rumpun pinus foreground kanan | `48,16` |
| `orn-fern-left.svg` | pakis undergrowth kiri | `58,20` |

### Satu kanvas, satu garis tanah

Empat lapisan treeline (`mist → pines-far → pines-near → floor`) memakai **kanvas yang sama,
2560×512, dengan garis tanah tepat di tepi bawah**. Wadah `.forest` menempatkannya `absolute`
pada `bottom: 0` dan mengisi seluruh tinggi wadah, sehingga keempat lapisan dipotong dan
diskalakan **identik** oleh `object-fit: cover; object-position: bottom center`:

```css
.forest { position: relative; height: clamp(140px, 17vw, 700px); }
.fl     { position: absolute; left: 0; bottom: 0; width: 100%; height: 100%;
          object-fit: cover; object-position: bottom center; }
```

Konsekuensinya: rasio keempat lapisan terkunci satu sama lain di segala lebar layar.
Pohon tidak mungkin lagi melayang di atas rumput atau tenggelam ke bawahnya — bug lama
"pohon tidak sejajar dengan rumput" hilang secara struktural, bukan dengan penyesuaian
angka per lapisan. Untuk menggeser komposisi, ubah **satu** kanvas bersama, bukan tiap file.

**Tingginya murni lebar-driven — tidak ada suku `vh`.** Selama band lebih pendek dari `20vw`,
`cover` men-skalakan gambar menurut sumbu lebar, jadi **1vw tinggi band = 2560/100 = 25,6 px
kanvas**. Siluet tertinggi di seluruh set (pinus dekat, kini 344 px di atas garis tanah)
karena itu menuntut `344 / 25,6 = 13,44vw` tinggi band; di bawah itu pucuknya terpotong rata.
`clamp(140px, 17vw, 700px)` memberi margin ~3,5vw di atas angka itu, dan tetap di bawah
ambang `20vw` tempat skala berbalik ke sumbu tinggi (di situ sisi kiri-kanan yang mulai
terpotong, bukan langitnya lagi). Angka `700px` baru bekerja di atas lebar ~4100 px.

Versi sebelumnya memakai `min(20vw, 24vh)`: pada 1920×1080 suku `24vh` menekan band ke 259 px
— hanya 346 dari 512 px kanvas — dan seluruh treeline terpangkas rata. Itulah bug "pohon
terpotong di 1080p". Karena itu pula art pinus dekat diperkecil 18% di `gen-forest.mjs`
(`hMin: 153, hMax: 354`, dari 432 px): konstanta tinggi di CSS harus diturunkan dengan
mengubah gambarnya, bukan dengan memberi suku `vh` yang perilakunya bergantung jendela.

Konsekuensi yang diterima: pada ultrawide pendek (3440×1080, 3840×1080) garis tanah jatuh
78–190 px di bawah lipatan. Itu harga dari "pucuk pohon tidak pernah terpotong" — dan masih
ada satu media query yang merapatkan ritme hero pada jendela pendek
(`@media (min-width: 961px) and (max-height: 1120px)`) supaya band selebar mungkin masuk
layar: 1920×1080 menyisakan 180 px rumput di bawah lipatan, 1440×900 menyisakan 97 px,
1366×768 menyisakan 20 px.

`.forest` berada **di dalam alur dokumen** (bukan `absolute` terhadap section). Itu yang
membuat rumput dan semak sudah terlihat pada view pertama tanpa scroll. Rumpun pinus
(`.orn-grove`) dan pakis (`.orn-fern`) ditambatkan ke tepi bawah `.forest` dengan `bottom: 0`,
jadi keduanya tumbuh dari garis tanah yang sama.

### Parallax (`forest.js`)

Setiap elemen ber-`data-parallax="scroll,mouse"` di-`translate3d` berdasarkan progres
section terhadap viewport (amp ≈ kedekatan lapisan) plus offset pointer yang di-smooth:

```
p = clamp((vh - rect.top) / (vh + rect.height), 0, 1)
y = -(p - 0.5) * scrollAmp + mouseY * mouseAmp * 0.35
x = mouseX * mouseAmp
```

Scope parallax adalah `el.closest('section, footer')`, jadi tiap band punya kurvanya sendiri.

**Pengecualian penting — band footer (`pinnedY`).** Band di dalam `<footer>` dipatok ke dasar
halaman: offset vertikal apa pun akan menaikkan `document.height` (saat band masih di bawah
viewport) atau mengangkat garis tanah dari tepi bawah (saat sudah terlihat) — inilah penyebab
halaman sempat memanjang ~31 px di bawah footer. Layer di dalam `footer` karena itu memakai
`y = 0` dan hanya mengambil drift horizontal dari pointer:

```js
pinnedY: el.closest('footer') !== null
```

Jalur kunang-kunang (`[data-trail]`) memakai `<path>` sebagai lintasan: JS menyuntikkan
3 pasang halo+core dan menggerakkannya sepanjang `getPointAtLength()` dengan lag, flicker,
dan kecepatan berbeda per titik. Konektor `client → firefly → provider` di section providers
memakai pola berbeda: `.wire-link::after` adalah paket terang yang berjalan sepanjang garis
(`wire-flow 2.6s`), dan hop kedua diberi `animation-delay: 1.3s` supaya paket terbaca
mengalir *menembus* gateway, bukan dua animasi terpisah.

Aturan penting saat mengedit:

- Animasi CSS (`sway`, `breathe`, `drift`) hanya boleh berada di **anak** elemen ber-parallax —
  JS menulis `transform` inline pada wrapper, jadi animasi di wrapper akan saling menimpa.
- `prefers-reduced-motion` memarkir kunang-kunang jalur di posisi tetap dan mematikan
  semua animasi ornamen; parallax tidak dijalankan sama sekali.
- Ornamen selalu `pointer-events: none` dan berada di bawah `.section` (z-index 1 vs 2)
  supaya tidak pernah menutupi teks atau mencuri klik.

## Catatan desain

- Tema: hutan malam — kelanjutan identitas dashboard ("nocturnal bioluminescent").
  Palet memakai keluarga warna yang sama (`#f0ffb4` firefly glow, `#bef264` aura lime,
  latar ink kehijauan `#04-08` range) agar terasa satu produk.
- Tipografi: Fraunces (display), Inter (body), JetBrains Mono (kode/telemetri).
- Kunang-kunang: dua lapis. (1) Canvas fixed full-page (`fireflies.js`) — drift perlahan,
  flicker individual, garis konstelasi antar titik yang berdekatan, parallax halus
  saat scroll. hormati `prefers-reduced-motion` dan pause saat tab hidden.
  (2) Ornamen DOM + jalur terbang ber-SVG (`forest.js`) yang ikut parallax section.
- Hutan: lapisan kedalaman dari kabut → pinus jauh → pinus dekat → lantai hutan,
  plus dahan sudut, bulan, rumpun, dan pakis. Semakin dekat lapisan, semakin besar
  amplitudo parallax-nya sehingga kedalaman terasa saat scroll maupun gerak pointer.
- Strip **"Speaks the wire of"** (`section.compat`): satu baris logo perusahaan AI
  berukuran 22 px. Logonya sendiri sudah monokrom (`fill="#fff"`, bukan logo berwarna),
  lalu diredupkan ke `opacity: 0.4` dan menyala hanya saat hover — jadi tampil sebagai
  abu-abu tanpa perlu `filter: grayscale()`. Fungsinya menyatakan kompatibilitas wire
  protocol tanpa satu paragraf pun, dan tanpa badge berkilau.
- Fitur disajikan sebagai **tab** (`Load balancing`, `Resilience`, `Token saver`, …) dengan
  satu panel per tab. Tab memakai `role="tablist"`/`role="tab"`/`aria-selected`/`aria-controls`,
  roving tabindex, dan navigasi ArrowLeft/ArrowRight/Home/End. Panel non-aktif memakai
  atribut `hidden` dan butuh `.feature-panel[hidden] { display: none }` karena `display: grid`
  mengalahkan aturan `[hidden]` bawaan browser.
- Kolom panel fitur tetap `5fr / 7fr`, tapi sisi kanannya sekarang **piring gambar**
  (`.diagram`), bukan crop yang menembus tepi viewport. Tidak ada lagi `--bleed`: gambar
  yang digambar bisa menjelaskan satu poin pada lebar kolom apa pun, sedangkan screenshot
  butuh lebar ekstra supaya teksnya terbaca — itulah alasan crops itu dicabut.
- Tanpa elemen "AI slop": tanpa glowing badge, tanpa bento grid, tanpa ikon per segmen,
  tanpa animasi reveal-on-scroll. Struktur terinspirasi plnty.app: hero yang dipimpin visual,
  strip kompatibilitas yang tenang, showcase provider interaktif, angka performa besar,
  footer dengan kalimat penutup besar dan treeline menyentuh dasar halaman.
