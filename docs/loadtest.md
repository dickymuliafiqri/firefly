# Firefly High-Concurrency Load Testing & Benchmark Guide

This guide documents the load testing and benchmarking tool used to stress the `/v1/chat/completions` endpoint of the **Firefly** gateway under **100 and 1,000 simultaneous requests** (*at the exact same millisecond*), across three workload profiles: **Low**, **Medium**, and **Heavy**.

The tool is built as a **standalone (detached) binary** in [`cmd/loadtest`](../cmd/loadtest), and ships with a **manual test suite** isolated from the regular CI process via the `//go:build manual` build tag.

---

## 1. Test Architecture & Workload Characteristics

### 1.1. Synchronized Barrier Concurrency Mechanism
To ensure that all requests (100 or 1,000) truly hit the gateway **at the same instant**, the runner uses a *two-phase barrier* synchronization:
1. All $C$ worker goroutines are spawned up front.
2. Each worker prepares its HTTP request payload in memory and reports readiness (`readyWg.Done()`).
3. Every worker blocks on the barrier channel `<-startBarrier`.
4. Once all workers are $100\%$ ready, the barrier is released at once (`close(startBarrier)`), unleashing 100 or 1,000 connections to Firefly at the same millisecond.

---

### 1.2. Three Workload Profiles

| Workload Profile | Stream Mode | Max Tokens | Payload Characteristics & Test Scenario |
| :--- | :--- | :--- | :--- |
| **Low (`low`)** | `stream: false` | 16 | **Minimal Prompt:** A short single-turn prompt (*"Ping! Respond with 'pong'"*). Tests raw throughput (raw RPS), minimal memory allocation, and gateway proxy round-trip speed. |
| **Medium (`medium`)** | `stream: true` (SSE) | 128 | **Interactive Chat:** A technical prompt (~150 words) about gateway architecture. Tests streaming Server-Sent Events (SSE) token consumption, streaming buffer allocation, and measures **TTFT** (Time to First Token). |
| **Heavy (`heavy`)** | `stream: true` (SSE) | 512 | **High-Context Stress Test:** A multi-turn conversation context (~2KB JSON prompt) with distributed-architecture system instructions. Tests prolonged socket lifetime, buffer recycling (`sync.Pool`), and memory-leak prevention under 1,000 concurrent connections. |

---

## 2. How to Run

### 2.1. Building the Standalone Binary (Detached CLI)

Compile the loadtest binary into the `bin/` folder:
```bash
make build-loadtest
# or
go build -o bin/loadtest ./cmd/loadtest
```

View the available options and command help:
```bash
./bin/loadtest -h
```

---

### 2.2. Mock Upstream Mode (Zero-Setup & Zero-Cost)
Use the `-mock` flag to run the Firefly gateway and a mock upstream in-process. This mode needs no internet connection, no paid external API key, and can handle thousands of concurrent connections.

#### A. Running 100 Simultaneous Requests:
```bash
# Low load (100 simultaneous requests, non-streaming)
./bin/loadtest -mock -c 100 -profile low

# Medium load (100 simultaneous SSE streaming requests)
./bin/loadtest -mock -c 100 -profile medium

# Heavy load (100 high-context streaming requests)
./bin/loadtest -mock -c 100 -profile heavy
```

#### B. Running 1,000 Simultaneous Requests:
```bash
# Low load (1,000 simultaneous requests)
./bin/loadtest -mock -c 1000 -profile low

# Medium load (1,000 simultaneous SSE streams)
./bin/loadtest -mock -c 1000 -profile medium

# Heavy load (1,000 simultaneous high-context streams)
./bin/loadtest -mock -c 1000 -profile heavy
```

#### C. Running the Full Test Matrix (Benchmark Suite):
This command runs all 6 scenarios in sequence and prints a performance comparison table:
```bash
make loadtest-suite
# or
./bin/loadtest -mock -suite
```

---

### 2.3. Running Against a Live Firefly Server (Live Instance)
If Firefly is already running on a local or remote server (for example `http://localhost:8080`):
```bash
# Test a live gateway with a specific model and tenant API key
./bin/loadtest -url http://localhost:8080 \
  -key sk-gw-demo-000000000000000000000000 \
  -model gemma4 \
  -c 100 \
  -profile medium
```

> **Rate Limiting Note:**
> If the tenant in `configs/tenants.json` has strict limits (for example `max_concurrent: 20` or `rps: 20`), the runner accurately reports both successful requests (`200 OK`) and rate-limited requests (`429 Too Many Requests`). This verifies that Firefly's Layer 2 admission control protects the backend from uncontrolled load spikes.

---

### 2.4. Running via Go Test (Manual Test Suite)

The test file at [`cmd/loadtest/load_test.go`](../cmd/loadtest/load_test.go) is protected by the `manual` build tag so it does not slow down a plain `go test ./...` run.

To run the tests through the Go test harness:
```bash
# Run the entire benchmark suite
make test-load
# or
go test -v -tags manual -run TestLoadSuite ./cmd/loadtest

# Run only the 100-concurrent test
go test -v -tags manual -run TestLoad100Concurrent ./cmd/loadtest

# Run only the 1,000-concurrent test
go test -v -tags manual -run TestLoad1000Concurrent ./cmd/loadtest
```

---

## 3. Metric Report Structure

Each test run reports a full set of observability data:

1. **Throughput & Data Transferred:**
   - Total execution time (`Total Duration`).
   - Requests per second (`Throughput RPS`).
   - Tokens per second (`Token Throughput`) for streaming responses.
   - Total payload data transferred.
2. **HTTP Status Breakdown:**
   - `✓ 200 OK`: Count and percentage of successes.
   - `⚠ 429 Too Many Requests`: Requests rejected by the Layer 1 / Layer 2 rate limiter.
   - `✗ 5xx Server Errors`: Upstream or gateway failures.
   - `✗ 4xx Client Errors`: Authentication / parameter validation errors.
   - `✗ Network/Conn Errors`: Timeouts, TCP resets, or file-descriptor exhaustion.
3. **Latency Distribution (Time to Complete Response):**
   - Min, P50 (median), P90, P95, P99, Max, Mean, and standard deviation.
4. **Time To First Token (TTFT):**
   - Measured specifically for SSE streaming connections: the time from when the request is sent until the first token character is received by the client.
