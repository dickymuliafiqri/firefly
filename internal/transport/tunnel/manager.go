package tunnel

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultShutdownGrace = 10 * time.Second
)

type Mode int

const (
	ModeDisabled Mode = iota
	ModeQuick
	ModeNamed
)

// ParseMode parses a string into a tunnel Mode.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "quick":
		return ModeQuick
	case "named":
		return ModeNamed
	default:
		return ModeDisabled
	}
}

type Config struct {
	AppCtx     context.Context
	Mode       Mode
	LocalURL   string
	Token      string
	BinDir     string
	BinaryPath string
	Downloader DownloaderFunc
	Logger     *slog.Logger
}

type Manager struct {
	appCtx      context.Context
	cfg         Config
	logger      *slog.Logger
	publicURL   atomic.Value
	mu          sync.Mutex
	downloadMu  sync.Mutex
	cmd         *exec.Cmd
	cancel      context.CancelFunc
	running     atomic.Bool
	downloading atomic.Bool
	statusMsg   atomic.Value
	done        chan struct{}
}

// Status describes the current state of the Cloudflare Tunnel ingress engine.
type Status struct {
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	Mode        string `json:"mode"`
	PublicURL   string `json:"public_url,omitempty"`
	LocalURL    string `json:"local_url,omitempty"`
	Downloading bool   `json:"downloading,omitempty"`
	Message     string `json:"message,omitempty"`
}

var urlRegex = regexp.MustCompile(`https://[a-zA-Z0-9\-]+\.trycloudflare\.com`)

func NewManager(cfg Config) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	appCtx := cfg.AppCtx
	if appCtx == nil {
		appCtx = context.Background()
	}
	return &Manager{
		appCtx: appCtx,
		cfg:    cfg,
		logger: logger.With("component", "tunnel"),
		done:   make(chan struct{}),
	}
}
func (m *Manager) PublicURL() string {
	if v := m.publicURL.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// Status returns a snapshot of the tunnel status for API reporting.
func (m *Manager) Status() Status {
	msg := ""
	if v := m.statusMsg.Load(); v != nil {
		msg = v.(string)
	}
	return Status{
		Enabled:     m.cfg.Mode != ModeDisabled,
		Running:     m.IsRunning(),
		Mode:        m.modeString(),
		PublicURL:   m.PublicURL(),
		LocalURL:    m.cfg.LocalURL,
		Downloading: m.downloading.Load(),
		Message:     msg,
	}
}

func (m *Manager) IsRunning() bool { return m.running.Load() }

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *Manager) buildArgs() []string {
	switch m.cfg.Mode {
	case ModeQuick:
		return []string{"tunnel", "--url", m.cfg.LocalURL}
	case ModeNamed:
		return []string{"tunnel", "run", "--token", m.cfg.Token}
	default:
		return nil
	}
}

func (m *Manager) modeString() string {
	switch m.cfg.Mode {
	case ModeQuick:
		return "quick"
	case ModeNamed:
		return "named"
	default:
		return "disabled"
	}
}

func (m *Manager) streamReader(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if m.cfg.Mode == ModeQuick && m.PublicURL() == "" {
			if match := urlRegex.FindString(line); match != "" {
				m.publicURL.Store(match)
				m.logger.Info("tunnel is live", "public_url", match)
			}
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isBannerLine(trimmed) {
			continue
		}
		level := slog.LevelDebug
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "err") || strings.Contains(lower, "fail") {
			level = slog.LevelError
		} else if strings.Contains(lower, "warn") {
			level = slog.LevelWarn
		} else if strings.Contains(lower, "inf") {
			level = slog.LevelInfo
		}
		m.logger.Log(context.Background(), level, "cloudflared", "msg", trimmed)
	}
}

func isBannerLine(line string) bool {
	if len(line) == 0 {
		return true
	}
	c := line[0]
	return c == '+' || c == '|' || c == '='
}
