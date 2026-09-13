//go:build manual

package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLoad100Concurrent exercises 100 simultaneous requests against the in-process mock Firefly gateway.
// Run with: go test -v -tags manual -run TestLoad100Concurrent ./cmd/loadtest
func TestLoad100Concurrent(t *testing.T) {
	harness, err := StartMockHarness()
	if err != nil {
		t.Fatalf("failed to start mock harness: %v", err)
	}
	defer harness.Close()

	profiles := []LoadProfile{ProfileLow, ProfileMedium, ProfileHeavy}

	for _, p := range profiles {
		t.Run(string(p), func(t *testing.T) {
			profile, payload, err := GetProfile(p, harness.Model, nil)
			if err != nil {
				t.Fatalf("get profile: %v", err)
			}

			cfg := RunConfig{
				URL:         harness.BaseURL,
				APIKey:      harness.APIKey,
				Model:       harness.Model,
				Concurrency: 100,
				TotalReqs:   100,
				Profile:     profile,
				Payload:     payload,
				Timeout:     30 * time.Second,
				Warmup:      true,
			}

			stats, err := Execute(context.Background(), cfg)
			if err != nil {
				t.Fatalf("execute load test: %v", err)
			}

			if testing.Verbose() {
				PrintTable(os.Stdout, stats)
			}

			if stats.SuccessRequests != 100 {
				t.Errorf("expected 100 successful requests, got %d (rate_limited=%d, errors=%d)",
					stats.SuccessRequests, stats.RateLimited, stats.ServerErrors+stats.NetworkErrors)
			}
		})
	}
}

// TestLoad1000Concurrent exercises 1,000 simultaneous requests against the in-process mock Firefly gateway.
// Run with: go test -v -tags manual -run TestLoad1000Concurrent ./cmd/loadtest
func TestLoad1000Concurrent(t *testing.T) {
	harness, err := StartMockHarness()
	if err != nil {
		t.Fatalf("failed to start mock harness: %v", err)
	}
	defer harness.Close()

	profiles := []LoadProfile{ProfileLow, ProfileMedium, ProfileHeavy}

	for _, p := range profiles {
		t.Run(string(p), func(t *testing.T) {
			profile, payload, err := GetProfile(p, harness.Model, nil)
			if err != nil {
				t.Fatalf("get profile: %v", err)
			}

			cfg := RunConfig{
				URL:         harness.BaseURL,
				APIKey:      harness.APIKey,
				Model:       harness.Model,
				Concurrency: 1000,
				TotalReqs:   1000,
				Profile:     profile,
				Payload:     payload,
				Timeout:     60 * time.Second,
				Warmup:      true,
			}

			stats, err := Execute(context.Background(), cfg)
			if err != nil {
				t.Fatalf("execute load test: %v", err)
			}

			if testing.Verbose() {
				PrintTable(os.Stdout, stats)
			}

			if stats.SuccessRequests != 1000 {
				t.Errorf("expected 1000 successful requests, got %d (rate_limited=%d, errors=%d)",
					stats.SuccessRequests, stats.RateLimited, stats.ServerErrors+stats.NetworkErrors)
			}
		})
	}
}

// TestLoadSuite runs the full benchmark matrix.
// Run with: go test -v -tags manual -run TestLoadSuite ./cmd/loadtest
func TestLoadSuite(t *testing.T) {
	harness, err := StartMockHarness()
	if err != nil {
		t.Fatalf("failed to start mock harness: %v", err)
	}
	defer harness.Close()

	err = runSuite(context.Background(), harness.BaseURL, harness.APIKey, harness.Model, 60*time.Second, false)
	if err != nil {
		t.Fatalf("runSuite failed: %v", err)
	}
}
