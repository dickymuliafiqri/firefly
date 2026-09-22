package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RequestResult records the outcome and timing of a single HTTP request.
type RequestResult struct {
	WorkerID     int           `json:"worker_id"`
	StatusCode   int           `json:"status_code"`
	Err          error         `json:"-"`
	ErrorMessage string        `json:"error,omitempty"`
	Duration     time.Duration `json:"duration"`
	TTFT         time.Duration `json:"ttft,omitempty"` // Time to first token (if streaming)
	Bytes        int64         `json:"bytes"`
	TokenCount   int           `json:"token_count"`
	IsStream     bool          `json:"is_stream"`
}

// RunConfig defines the parameters for a load test run.
type RunConfig struct {
	URL         string
	APIKey      string
	Model       string
	Concurrency int
	TotalReqs   int
	Profile     ProfileConfig
	Payload     []byte
	Timeout     time.Duration
	Warmup      bool
}

// SummaryStats aggregates the results of a load test run.
type SummaryStats struct {
	Profile         string        `json:"profile"`
	Description     string        `json:"description"`
	Concurrency     int           `json:"concurrency"`
	TotalRequests   int           `json:"total_requests"`
	SuccessRequests int           `json:"success_requests"`
	RateLimited     int           `json:"rate_limited_429"`
	ServerErrors    int           `json:"server_errors_5xx"`
	ClientErrors    int           `json:"client_errors_4xx"`
	NetworkErrors   int           `json:"network_errors"`
	TotalDuration   time.Duration `json:"total_duration"`
	ThroughputRPS   float64       `json:"throughput_rps"`
	TotalBytes      int64         `json:"total_bytes"`
	TotalTokens     int64         `json:"total_tokens"`
	TokensPerSec    float64       `json:"tokens_per_sec,omitempty"`
	StatusCodes     map[int]int   `json:"status_codes"`
	LatencyMin      time.Duration `json:"latency_min"`
	LatencyP50      time.Duration `json:"latency_p50"`
	LatencyP90      time.Duration `json:"latency_p90"`
	LatencyP95      time.Duration `json:"latency_p95"`
	LatencyP99      time.Duration `json:"latency_p99"`
	LatencyMax      time.Duration `json:"latency_max"`
	LatencyMean     time.Duration `json:"latency_mean"`
	LatencyStdDev   time.Duration `json:"latency_std_dev"`
	TTFTMin         time.Duration `json:"ttft_min,omitempty"`
	TTFTP50         time.Duration `json:"ttft_p50,omitempty"`
	TTFTP90         time.Duration `json:"ttft_p90,omitempty"`
	TTFTP99         time.Duration `json:"ttft_p99,omitempty"`
	SampleError     string        `json:"sample_error,omitempty"`
	IsStream        bool          `json:"is_stream"`
}

