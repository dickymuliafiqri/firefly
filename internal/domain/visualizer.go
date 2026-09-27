package domain

// Visualizer bounds for the private request visualizer.
const (
	// DefaultVisualizerRetention is how many traces the ring keeps.
	DefaultVisualizerRetention = 500
	// DefaultVisualizerKeepEvents is how many stage entries a single trace keeps
	// before the middle is coalesced.
	DefaultVisualizerKeepEvents = 24
	// MaxVisualizerRetention caps the ring so a misconfigured file cannot pin
	// unbounded memory: each trace is a few hundred bytes, so 4096 traces stays
	// well under a megabyte.
	MaxVisualizerRetention = 4096
	// MaxVisualizerKeepEvents caps per-trace stage history.
	MaxVisualizerKeepEvents = 256
)

// VisualizerConfig defines the in-memory request visualizer.
//
// The visualizer is a content-free picture of gateway traffic: routing
// decisions, stage timing, and stream activity counts. It never sees prompts,
// completions, or raw credentials, so it can be shown on the dashboard without
// leaking customer data. It is off by default; capture only runs while a
// dashboard subscriber is attached, or after an explicit operator toggle.
type VisualizerConfig struct {
	Enabled    bool
	Retention  int // traces kept in the ring (newest wins)
	KeepEvents int // stage entries retained per trace before coalescing
}

// DefaultVisualizerConfig returns the recorder bounds used when
// visualizer.json is absent.
func DefaultVisualizerConfig() VisualizerConfig {
	return VisualizerConfig{
		Enabled:    true,
		Retention:  DefaultVisualizerRetention,
		KeepEvents: DefaultVisualizerKeepEvents,
	}
}
