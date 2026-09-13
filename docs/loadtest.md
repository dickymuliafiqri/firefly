# Firefly High-Concurrency Load Testing & Benchmark Guide

Panduan ini mendokumentasikan alat uji beban (*load testing*) dan *benchmarking* untuk menguji ketahanan endpoint `/v1/chat/completions` pada gateway **Firefly** di bawah skema **100 dan 1.000 request simultan** (*at the exact same millisecond*) dengan profil beban: **Rendah (Low)**, **Sedang (Medium)**, dan **Berat (Heavy)**.

Alat ini dirancang sebagai **binary eksternal (*detached/standalone*)** di [`cmd/loadtest`](file:///Users/dickymuliafiqri/go/src/github.com/dickymuliafiqri/gorouter/cmd/loadtest), serta dilengkapi dengan **suite pengujian manual (*manual test*)** yang terisolasi dari proses CI reguler melalui build tag `//go:build manual`.

---

## 1. Arsitektur Pengujian & Karakteristik Beban

### 1.1. Mekanisme Synchronized Barrier Concurrency
Untuk memastikan seluruh request (100 atau 1.000) benar-benar menghantam gateway **di waktu yang bersamaan**, runner menggunakan sinkronisasi *two-phase barrier*:
1. Semua $C$ goroutine pekerja (*workers*) di-spawn terlebih dahulu.
2. Setiap pekerja menyiapkan HTTP request payload di memori dan melaporkan status siap (`readyWg.Done()`).
3. Seluruh pekerja menahan eksekusi pada channel barrier `<-startBarrier`.
4. Setelah seluruh pekerja $100\%$ siap, barrier dibuka serentak (`close(startBarrier)`), melepaskan 100 atau 1.000 koneksi ke Firefly pada milidetik yang sama.

---

### 1.2. Tiga Profil Beban (Workload Profiles)

| Profil Beban | Mode Stream | Max Tokens | Karakteristik Payload & Skenario Uji |
| :--- | :--- | :--- | :--- |
| **Rendah (`low`)** | `stream: false` | 16 | **Minimal Prompt:** Single-turn prompt singkat (*"Ping! Respond with 'pong'"*). Menguji throughput murni (*raw RPS*), alokasi memori minimal, dan kecepatan *round-trip* proxy gateway. |
| **Sedang (`medium`)** | `stream: true` (SSE) | 128 | **Interactive Chat:** Prompt teknis (~150 kata) mengenai arsitektur gateway. Menguji konsumsi token *Server-Sent Events* (SSE) secara streaming, alokasi buffer streaming, serta mengukur **TTFT** (*Time to First Token*). |
| **Berat (`heavy`)** | `stream: true` (SSE) | 512 | **High-Context Stress Test:** Multi-turn conversation context (~2KB prompt JSON) dengan instruksi sistem arsitektur terdistribusi. Menguji penanganan koneksi berdurasi panjang (*prolonged socket lifetime*), *buffer recycling* (`sync.Pool`), dan pencegahan kebocoran memori di bawah saturasi 1.000 koneksi bersamaan. |

---

## 2. Cara Menjalankan

### 2.1. Membangun Binary Standalone (Detached CLI)

Kompilasi binary loadtest ke folder `bin/`:
```bash
make build-loadtest
# atau
go build -o bin/loadtest ./cmd/loadtest
```

Lihat opsi dan bantuan perintah:
```bash
./bin/loadtest -h
```

---

### 2.2. Mode Mock Upstream (Zero-Setup & Zero-Cost)
Gunakan flag `-mock` untuk menjalankan gateway Firefly dan upstream mock secara *in-process*. Mode ini tidak membutuhkan koneksi internet, tidak memerlukan API key eksternal berbayar, dan memiliki kapasitas hingga ribuan koneksi konkuren.

#### A. Menjalankan 100 Request Simultan:
```bash
# Beban Rendah (100 request simultan, non-streaming)
./bin/loadtest -mock -c 100 -profile low

# Beban Sedang (100 request streaming SSE simultan)
./bin/loadtest -mock -c 100 -profile medium

# Beban Berat (100 request context berat & streaming)
./bin/loadtest -mock -c 100 -profile heavy
```

#### B. Menjalankan 1.000 Request Simultan:
```bash
# Beban Rendah (1.000 request simultan)
./bin/loadtest -mock -c 1000 -profile low

# Beban Sedang (1.000 stream SSE simultan)
./bin/loadtest -mock -c 1000 -profile medium

# Beban Berat (1.000 stream context berat simultan)
./bin/loadtest -mock -c 1000 -profile heavy
```

#### C. Menjalankan Matriks Pengujian Lengkap (Benchmark Suite):
Perintah ini akan menjalankan seluruh 6 skenario secara bertahap dan mencetak tabel perbandingan performa:
```bash
make loadtest-suite
# atau
./bin/loadtest -mock -suite
```

---

### 2.3. Menjalankan Terhadap Server Firefly yang Sedang Berjalan (Live Instance)
Jika Firefly sudah berjalan di server lokal atau remote (misal `http://localhost:8080`):
```bash
# Menguji gateway aktif dengan model dan API key tenant tertentu
./bin/loadtest -url http://localhost:8080 \
  -key sk-gw-demo-000000000000000000000000 \
  -model gemma4 \
  -c 100 \
  -profile medium
```

> **Catatan Rate Limiting:**
> Jika tenant di `configs/tenants.json` memiliki batas limitasi ketat (misal `max_concurrent: 20` atau `rps: 20`), runner akan secara akurat menampilkan request yang sukses (`200 OK`) dan request yang terkena pembatasan rate limit (`429 Too Many Requests`). Ini memverifikasi bahwa Layer 2 Admission Firefly bekerja melindungi backend dari lonjakan beban tak terkendali.

---

### 2.4. Menjalankan via Go Test (Manual Test Suite)

File pengujian di [`cmd/loadtest/load_test.go`](file:///Users/dickymuliafiqri/go/src/github.com/dickymuliafiqri/gorouter/cmd/loadtest/load_test.go) diproteksi dengan tag `manual` sehingga tidak akan memperlambat perintah `go test ./...` biasa.

Untuk menjalankan pengujian melalui harness Go test:
```bash
# Jalankan seluruh suite benchmark
make test-load
# atau
go test -v -tags manual -run TestLoadSuite ./cmd/loadtest

# Menjalankan spesifik 100 konkuren
go test -v -tags manual -run TestLoad100Concurrent ./cmd/loadtest

# Menjalankan spesifik 1.000 konkuren
go test -v -tags manual -run TestLoad1000Concurrent ./cmd/loadtest
```

---

## 3. Struktur Laporan Metrik

Setiap pengujian menyajikan data observabilitas lengkap:

1. **Throughput & Data Transferred:**
   - Total waktu eksekusi (`Total Duration`).
   - Request per detik (`Throughput RPS`).
   - Token per detik (`Token Throughput`) untuk respons streaming.
   - Total transfer payload data.
2. **HTTP Status Breakdown:**
   - `✓ 200 OK`: Jumlah dan persentase keberhasilan.
   - `⚠ 429 Too Many Requests`: Request yang ditolak oleh Layer 1 / Layer 2 Rate Limiter.
   - `✗ 5xx Server Errors`: Gangguan upstream atau gateway.
   - `✗ 4xx Client Errors`: Error otentikasi / validasi parameter.
   - `✗ Network/Conn Errors`: Timeout, TCP reset, atau kehabisan file descriptor.
3. **Distribusi Latensi (Time to Complete Response):**
   - Min, P50 (Median), P90, P95, P99, Max, Mean, dan Standar Deviasi.
4. **Time To First Token (TTFT):**
   - Diukur khusus untuk koneksi streaming SSE: waktu sejak request dikirim hingga karakter token pertama diterima oleh klien.
