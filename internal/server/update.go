package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dickymuliafiqri/firefly/internal/selfupdate"
	"github.com/dickymuliafiqri/firefly/internal/supervisor"
)

const (
	// maxReleaseNotes caps the release body relayed to the dashboard. Release
	// notes are unrestricted markdown authored by hand; the panel renders a
	// preview, not a printing press.
	maxReleaseNotes = 4000

	// defaultRestartDelay is how long the process waits after a successful
	// install before replacing itself. It exists so the dashboard's poll can
	// observe the finished state and report success to the operator instead of
	// losing the connection mid-answer.
	defaultRestartDelay = 3 * time.Second

	// statusTTL collapses the dashboard's poll rate onto the release lookup.
	// The underlying client caches for 15 minutes; this shorter window merely
	// makes a burst of concurrent polls share one computation.
	statusTTL = 5 * time.Second

	// releaseLookupTimeout bounds a single GitHub call so a slow upstream can
	// never stall the status endpoint: on timeout the client falls back to the
	// release it already has.
	releaseLookupTimeout = 8 * time.Second
)

// ErrUpdateInProgress reports that an install is already running. Callers map it
// to 409 rather than starting a second download into the same staging path.
var ErrUpdateInProgress = errors.New("an update is already in progress")

// UpdateManager owns the release lookup, the host capability probe, and the
// single in-flight install that backs the dashboard's update panel.
//
// It is deliberately its own object rather than fields on RouterDeps: the
// install outlives the request that starts it, so its state must live somewhere
// that is not request-scoped.
type UpdateManager struct {
	client    *selfupdate.Client
	installer *selfupdate.Installer
	env       selfupdate.Environment

	version   string
	commit    string
	buildDate string

	restart      func(reason string)
	restartDelay time.Duration
	statusTTL    time.Duration
	logger       *slog.Logger

	mu sync.Mutex
	// release lookup cache
	release   *selfupdate.Release
	releaseAt time.Time
	lookErr   string
	lookStale bool
	// install state
	running   bool
	done      bool
	progress  selfupdate.Progress
	result    *selfupdate.InstallResult
	installEr string
	started   time.Time
	finished  time.Time
	restarted bool
}

// UpdateConfig parameterises NewUpdateManager. Every field is optional; the
// version strings default to "dev" and the restart hook to a no-op so a build
// without ldflags still serves a coherent (if inert) status.
type UpdateConfig struct {
	CurrentVersion string
	CurrentCommit  string
	BuildDate      string
	// Restart is invoked once, after a successful install, with the reason the
	// process should come back up: it must cancel the root context so the
	// existing graceful shutdown drains in-flight streams.
	Restart func(reason string)
	// Client and Logger are injected for tests; production leaves them nil.
	Client       *selfupdate.Client
	Logger       *slog.Logger
	RestartDelay time.Duration
	// Environment overrides the probed host capabilities. It exists for tests,
	// which must not depend on where the test binary happens to run from or on
	// whether the build host is itself containerised; production leaves it nil
	// so the host is probed.
	Environment *selfupdate.Environment
	// StatusTTL bounds how long a release lookup is reused across status calls.
	// Tests shrink it; production uses statusTTL.
	StatusTTL time.Duration
}

// NewUpdateManager builds the manager and probes the host once. The environment
// is resolved at construction rather than per request because the answer
// depends on the executable's location and the process's container entry point,
// neither of which changes while the process runs.
func NewUpdateManager(cfg UpdateConfig) *UpdateManager {
	client := cfg.Client
	if client == nil {
		client = selfupdate.NewClient(selfupdate.WithLogger(cfg.Logger))
	}
	env := selfupdate.DetectEnvironment()
	if cfg.Environment != nil {
		env = *cfg.Environment
	}

	version := cfg.CurrentVersion
	if version == "" {
		version = "dev"
	}
	delay := cfg.RestartDelay
	if delay <= 0 {
		delay = defaultRestartDelay
	}
	ttl := cfg.StatusTTL
	if ttl <= 0 {
		ttl = statusTTL
	}

	return &UpdateManager{
		client:       client,
		installer:    selfupdate.NewInstaller(client, cfg.Logger),
		env:          env,
		version:      version,
		commit:       cfg.CurrentCommit,
		buildDate:    cfg.BuildDate,
		restart:      cfg.Restart,
		restartDelay: delay,
		statusTTL:    ttl,
		logger:       cfg.Logger,
	}
}

