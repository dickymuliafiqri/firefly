package tunnel

import (
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		wantArgs []string
	}{
		{
			name: "quick tunnel",
			cfg: Config{
				Mode:     ModeQuick,
				LocalURL: "http://127.0.0.1:8080",
			},
			wantArgs: []string{"tunnel", "--url", "http://127.0.0.1:8080"},
		},
		{
			name: "named tunnel",
			cfg: Config{
				Mode:  ModeNamed,
				Token: "eyJhIjoi...",
			},
			wantArgs: []string{"tunnel", "run", "--token", "eyJhIjoi..."},
		},
		{
			name: "disabled",
			cfg: Config{
				Mode: ModeDisabled,
			},
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(tt.cfg)
			got := m.buildArgs()
			if len(got) != len(tt.wantArgs) {
				t.Fatalf("buildArgs() got %v, want %v", got, tt.wantArgs)
			}
			for i := range got {
				if got[i] != tt.wantArgs[i] {
					t.Errorf("buildArgs()[%d] = %q, want %q", i, got[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestURLRegex(t *testing.T) {
	t.Parallel()

	sampleLog := `
2026-09-26T08:00:00Z INF +--------------------------------------------------------------------------------------------+
2026-09-26T08:00:00Z INF |  Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):  |
2026-09-26T08:00:00Z INF |  https://happy-cat-1234.trycloudflare.com                                                  |
2026-09-26T08:00:00Z INF +--------------------------------------------------------------------------------------------+
`
	match := urlRegex.FindString(sampleLog)
	want := "https://happy-cat-1234.trycloudflare.com"
	if match != want {
		t.Fatalf("urlRegex failed: got %q, want %q", match, want)
	}
}

func TestStreamReader_ExtractsURL(t *testing.T) {
	t.Parallel()

	m := NewManager(Config{
		Mode:     ModeQuick,
		LocalURL: "http://127.0.0.1:8080",
	})

	input := strings.NewReader(`
INF Starting tunnel...
INF |  https://quiet-fox-9876.trycloudflare.com  |
INF Registered tunnel connection
`)

	m.streamReader(input)

	if got := m.PublicURL(); got != "https://quiet-fox-9876.trycloudflare.com" {
		t.Fatalf("PublicURL() got %q, want https://quiet-fox-9876.trycloudflare.com", got)
	}
}
