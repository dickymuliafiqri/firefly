package domain

// oauthManagedBaseURLs is the provider-managed inference endpoint of every
// protocol whose host is fixed by the provider rather than chosen by the
// operator. Two families qualify today:
//
//   - protocols that authenticate with a provider OAuth token (antigravity,
//     cline, codebuddy-cn/-intl, grok-cli), and
//   - provider-hosted gateways whose request shape only works against the
//     vendor's own host (opencode/opencode-go, qoder).
//
// An adapter forwards the provider's credential to this host, so the value is
// deliberately NOT operator configurable: the configuration builder replaces any
// base_url a file, a database row, or an API client supplies with the entry
// below (see config.Build and config.PinOAuthManagedEndpoints). Keeping the
// table here — a leaf package imported by both the config loader and the
// adapters — gives the pin, the adapter's own default, and the dashboard's
// read-only field one source of truth. Only `openai` and `anthropic` keep an
// operator-chosen host.
//
// The value is the adapter's base URL, not the full inference URL: the verb path
// (/chat/completions, /responses, /v1internal:*, …) is appended by the adapter,
// and an adapter may refine the pinned host further at request time from the
// credential itself (see the qoder and opencode rows).
var oauthManagedBaseURLs = map[Protocol]string{
	ProtocolAntigravity:   "https://daily-cloudcode-pa.googleapis.com",
	ProtocolCline:         "https://api.cline.bot/api/v1",
	ProtocolCodeBuddyCN:   "https://copilot.tencent.com/v2",
	ProtocolCodeBuddyIntl: "https://www.codebuddy.ai/v2",
	// grok-cli authenticates with an xAI OAuth bearer token minted at auth.x.ai
	// for cli-chat-proxy.grok.com. The adapter appends /responses, so the pin is
	// the base, never the full inference URL.
	ProtocolGrokCLI: "https://cli-chat-proxy.grok.com/v1",
	// OpenCode Zen has one canonical entry point; the adapter swaps it to
	// /zen/go/v1 for a subscription credential and keeps /zen/v1 for the keyless
	// Free tier (buildEndpointURL), so the pinned value is the free host.
	ProtocolOpenCode: "https://opencode.ai/zen/v1",
	// The Go subscription tier, for callers that name it explicitly.
	ProtocolOpenCodeGo: "https://opencode.ai/zen/go/v1",
	// Qoder pins the device-token host (api3); qoder.ResolveBaseURL reroutes job
	// tokens (jt-) to api2 at request time, because api3 rejects them with 403.
	ProtocolQoder: "https://api3.qoder.sh",
}

// OAuthManagedBaseURL returns the provider-managed endpoint of a protocol whose
// host the operator may not choose, and reports whether the protocol has one. ok
// is false only for openai and anthropic, the two protocols that point at
// arbitrary (self-hosted, Azure, vLLM, …) endpoints.
func OAuthManagedBaseURL(p Protocol) (string, bool) {
	endpoint, ok := oauthManagedBaseURLs[p]
	return endpoint, ok
}
