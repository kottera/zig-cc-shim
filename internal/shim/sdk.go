package shim

import (
	"encoding/json"
	"strings"
)

// CommandLineToolsSDK is the last place the darwin SDK is looked for.
const CommandLineToolsSDK = "/Library/Developer/CommandLineTools/SDKs/MacOSX.sdk"

// SDKProbe is what resolving the darwin SDK reads from the host. It is a
// parameter so the resolution order is tested on any host.
type SDKProbe struct {
	Getenv func(string) string
	IsDir  func(string) bool
	// Xcrun runs `/usr/bin/xcrun --sdk macosx --show-sdk-path` and returns its
	// trimmed output.
	Xcrun func() (string, error)
}

// ResolveSDK finds the macOS SDK, the one host input the shim takes (kottera
// ADR-155 D3 item 3, D4): SDKROOT if set, then xcrun, then the Command Line
// Tools path. With none, it refuses, naming `xcode-select --install`.
func ResolveSDK(role string, p SDKProbe) (string, error) {
	if sdk := p.Getenv("SDKROOT"); sdk != "" {
		if !p.IsDir(sdk) {
			return "", &Refusal{
				Role:    role,
				Missing: "the macOS SDK named by SDKROOT: " + sdk + " is not a directory",
				Why:     "SDKROOT is the first place the shim takes the SDK from, and it does not fall past a set SDKROOT to another SDK",
				Repair:  "point SDKROOT at an installed SDK (`/usr/bin/xcrun --sdk macosx --show-sdk-path` prints one), or unset it",
			}
		}
		return sdk, nil
	}
	if p.Xcrun != nil {
		if sdk, err := p.Xcrun(); err == nil && sdk != "" && p.IsDir(sdk) {
			return sdk, nil
		}
	}
	if p.IsDir(CommandLineToolsSDK) {
		return CommandLineToolsSDK, nil
	}
	return "", &Refusal{
		Role:    role,
		Missing: "a macOS SDK: SDKROOT is unset, `/usr/bin/xcrun --sdk macosx --show-sdk-path` found none, and " + CommandLineToolsSDK + " does not exist",
		Why:     "zig does not ship the macOS frameworks' headers and link stubs; they come from the host's SDK, which Apple does not permit redistributing (kottera ADR-155 D2)",
		Repair:  "install the Command Line Tools with `xcode-select --install` (an operator action on this host)",
	}
}

// DarwinFlags is the flag set measured in kottera ADR-155 D3 item 3, verbatim.
func DarwinFlags(sdk string) []string {
	return []string{
		"--sysroot", sdk,
		"-idirafter", sdk + "/usr/include",
		"-F/System/Library/Frameworks",
		"-L/usr/lib",
	}
}

// SDKVersion reads the SDK's version from its SDKSettings.json, or returns
// "unknown".
func SDKVersion(sdk string, readFile func(string) ([]byte, error)) string {
	b, err := readFile(sdk + "/SDKSettings.json")
	if err != nil {
		return "unknown"
	}
	var s struct{ Version string }
	if json.Unmarshal(b, &s) != nil || strings.TrimSpace(s.Version) == "" {
		return "unknown"
	}
	return strings.TrimSpace(s.Version)
}