// Environment exposes the probed host capabilities.
func (m *UpdateManager) Environment() selfupdate.Environment { return m.env }

// --- wire types -----------------------------------------------------------

type updateEnvironmentDTO struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	Container  bool   `json:"container"`
	Supervisor string `json:"supervisor,omitempty"`
	Executable string `json:"executable,omitempty"`
	Dir        string `json:"dir,omitempty"`
	Writable   bool   `json:"writable"`
	Restart    string `json:"restart_behavior,omitempty"`
}

type updateReleaseDTO struct {
	Tag         string `json:"tag"`
	Name        string `json:"name,omitempty"`
	URL         string `json:"url,omitempty"`
	Notes       string `json:"notes,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Prerelease  bool   `json:"prerelease"`
	Asset       string `json:"asset,omitempty"`
	AssetSize   int64  `json:"asset_size,omitempty"`
}

type updateInstallDTO struct {
	Running    bool   `json:"running"`
	Done       bool   `json:"done"`
	Stage      string `json:"stage,omitempty"`
	Message    string `json:"message,omitempty"`
	Written    int64  `json:"written,omitempty"`
	Total      int64  `json:"total,omitempty"`
	Percent    int    `json:"percent"`
	Error      string `json:"error,omitempty"`
	Previous   string `json:"previous,omitempty"`
	Installed  string `json:"installed,omitempty"`
	Path       string `json:"path,omitempty"`
	Restarting bool   `json:"restarting"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type updateStatusDTO struct {
	Repository      string               `json:"repository"`
	CurrentVersion  string               `json:"current_version"`
	CurrentCommit   string               `json:"current_commit,omitempty"`
	BuildDate       string               `json:"build_date,omitempty"`
	Platform        string               `json:"platform"`
	UpdateAvailable bool                 `json:"update_available"`
	Latest          *updateReleaseDTO    `json:"latest,omitempty"`
	Environment     updateEnvironmentDTO `json:"environment"`
	CheckedAt       string               `json:"checked_at,omitempty"`
	// Stale marks a release that is displayed but could not be refreshed from
	// GitHub; CheckError carries the reason.
	Stale      bool              `json:"stale,omitempty"`
	CheckError string            `json:"check_error,omitempty"`
	Install    *updateInstallDTO `json:"install,omitempty"`
}

// --- status ---------------------------------------------------------------

// Status reports the running build, the newest release, and whether this host
// can install it. It never fails: a release lookup that is unavailable is
// reported in CheckError so the panel can show a warning instead of an error
// screen, and the previously fetched release is reused when the API is
// rate-limited.
func (m *UpdateManager) Status(ctx context.Context) updateStatusDTO {
	dto := updateStatusDTO{
		Repository:     m.client.Repo(),
		CurrentVersion: m.version,
		CurrentCommit:  m.commit,
		BuildDate:      m.buildDate,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		Environment:    environmentDTO(m.env),
	}

	lookup := m.lookupRelease(ctx)
	if lookup.release != nil {
		dto.UpdateAvailable = selfupdate.IsNewer(lookup.release.Tag, m.version)
		if dto.UpdateAvailable {
			dto.Latest = releaseDTO(lookup.release)
		}
	}
	if !lookup.checkedAt.IsZero() {
		dto.CheckedAt = lookup.checkedAt.UTC().Format(time.RFC3339)
	}
	dto.CheckError = lookup.errMsg
	dto.Stale = lookup.stale

	dto.Install = m.installDTO()
	return dto
}

// releaseLookup is the outcome of one lookup, including whether the release it
// carries is fresh or the previously cached one.
type releaseLookup struct {
	release   *selfupdate.Release
	checkedAt time.Time
	errMsg    string
	stale     bool
}

// lookupRelease serves the release from a short-lived in-process cache, then
// from the client's longer 15-minute cache, and only then from the network.
func (m *UpdateManager) lookupRelease(ctx context.Context) releaseLookup {
	m.mu.Lock()
	if m.release != nil && time.Since(m.releaseAt) < m.statusTTL {
		out := releaseLookup{release: m.release, checkedAt: m.releaseAt, errMsg: m.lookErr, stale: m.lookStale}
		m.mu.Unlock()
		return out
	}
	m.mu.Unlock()

	// A caller waiting on the network while the dashboard polls every two
	// seconds would otherwise pile up; give the in-flight lookup a bounded
	// budget and let the client's stale cache absorb a failure.
	lookupCtx, cancel := context.WithTimeout(ctx, releaseLookupTimeout)
	defer cancel()

	rel, err := m.client.Latest(lookupCtx)
	now := time.Now()

	var errMsg string
	stale := false
	if err != nil {
		errMsg = err.Error()
		stale = errors.Is(err, selfupdate.ErrStaleRelease)
		if m.logger != nil {
			m.logger.Warn("release lookup failed", "err", err, "stale", stale)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if rel != nil {
		m.release = rel
		m.releaseAt = now
	}
	// Record the outcome against the snapshot that is actually being served, so
	// a stale cached release still reports why it could not be refreshed.
	m.lookErr = errMsg
	m.lookStale = stale
	if m.releaseAt.IsZero() {
		m.releaseAt = now
	}
	return releaseLookup{release: m.release, checkedAt: m.releaseAt, errMsg: errMsg, stale: stale}
}

func (m *UpdateManager) installDTO() *updateInstallDTO {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installDTOLocked()
}

func (m *UpdateManager) installDTOLocked() *updateInstallDTO {
	if !m.running && !m.done {
		return nil
	}
	dto := &updateInstallDTO{
		Running:    m.running,
		Done:       m.done,
		Stage:      string(m.progress.Stage),
		Message:    m.progress.Message,
		Written:    m.progress.Written,
		Total:      m.progress.Total,
		Error:      m.installEr,
		Restarting: m.restarted,
	}
	if dto.Message == "" {
		dto.Message = m.progress.Stage.Describe()
	}
	if dto.Total > 0 {
		dto.Percent = clampPercent(m.progress.Written, m.progress.Total)
	} else if m.done && m.installEr == "" {
		dto.Percent = 100
	}
	if !m.started.IsZero() {
		dto.StartedAt = m.started.UTC().Format(time.RFC3339)
	}
	if !m.finished.IsZero() {
		dto.FinishedAt = m.finished.UTC().Format(time.RFC3339)
	}
	if m.result != nil {
		dto.Previous = m.result.Previous
		dto.Installed = m.result.Installed
		dto.Path = m.result.Path
	}
	return dto
}

func clampPercent(written, total int64) int {
	if total <= 0 {
		return 0
	}
	if written < 0 {
		written = 0
	}
	p := int(written * 100 / total)
	if p > 100 {
		return 100
	}
	return p
}

func environmentDTO(env selfupdate.Environment) updateEnvironmentDTO {
	return updateEnvironmentDTO{
		Available:  env.Available(),
		Reason:     env.Reason,
		Container:  env.Container,
		Supervisor: string(env.Supervisor),
		Executable: env.Executable,
		Dir:        env.Dir,
		Writable:   env.Writable,
		Restart:    env.RestartBehavior(),
	}
}

func releaseDTO(rel *selfupdate.Release) *updateReleaseDTO {
	dto := &updateReleaseDTO{
		Tag:         rel.Tag,
		Name:        rel.Name,
		URL:         rel.HTMLURL,
		PublishedAt: rel.Published.UTC().Format(time.RFC3339),
		Prerelease:  rel.Prerelease,
		Notes:       truncateNotes(rel.Notes),
	}
	// Report the asset this platform would actually install, so the operator
	// sees the real filename instead of a generic "download" label.
	if asset, _, err := selfupdate.AssetName(rel.Tag, runtime.GOOS, runtime.GOARCH); err == nil {
		if match, ok := rel.Asset(asset); ok {
			dto.Asset = match.Name
			dto.AssetSize = match.Size
		} else {
			dto.Asset = asset
		}
	}
	return dto
}

// truncateNotes cuts release notes to the panel's budget on a rune boundary,
// because a GitHub release body is arbitrary UTF-8 and slicing bytes mid-rune
// would produce invalid JSON.
func truncateNotes(notes string) string {
	if len(notes) <= maxReleaseNotes {
		return notes
	}
	cut := maxReleaseNotes
	for cut > 0 && !utf8.RuneStart(notes[cut]) {
		cut--
	}
	return notes[:cut] + "\n\n…"
}

// --- apply ----------------------------------------------------------------

// Apply starts an install of the newest release and returns immediately. The
// download continues on a context detached from the request, and the caller
// follows its progress through Status.
func (m *UpdateManager) Apply(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return ErrUpdateInProgress
	}
	if !m.env.Available() {
		m.mu.Unlock()
		return fmt.Errorf("self-update is unavailable: %s", m.env.Reason)
	}
	rel := m.release
	if rel == nil {
		m.mu.Unlock()
		return errors.New("no release information is available yet; check for updates first")
	}
	if !selfupdate.IsNewer(rel.Tag, m.version) {
		m.mu.Unlock()
		return fmt.Errorf("version %s is already the newest release", m.version)
	}

	m.running = true
	m.done = false
	m.installEr = ""
	m.result = nil
	m.progress = selfupdate.Progress{Stage: selfupdate.StageResolving, Message: "starting"}
	m.started = time.Now()
	m.finished = time.Time{}
	m.restarted = false
	m.mu.Unlock()

	// The request that starts the install returns immediately, so its context
	// dies long before the download finishes. Detach from cancellation while
	// keeping the values (request id, logger) that make the logs greppable.
	installCtx := context.WithoutCancel(ctx)

	go func() {
		defer supervisor.Guard("server.selfupdate")()

		result, err := m.installer.Install(installCtx, selfupdate.InstallRequest{
			Release: rel,
			Env:     m.env,
			Current: m.version,
			Report:  m.reportProgress,
		})

		m.mu.Lock()
		m.running = false
		m.done = true
		m.finished = time.Now()
		if err != nil {
			m.installEr = err.Error()
		} else {
			m.result = &result
		}
		m.mu.Unlock()

		if err != nil {
			if m.logger != nil {
				m.logger.Error("self-update failed", "err", err, "release", rel.Tag)
			}
			return
		}
		if m.logger != nil {
			m.logger.Info("self-update installed",
				"version", result.Installed, "previous", result.Previous, "path", result.Path)
		}
		m.scheduleRestart()
	}()

	return nil
}

func (m *UpdateManager) reportProgress(p selfupdate.Progress) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.progress = p
}

// scheduleRestart lets the dashboard observe the completed install before the
// process is replaced.
func (m *UpdateManager) scheduleRestart() {
	delay := m.restartDelay
	m.mu.Lock()
	m.restarted = true
	m.mu.Unlock()

	time.AfterFunc(delay, func() {
		defer supervisor.Guard("server.selfupdate.restart")()
		if m.restart == nil {
			if m.logger != nil {
				m.logger.Warn("self-update installed but no restart hook is configured; the new binary starts on the next manual restart")
			}
			return
		}
		m.restart("self-update installed " + m.installedVersion())
	})
}

func (m *UpdateManager) installedVersion() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.result != nil {
		return m.result.Installed
	}
	return "unknown"
}
