package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Start launches a cloudflared tunnel process in background.
// If another tunnel is already running, it stops it first before starting the new one.
func (m *Manager) Start(mode Mode, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running.Load() {
		m.stopLocked()
	}

	if mode == ModeDisabled {
		mode = ModeQuick
	}
	if mode == ModeNamed && token == "" && m.cfg.Token == "" {
		return errors.New("tunnel: token is required for named tunnel mode")
	}

	appCtx := m.appCtx
	if appCtx == nil {
		appCtx = context.Background()
	}

	binPath, err := m.resolveBinary(appCtx)
	if err != nil {
		return fmt.Errorf("tunnel: %w", err)
	}

	m.cfg.Mode = mode
	if token != "" {
		m.cfg.Token = token
	}
	m.publicURL.Store("")

	args := m.buildArgs()

	cmdCtx, cancel := context.WithCancel(appCtx)
	cmd := exec.CommandContext(cmdCtx, binPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("tunnel: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("tunnel: stderr pipe: %w", err)
	}

	m.cmd = cmd
	m.cancel = cancel
	done := make(chan struct{})
	m.done = done

	m.logger.Info("starting cloudflared tunnel", "mode", m.modeString(), "local_url", m.cfg.LocalURL)
	if err := cmd.Start(); err != nil {
		cancel()
		m.cmd = nil
		m.cancel = nil
		return fmt.Errorf("tunnel: failed to start cloudflared: %w", err)
	}

	m.running.Store(true)

	go func() {
		defer cancel()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.streamReader(stdout) }()
		go func() { defer wg.Done(); m.streamReader(stderr) }()
		waitErr := cmd.Wait()
		m.running.Store(false)
		close(done)
		wg.Wait()
		if waitErr != nil && cmdCtx.Err() == nil {
			m.logger.Warn("cloudflared process terminated", "err", waitErr)
		}
	}()

	return nil
}

// Stop gracefully shuts down the running cloudflared process.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopLocked()
	m.cfg.Mode = ModeDisabled
	return nil
}

func (m *Manager) stopLocked() {
	if m.cmd == nil || !m.running.Load() {
		return
	}
	m.logger.Info("shutting down cloudflared tunnel")
	if m.cancel != nil {
		m.cancel()
	}
	if m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	done := m.done
	if done != nil {
		select {
		case <-done:
		case <-time.After(DefaultShutdownGrace):
			m.logger.Warn("cloudflared did not exit within grace period")
		}
	}
	m.running.Store(false)
	m.publicURL.Store("")
	m.cmd = nil
	m.cancel = nil
}

func (m *Manager) resolveBinary(ctx context.Context) (string, error) {
	if m.cfg.BinaryPath != "" {
		return m.cfg.BinaryPath, nil
	}

	if bin, ok := FindBinary(m.cfg.BinDir); ok {
		return bin, nil
	}

	m.downloadMu.Lock()
	defer m.downloadMu.Unlock()

	// Double-check inside lock
	if bin, ok := FindBinary(m.cfg.BinDir); ok {
		return bin, nil
	}

	destDir := m.cfg.BinDir
	if destDir == "" {
		var err error
		destDir, err = DefaultBinaryDir()
		if err != nil {
			return "", fmt.Errorf("resolve default binary dir: %w", err)
		}
	}

	targetPath := filepath.Join(destDir, BinaryName())

	m.downloading.Store(true)
	m.statusMsg.Store("Downloading cloudflared binary...")
	defer func() {
		m.downloading.Store(false)
		m.statusMsg.Store("")
	}()

	downloader := m.cfg.Downloader
	if downloader == nil {
		downloader = DownloadBinary
	}

	if err := downloader(ctx, targetPath, m.logger); err != nil {
		return "", fmt.Errorf("auto-download cloudflared failed: %w", err)
	}

	return targetPath, nil
}

// Run executes the tunnel manager life cycle tied to the application context.
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	m.appCtx = ctx
	initialMode := m.cfg.Mode
	initialToken := m.cfg.Token
	m.mu.Unlock()

	if initialMode != ModeDisabled {
		if err := m.Start(initialMode, initialToken); err != nil {
			m.logger.Error("failed to start initial tunnel", "err", err)
		}
	}

	<-ctx.Done()
	m.Close()
	return ctx.Err()
}