// Execute runs the load test with synchronized concurrency.
func Execute(ctx context.Context, cfg RunConfig) (*SummaryStats, error) {
	if cfg.Concurrency <= 0 {
		return nil, fmt.Errorf("concurrency must be > 0 (got %d)", cfg.Concurrency)
	}
	if cfg.TotalReqs <= 0 {
		cfg.TotalReqs = cfg.Concurrency
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}

	endpoint := strings.TrimRight(cfg.URL, "/") + "/v1/chat/completions"

	// High-throughput HTTP client tuned for 1,000+ simultaneous connections.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          3000,
		MaxIdleConnsPerHost:   3000,
		MaxConnsPerHost:       3000,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer transport.CloseIdleConnections()

	// Optional warmup: 3 quick requests to establish TLS handshakes and connection pools.
	if cfg.Warmup {
		warmupCtx, cancelWarmup := context.WithTimeout(ctx, 5*time.Second)
		for i := 0; i < 3; i++ {
			_ = executeSingleRequest(warmupCtx, client, endpoint, cfg.APIKey, cfg.Payload, cfg.Profile.Stream, -1)
		}
		cancelWarmup()
	}

	results := make([]RequestResult, cfg.TotalReqs)
	jobs := make(chan int, cfg.TotalReqs)
	for i := 0; i < cfg.TotalReqs; i++ {
		jobs <- i
	}
	close(jobs)

	// Synchronized Barrier: All C workers wait on startBarrier before firing simultaneously!
	var readyWg sync.WaitGroup
	readyWg.Add(cfg.Concurrency)
	startBarrier := make(chan struct{})

	var doneWg sync.WaitGroup
	doneWg.Add(cfg.Concurrency)

	var completedCounter atomic.Int64

	for workerID := 0; workerID < cfg.Concurrency; workerID++ {
		go func(wid int) {
			defer doneWg.Done()

			// Signal readiness for synchronized release
			readyWg.Done()
			<-startBarrier

			for jobID := range jobs {
				if ctx.Err() != nil {
					results[jobID] = RequestResult{
						WorkerID:     wid,
						Err:          ctx.Err(),
						ErrorMessage: ctx.Err().Error(),
					}
					completedCounter.Add(1)
					continue
				}

				res := executeSingleRequest(ctx, client, endpoint, cfg.APIKey, cfg.Payload, cfg.Profile.Stream, wid)
				results[jobID] = res
				completedCounter.Add(1)
			}
		}(workerID)
	}

	// Wait for all workers to spawn and prepare
	readyWg.Wait()

	// FIRE! All concurrent workers launch simultaneously at this precise instant.
	testStart := time.Now()
	close(startBarrier)

	// Wait for all requests to finish
	doneWg.Wait()
	testDuration := time.Since(testStart)

	return aggregateStats(cfg, results, testDuration), nil
}

// executeSingleRequest dispatches one HTTP request and records its metrics.
func executeSingleRequest(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	apiKey string,
	payload []byte,
	isStream bool,
	workerID int,
) RequestResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return RequestResult{
			WorkerID:     workerID,
			Err:          err,
			ErrorMessage: err.Error(),
		}
	}

	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	reqStart := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return RequestResult{
			WorkerID:     workerID,
			Duration:     time.Since(reqStart),
			Err:          err,
			ErrorMessage: err.Error(),
			IsStream:     isStream,
		}
	}
	defer resp.Body.Close()

	var (
		ttft         time.Duration
		tokenCount   int
		totalBytes   int64
		firstTokenOk bool
	)

	// Determine if response is an SSE stream
	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(contentType, "text/event-stream")

	if resp.StatusCode == http.StatusOK && isSSE {
		reader := bufio.NewReaderSize(resp.Body, 32*1024)
		for {
			line, err := reader.ReadBytes('\n')
			totalBytes += int64(len(line))

			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				if !firstTokenOk {
					ttft = time.Since(reqStart)
					firstTokenOk = true
				}
				dataContent := bytes.TrimPrefix(trimmed, []byte("data:"))
				dataContent = bytes.TrimSpace(dataContent)
				if !bytes.Equal(dataContent, []byte("[DONE]")) {
					tokenCount++
				}
			}

			if err != nil {
				break
			}
		}
	} else {
		bodyBytes, _ := io.ReadAll(resp.Body)
		totalBytes = int64(len(bodyBytes))
		if resp.StatusCode != http.StatusOK {
			return RequestResult{
				WorkerID:     workerID,
				StatusCode:   resp.StatusCode,
				Duration:     time.Since(reqStart),
				Bytes:        totalBytes,
				ErrorMessage: string(bodyBytes),
				IsStream:     isStream,
			}
		}
	}

	duration := time.Since(reqStart)

	return RequestResult{
		WorkerID:   workerID,
		StatusCode: resp.StatusCode,
		Duration:   duration,
		TTFT:       ttft,
		Bytes:      totalBytes,
		TokenCount: tokenCount,
		IsStream:   isSSE || isStream,
	}
}

