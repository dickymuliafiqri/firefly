// Command loadtest is a high-concurrency LLM benchmark and stress-testing tool
// for the Firefly API gateway (/v1/chat/completions).
//
// It supports synchronized burst concurrency (100 and 1,000 simultaneous requests)
// across low, medium, and heavy workloads, with both live-target and standalone in-process
// mock upstream modes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		concurrency = flag.Int("c", 100, "concurrency level: number of simultaneous requests (e.g. 100 or 1000)")
		totalReqs   = flag.Int("n", 0, "total requests to dispatch (default: same as concurrency for synchronized burst)")
		profileName = flag.String("profile", "low", "workload profile: low (rendah), medium (sedang), heavy (berat)")
		suite       = flag.Bool("suite", false, "run the complete automated 6-scenario matrix (100 & 1000 requests across low/medium/heavy)")
		targetURL   = flag.String("url", "http://localhost:8080", "target Firefly gateway base URL")
		apiKey      = flag.String("key", "sk-gw-demo-000000000000000000000000", "tenant Bearer API key")
		modelName   = flag.String("model", "gemma4", "model name to request")
		useMock     = flag.Bool("mock", false, "launch an in-process mock upstream and Firefly gateway (no external network or key required)")
		timeout     = flag.Duration("timeout", 60*time.Second, "per-request timeout duration")
		outputJSON  = flag.Bool("json", false, "output report as JSON")
	)
	flag.Parse()

	// Intercept SIGINT/SIGTERM for graceful benchmark abort
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// If mock mode is chosen, spin up the in-process mock harness
	if *useMock {
		harness, err := StartMockHarness()
		if err != nil {
			return fmt.Errorf("failed to start mock harness: %w", err)
		}
		defer harness.Close()

		*targetURL = harness.BaseURL
		*modelName = harness.Model
		*apiKey = harness.APIKey

		if !*outputJSON {
			fmt.Printf(">> [MOCK HARNESS ACTIVE] Firefly listening on %s (Model: %s)\n\n", harness.BaseURL, harness.Model)
		}
	}

	if *suite {
		return runSuite(ctx, *targetURL, *apiKey, *modelName, *timeout, *outputJSON)
	}

	// Single scenario execution
	if *totalReqs <= 0 {
		*totalReqs = *concurrency
	}

	profile, payload, err := GetProfile(LoadProfile(*profileName), *modelName, nil)
	if err != nil {
		return err
	}

	if !*outputJSON {
		fmt.Printf(">> Starting Load Test: %d simultaneous requests [%s]\n", *concurrency, profile.Name)
		fmt.Printf("   Target : %s/v1/chat/completions\n", *targetURL)
		fmt.Printf("   Model  : %s | Stream: %t | MaxTokens: %d\n\n", *modelName, profile.Stream, profile.MaxTokens)
	}

	runCfg := RunConfig{
		URL:         *targetURL,
		APIKey:      *apiKey,
		Model:       *modelName,
		Concurrency: *concurrency,
		TotalReqs:   *totalReqs,
		Profile:     profile,
		Payload:     payload,
		Timeout:     *timeout,
		Warmup:      true,
	}

	stats, err := Execute(ctx, runCfg)
	if err != nil {
		return fmt.Errorf("load test execution failed: %w", err)
	}

	if *outputJSON {
		return PrintJSON(os.Stdout, stats)
	}

	PrintTable(os.Stdout, stats)
	return nil
}

// runSuite runs the full matrix of 100 and 1,000 concurrent requests across low, medium, and heavy workloads.
func runSuite(ctx context.Context, targetURL, apiKey, model string, timeout time.Duration, outputJSON bool) error {
	scenarios := []struct {
		concurrency int
		profile     LoadProfile
	}{
		{concurrency: 100, profile: ProfileLow},
		{concurrency: 100, profile: ProfileMedium},
		{concurrency: 100, profile: ProfileHeavy},
		{concurrency: 1000, profile: ProfileLow},
		{concurrency: 1000, profile: ProfileMedium},
		{concurrency: 1000, profile: ProfileHeavy},
	}

	if !outputJSON {
		fmt.Println("╔═════════════════════════════════════════════════════════════════════════════╗")
		fmt.Println("║               FIREFLY HIGH-CONCURRENCY BENCHMARK SUITE                      ║")
		fmt.Println("║       Scenarios: 100 & 1,000 Simultaneous Requests [Low / Med / Heavy]      ║")
		fmt.Println("╚═════════════════════════════════════════════════════════════════════════════╝")
		fmt.Printf(" Target URL: %s/v1/chat/completions | Model: %s\n\n", targetURL, model)
	}

	var allStats []*SummaryStats

	for i, sc := range scenarios {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		profile, payload, err := GetProfile(sc.profile, model, nil)
		if err != nil {
			return err
		}

		if !outputJSON {
			fmt.Printf(">> [%d/6] Executing %d Concurrent Requests | Workload: %s ...\n",
				i+1, sc.concurrency, sc.profile)
		}

		runCfg := RunConfig{
			URL:         targetURL,
			APIKey:      apiKey,
			Model:       model,
			Concurrency: sc.concurrency,
			TotalReqs:   sc.concurrency,
			Profile:     profile,
			Payload:     payload,
			Timeout:     timeout,
			Warmup:      i == 0,
		}

		stats, err := Execute(ctx, runCfg)
		if err != nil {
			return fmt.Errorf("scenario %s c=%d failed: %w", sc.profile, sc.concurrency, err)
		}

		allStats = append(allStats, stats)

		if !outputJSON {
			PrintTable(os.Stdout, stats)
		}

		// Brief pause between scenarios to allow sockets and goroutines to settle
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	if outputJSON {
		return PrintJSON(os.Stdout, allStats)
	}

	// Overall Suite Summary Table
	fmt.Println("╔═════════════════════════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                                  BENCHMARK SUITE SUMMARY MATRIX                                     ║")
	fmt.Println("╠═════════════╦═════════════╦══════════╦══════════════╦══════════════╦══════════════╦═════════════════╣")
	fmt.Println("║ Concurrency ║ Profile     ║ Requests ║ Success Rate ║ Throughput   ║ P50 Latency  ║ P99 Latency     ║")
	fmt.Println("╠═════════════╬═════════════╬══════════╬══════════════╬══════════════╬══════════════╬═════════════════╣")
	for _, s := range allStats {
		successRate := float64(s.SuccessRequests) / float64(s.TotalRequests) * 100
		fmt.Printf("║ %-11d ║ %-11s ║ %-8d ║ %10.1f%%  ║ %8.1f rps ║ %10s   ║ %10s      ║\n",
			s.Concurrency, s.Profile, s.TotalRequests, successRate, s.ThroughputRPS,
			s.LatencyP50.Round(time.Millisecond), s.LatencyP99.Round(time.Millisecond))
	}
	fmt.Println("╚═════════════╩═════════════╩══════════╩══════════════╩══════════════╩══════════════╩═════════════════╝")

	return nil
}
