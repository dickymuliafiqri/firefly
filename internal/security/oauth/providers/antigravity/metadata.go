package antigravity

import (
	"runtime"
)

// Antigravity client metadata constants matching Google IDE Desktop binary.
const (
	IDETypeAntigravity = 9
	PluginTypeGemini   = 2

	PlatformUnspecified  = 0
	PlatformDarwinAMD64  = 1
	PlatformDarwinARM64  = 2
	PlatformLinuxAMD64   = 3
	PlatformLinuxARM64   = 4
	PlatformWindowsAMD64 = 5

	// AntigravityIDEVersion represents the official client release version.
	AntigravityIDEVersion = "2.11.0"

	// AntigravityIDEUserAgent is the canonical user-agent sent by official IDE desktop clients.
	AntigravityIDEUserAgent = "antigravity/ide/2.11.0 darwin/arm64"
)

// ClientMetadataDTO matches the JSON payload expected by Google Cloud Code PA endpoints.
type ClientMetadataDTO struct {
	IDEType    int `json:"ideType"`
	Platform   int `json:"platform"`
	PluginType int `json:"pluginType"`
}

// DetectPlatform returns the numeric enum matching the current OS architecture.
func DetectPlatform() int {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return PlatformDarwinARM64
		}
		return PlatformDarwinAMD64
	case "linux":
		if runtime.GOARCH == "arm64" {
			return PlatformLinuxARM64
		}
		return PlatformLinuxAMD64
	case "windows":
		return PlatformWindowsAMD64
	default:
		return PlatformUnspecified
	}
}

// DefaultClientMetadata builds the standard metadata payload for loadCodeAssist/onboardUser.
func DefaultClientMetadata() ClientMetadataDTO {
	return ClientMetadataDTO{
		IDEType:    IDETypeAntigravity,
		Platform:   DetectPlatform(),
		PluginType: PluginTypeGemini,
	}
}
