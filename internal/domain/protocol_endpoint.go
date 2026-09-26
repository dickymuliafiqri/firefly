package domain

// oauthManagedBaseURLs is the provider-managed inference endpoint of every
// protocol that authenticates with an OAuth token instead of an
// operator-supplied API key.
//
// An adapter forwards the provider's bearer token to this host, so the value is
// deliberately NOT operator configurable: the configuration builder replaces any
// base_url a file, a database row, or an API client supplies with the entry
// below (see config.Build and config.PinOAuthManagedEndpoints). Keeping the
// table here — a leaf package imported by both the config loader and the
// adapters — gives the pin, the adapter's own default, and the dashboard's
// read-only field one source of truth.
//
// The value is the adapter's base URL, not the full inference URL: the verb path
// (/chat/completions, /v1internal:*, …) is appended by the adapter.
var oauthManagedBaseURLs = map[Protocol]string{
	ProtocolAntigravity:   "https://daily-cloudcode-pa.googleapis.com",
	ProtocolCline:         "https://api.cline.bot/api/v1",
	ProtocolCodeBuddyCN:   "https://copilot.tencent.com/v2",
	ProtocolCodeBuddyIntl: "https://www.codebuddy.ai/v2",
}

// OAuthManagedBaseURL returns the provider-managed endpoint of an
// OAuth-authenticated protocol and reports whether the protocol has one. ok is
// false for protocols whose host the operator chooses (openai, anthropic,
// grok-cli, opencode, qoder).
func OAuthManagedBaseURL(p Protocol) (string, bool) {
	endpoint, ok := oauthManagedBaseURLs[p]
	return endpoint, ok
}