// aggregateStats compiles statistical metrics across all request results.
func aggregateStats(cfg RunConfig, results []RequestResult, totalDuration time.Duration) *SummaryStats {
	stats := &SummaryStats{
		Profile:       string(cfg.Profile.Name),
		Description:   cfg.Profile.Description,
		Concurrency:   cfg.Concurrency,
		TotalRequests: len(results),
		TotalDuration: totalDuration,
		ThroughputRPS: float64(len(results)) / totalDuration.Seconds(),
		StatusCodes:   make(map[int]int),
		IsStream:      cfg.Profile.Stream,
	}

	var (
		durations []time.Duration
		ttfts     []time.Duration
		sumDurMs  float64
	)

	for _, r := range results {
		if r.ErrorMessage != "" && stats.SampleError == "" {
			stats.SampleError = r.ErrorMessage
		}

		if r.Err != nil {
			stats.NetworkErrors++
			continue
		}

		stats.StatusCodes[r.StatusCode]++
		stats.TotalBytes += r.Bytes
		stats.TotalTokens += int64(r.TokenCount)

		durations = append(durations, r.Duration)
		sumDurMs += float64(r.Duration.Milliseconds())

		if r.StatusCode == http.StatusOK {
			stats.SuccessRequests++
			if r.TTFT > 0 {
				ttfts = append(ttfts, r.TTFT)
			}
		} else if r.StatusCode == http.StatusTooManyRequests {
			stats.RateLimited++
		} else if r.StatusCode >= 500 {
			stats.ServerErrors++
		} else if r.StatusCode >= 400 {
			stats.ClientErrors++
		}
	}

	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		stats.LatencyMin = durations[0]
		stats.LatencyMax = durations[len(durations)-1]
		stats.LatencyP50 = percentile(durations, 0.50)
		stats.LatencyP90 = percentile(durations, 0.90)
		stats.LatencyP95 = percentile(durations, 0.95)
		stats.LatencyP99 = percentile(durations, 0.99)

		meanMs := sumDurMs / float64(len(durations))
		stats.LatencyMean = time.Duration(meanMs * float64(time.Millisecond))

		// Standard deviation
		var varianceSum float64
		for _, d := range durations {
			diff := float64(d.Milliseconds()) - meanMs
			varianceSum += diff * diff
		}
		stdDevMs := math.Sqrt(varianceSum / float64(len(durations)))
		stats.LatencyStdDev = time.Duration(stdDevMs * float64(time.Millisecond))
	}

	if len(ttfts) > 0 {
		sort.Slice(ttfts, func(i, j int) bool { return ttfts[i] < ttfts[j] })
		stats.TTFTMin = ttfts[0]
		stats.TTFTP50 = percentile(ttfts, 0.50)
		stats.TTFTP90 = percentile(ttfts, 0.90)
		stats.TTFTP99 = percentile(ttfts, 0.99)
	}

	if stats.TotalTokens > 0 && totalDuration.Seconds() > 0 {
		stats.TokensPerSec = float64(stats.TotalTokens) / totalDuration.Seconds()
	}

	return stats
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(p * float64(len(sorted)-1)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

// PrintTable formats and prints the benchmark summary in a structured ASCII terminal format.
func PrintTable(w io.Writer, stats *SummaryStats) {
	div := "─────────────────────────────────────────────────────────────────────────────"
	boldDiv := "═════════════════════════════════════════════════════════════════════════════"

	fmt.Fprintln(w, boldDiv)
	fmt.Fprintf(w, " LOAD TEST REPORT — %s (Concurrency: %d, Total: %d)\n",
		strings.ToUpper(stats.Profile), stats.Concurrency, stats.TotalRequests)
	fmt.Fprintln(w, boldDiv)
	fmt.Fprintf(w, " Workload Profile   : %s\n", stats.Description)
	fmt.Fprintf(w, " Total Duration     : %s\n", stats.TotalDuration.Round(time.Millisecond))
	fmt.Fprintf(w, " Throughput         : %.2f req/sec\n", stats.ThroughputRPS)
	if stats.TokensPerSec > 0 {
		fmt.Fprintf(w, " Token Throughput   : %.2f tokens/sec (Total: %d tokens)\n",
			stats.TokensPerSec, stats.TotalTokens)
	}
	fmt.Fprintf(w, " Data Transferred   : %.2f KB\n", float64(stats.TotalBytes)/1024.0)
	fmt.Fprintln(w, div)

	// Status Distribution
	fmt.Fprintln(w, " HTTP STATUS BREAKDOWN:")
	successPct := float64(stats.SuccessRequests) / float64(stats.TotalRequests) * 100
	fmt.Fprintf(w, "   ✓ 200 OK          : %6d  (%6.2f%%)\n", stats.SuccessRequests, successPct)
	if stats.RateLimited > 0 {
		rlPct := float64(stats.RateLimited) / float64(stats.TotalRequests) * 100
		fmt.Fprintf(w, "   ⚠ 429 Too Many Req: %6d  (%6.2f%%) [Layer 2 Tenant/Admission Limits]\n", stats.RateLimited, rlPct)
	}
	if stats.ServerErrors > 0 {
		sePct := float64(stats.ServerErrors) / float64(stats.TotalRequests) * 100
		fmt.Fprintf(w, "   ✗ 5xx Server Error: %6d  (%6.2f%%)\n", stats.ServerErrors, sePct)
	}
	if stats.ClientErrors > 0 {
		cePct := float64(stats.ClientErrors) / float64(stats.TotalRequests) * 100
		fmt.Fprintf(w, "   ✗ 4xx Client Error: %6d  (%6.2f%%)\n", stats.ClientErrors, cePct)
	}
	if stats.NetworkErrors > 0 {
		nePct := float64(stats.NetworkErrors) / float64(stats.TotalRequests) * 100
		fmt.Fprintf(w, "   ✗ Network/Conn Err: %6d  (%6.2f%%)\n", stats.NetworkErrors, nePct)
	}
	if stats.SampleError != "" {
		fmt.Fprintf(w, "   ℹ Sample Error Msg: %s\n", strings.TrimSpace(stats.SampleError))
	}

	fmt.Fprintln(w, div)
	fmt.Fprintln(w, " LATENCY DISTRIBUTION (Time to Complete Response):")
	fmt.Fprintf(w, "   Min   : %10s    P50 (Median) : %10s\n", stats.LatencyMin.Round(time.Millisecond), stats.LatencyP50.Round(time.Millisecond))
	fmt.Fprintf(w, "   P90   : %10s    P95          : %10s\n", stats.LatencyP90.Round(time.Millisecond), stats.LatencyP95.Round(time.Millisecond))
	fmt.Fprintf(w, "   P99   : %10s    Max          : %10s\n", stats.LatencyP99.Round(time.Millisecond), stats.LatencyMax.Round(time.Millisecond))
	fmt.Fprintf(w, "   Mean  : %10s    StdDev       : %10s\n", stats.LatencyMean.Round(time.Millisecond), stats.LatencyStdDev.Round(time.Millisecond))

	if stats.IsStream && stats.TTFTP50 > 0 {
		fmt.Fprintln(w, div)
		fmt.Fprintln(w, " TIME TO FIRST TOKEN (TTFT / Latency to Stream Start):")
		fmt.Fprintf(w, "   Min   : %10s    P50 (Median) : %10s\n", stats.TTFTMin.Round(time.Millisecond), stats.TTFTP50.Round(time.Millisecond))
		fmt.Fprintf(w, "   P90   : %10s    P99          : %10s\n", stats.TTFTP90.Round(time.Millisecond), stats.TTFTP99.Round(time.Millisecond))
	}

	fmt.Fprintln(w, boldDiv)
	fmt.Fprintln(w)
}

// PrintJSON formats the stats as formatted JSON.
func PrintJSON(w io.Writer, stats any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(stats)
}
