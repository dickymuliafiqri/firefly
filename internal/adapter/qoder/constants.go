// Package qoder provides an upstream adapter for the Qoder IDE inference API.
//
// Qoder speaks an OpenAI-shaped chat protocol wrapped in a bespoke transport:
//   - request bodies are WAF-obfuscated (see encoding.go) and sent with &Encode=1
//   - every request is COSY-signed (RSA+AES+MD5 + ~17 Cosy-* headers, see cosy.go)
//   - the SSE stream is an OpenAI chunk wrapped in a {statusCodeValue, body} envelope
//
// Source of truth: 9router open-sse shared/qoder + executors/qoder.js, ported to Go.
package qoder

// Endpoint bases.
const (
	// QoderOpenAPIBase serves device flow + userinfo + quota usage.
	QoderOpenAPIBase = "https://openapi.qoder.sh"
	// QoderCenterBase serves token refresh (best-effort).
	QoderCenterBase = "https://center.qoder.sh"
	// QoderChatBase is the inference host for device tokens (dt-).
	QoderChatBase = "https://api3.qoder.sh"
	// QoderChatBaseAlt is the inference host for job tokens (jt-); api3 rejects jt- with 403.
	QoderChatBaseAlt = "https://api2.qoder.sh"
)

// Device / auth flow endpoints.
const (
	QoderUserinfoURL         = QoderOpenAPIBase + "/api/v1/userinfo"
	QoderJobTokenExchangeURL = QoderOpenAPIBase + "/api/v1/jobToken/exchange"
)

// Inference endpoints (under /algo, all COSY-signed).
const (
	// QoderChatSigPath is the signature path (the "/algo" prefix is stripped when signing).
	QoderChatSigPath = "/api/v2/service/pro/sse/agent_chat_generation"
	// QoderChatQuery is appended to the chat URL.
	QoderChatQuery = "?FetchKeys=llm_model_result&AgentId=agent_common"
	// QoderModelListPath is the model catalog path.
	QoderModelListPath = "/algo/api/v2/model/list"
)

// COSY header constants. These are not arbitrary — the upstream signature
// validation matches them against the values used at signing time.
const (
	QoderIDEVersion   = "1.0.0"
	QoderClientType   = "5"
	QoderDataPolicy   = "disagree"
	QoderLoginVersion = "v2"
	QoderMachineOS    = "x86_64_windows"
	QoderMachineType  = "5"
)

// Token prefixes.
const (
	QoderPATPrefix = "pt-" // personal access token (must be exchanged for a job token)
	QoderJobPrefix = "jt-" // short-lived job token (served by api2)
	QoderDevPrefix = "dt-" // device token (served by api3)
)

// QoderRSAPublicKey is the RSA public key used for COSY encryption (extracted from
// Qoder IDE v0.9). Matches the CLIProxyAPIPlus branch and live qodercli traffic.
const QoderRSAPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// qoderInferenceBase returns the correct inference host for a token. Job tokens
// (jt-) must hit api2; device tokens (dt-) stay on api3. PATs are exchanged for
// job tokens before this is consulted.
func qoderInferenceBase(token string) string {
	if len(token) >= 3 && token[:3] == QoderJobPrefix {
		return QoderChatBaseAlt
	}
	return QoderChatBase
}
