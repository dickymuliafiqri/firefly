package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestGracefulShutdownEndToEnd builds and runs the gateway, drives a request,
// then sends an interrupt and asserts the process (a) logs the graceful-drain
// sequence and (b) exits 0 — i.e. background workers were joined, not killed.
//
// The test is skipped on Windows where os.Interrupt cannot be delivered to a
// child process in the same way; CI runs it on Linux/macOS.
func TestGracefulShutdownEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt delivery to a child process is not supported on Windows")
	}

	bin := buildBinary(t)
	dir := writeMinimalConfig(t)

	dataAddr := freeAddr(t)
	adminAddr := freeAddr(t)

	cmd := exec.Command(bin,
		"-config-dir", dir,
		"-addr", dataAddr,
		"-admin-addr", adminAddr,
	)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=sk-test")
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.SysProcAttr = interruptAttr()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the data plane to answer /healthz.
	waitHealthy(t, "http://"+dataAddr+"/healthz")

	// Deliver SIGINT and require a clean, bounded exit.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error: %v\nlogs:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 10s\nlogs:\n%s", out.String())
	}

	logs := out.String()
	if !strings.Contains(logs, "graceful shutdown complete") {
		t.Fatalf("missing graceful shutdown log:\n%s", logs)
	}
	if !strings.Contains(logs, `"msg":"bye"`) {
		t.Fatalf("missing final 'bye' log (workers not joined?):\n%s", logs)
	}
}

// TestZeroConfigStartupEndToEnd runs the binary with a non-existent config directory.
// It verifies the server starts up cleanly, answers /healthz, exposes /api/settings,
// accepts settings configuration via POST /api/settings, and shuts down gracefully.
func TestZeroConfigStartupEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt delivery to a child process is not supported on Windows")
	}

	bin := buildBinary(t)
	// Non-existent directory
	dir := filepath.Join(t.TempDir(), "non-existent-configs")
	dataAddr := freeAddr(t)

	cmd := exec.Command(bin,
		"-config-dir", dir,
		"-addr", dataAddr,
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.SysProcAttr = interruptAttr()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 1. Wait for server to come up and answer /healthz
	waitHealthy(t, "http://"+dataAddr+"/healthz")

	// 2. Query /api/settings: should answer 200 OK
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + dataAddr + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. POST new settings via /api/settings
	newSettings := `{"upstreams":[{"name":"u1","base_url":"https://api.openai.com/v1","api_key":"sk-key"}],"models":[],"tenants":[]}`
	postResp, err := client.Post("http://"+dataAddr+"/api/settings", "application/json", strings.NewReader(newSettings))
	if err != nil {
		t.Fatalf("POST /api/settings: %v", err)
	}
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/settings status = %d, want 200", postResp.StatusCode)
	}
	postResp.Body.Close()

	// Deliver SIGINT and require clean exit
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error: %v\nlogs:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 10s\nlogs:\n%s", out.String())
	}
}

// TestDefaultConfigsWithoutEnvStartsCleanly verifies that starting the application
// pointing to the shipped configs directory without OPENAI_API_KEY does NOT crash with
// fatal validation error, but starts up smoothly and answers health/settings endpoints.
func TestDefaultConfigsWithoutEnvStartsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt delivery to a child process is not supported on Windows")
	}

	bin := buildBinary(t)
	// configs dir in repo root
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	dir := filepath.Join(root, "configs")
	dataAddr := freeAddr(t)

	cmd := exec.Command(bin,
		"-config-dir", dir,
		"-addr", dataAddr,
	)
	// Strip OPENAI_API_KEY from environment
	cleanEnv := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "OPENAI_API_KEY=") {
			cleanEnv = append(cleanEnv, e)
		}
	}
	cmd.Env = cleanEnv

	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.SysProcAttr = interruptAttr()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 1. Wait for server to come up and answer /healthz
	waitHealthy(t, "http://"+dataAddr+"/healthz")

	// 2. Query /api/settings: should answer 200 OK
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + dataAddr + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. Deliver SIGINT and require clean exit
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error: %v\nlogs:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 10s\nlogs:\n%s", out.String())
	}
}

// buildBinary compiles the command into a temp path.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "firefly-test")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	build.Env = os.Environ()
	if outp, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, outp)
	}
	return bin
}

// writeMinimalConfig writes a valid 3-file config set with no upstream calls.
func writeMinimalConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"upstreams.json": `{"upstreams":[{"name":"openai","base_url":"https://api.openai.com/v1",` +
			`"credential_ref":"OPENAI_API_KEY","protocol":"openai"}]}`,
		"models.json":  `{"models":[{"public_name":"gpt-4o","upstream":"openai","upstream_model":"gpt-4o","enabled":true}]}`,
		"tenants.json": `{"tenants":[]}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// freeAddr returns a currently-free TCP address on the loopback interface.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// waitHealthy polls until url answers 200.
func waitHealthy(t *testing.T, rawURL string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(rawURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s never came up", rawURL)
}
