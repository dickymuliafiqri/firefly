package server

import "testing"

func TestNormalizeAutoTLSConfig(t *testing.T) {
	tests := []struct {
		name    string
		input   AutoTLSConfig
		want    AutoTLSConfig
		wantErr bool
	}{
		{
			name:  "disabled clears stale fields",
			input: AutoTLSConfig{Domain: "stale.example.com", Email: "ops@example.com"},
			want:  AutoTLSConfig{},
		},
		{
			name:  "canonicalizes hostname",
			input: AutoTLSConfig{Enabled: true, Domain: " AI.Example.COM. ", Email: "ops@example.com"},
			want:  AutoTLSConfig{Enabled: true, Domain: "ai.example.com", Email: "ops@example.com"},
		},
		{
			name:    "rejects IP address",
			input:   AutoTLSConfig{Enabled: true, Domain: "203.0.113.12", Email: "ops@example.com"},
			wantErr: true,
		},
		{
			name:    "rejects URL",
			input:   AutoTLSConfig{Enabled: true, Domain: "https://ai.example.com", Email: "ops@example.com"},
			wantErr: true,
		},
		{
			name:    "rejects wildcard",
			input:   AutoTLSConfig{Enabled: true, Domain: "*.example.com", Email: "ops@example.com"},
			wantErr: true,
		},
		{
			name:    "requires email",
			input:   AutoTLSConfig{Enabled: true, Domain: "ai.example.com"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAutoTLSConfig(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeAutoTLSConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("NormalizeAutoTLSConfig() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRequestHostMatches(t *testing.T) {
	if !requestHostMatches("AI.EXAMPLE.COM:80", "ai.example.com") {
		t.Fatal("configured host with port should match")
	}
	if requestHostMatches("attacker.example", "ai.example.com") {
		t.Fatal("unconfigured host must not match")
	}
}
