package shim

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// CommandLineToolsDeveloperDir is the Command Line Tools' developer directory,
// and CommandLineToolsSDK the SDK inside it.
const (
	CommandLineToolsDeveloperDir = "/Library/Developer/CommandLineTools"
	CommandLineToolsSDK          = CommandLineToolsDeveloperDir + "/SDKs/MacOSX.sdk"
)

// XcodeDeveloperDir is Xcode.app's developer directory at its default
// location, and XcodeSDK the macOS SDK inside it. An Xcode elsewhere is
// selected with DEVELOPER_DIR.
const (
	XcodeDeveloperDir = "/Applications/Xcode.app/Contents/Developer"
	XcodeSDK          = XcodeDeveloperDir + "/" + xcodeSDKInDeveloperDir
)

const (
	xcodeSDKInDeveloperDir = "Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk"
	cltSDKInDeveloperDir   = "SDKs/MacOSX.sdk"
)

// SDKProbe is what resolving the darwin SDK reads from the host. It is a
// parameter so the resolution order is tested on any host. Resolution only
// reads the filesystem: it runs no subprocess, so a sandboxed consumer that
// grants the SDKs directory and nothing else gets a working SDK (kottera
// PLAN-2026-10 R1). xcrun cannot run under that read set.
type SDKProbe struct {
	Getenv func(string) string
	// Dir returns nil when the path is a directory, an error satisfying
	// absent (fs.ErrNotExist or ENOTDIR) when there is no directory there,
	// and any other error, typically a sandbox's EPERM, when something is
	// there that cannot be read.
	Dir func(string) error
}

// ResolveSDK finds the macOS SDK, the one host input the shim takes (kottera
// ADR-155 D3 item 3, D4, as amended by PLAN-2026-10 R1). In order:
//
//  1. SDKROOT, if set. A set SDKROOT that is not a readable directory is
//     refused, never fallen past.
//  2. DEVELOPER_DIR, if set: the SDK inside the developer directory it
//     selects (an Xcode or a Command Line Tools one), or a refusal. It never
//     falls past to another developer directory's SDK.
//  3. The Command Line Tools' SDK, then Xcode.app's.
//
// In step 3 the first SDK that is present decides: if it cannot be read the
// shim refuses rather than taking the next, so a sandbox's grants never change
// which SDK a host builds with. Each refusal names the repair for the host it
// is on; `xcode-select --install` is named only when the host has no SDK.
func ResolveSDK(role string, p SDKProbe) (string, error) {
	if sdk := p.Getenv("SDKROOT"); sdk != "" {
		err := p.Dir(sdk)
		if err == nil {
			return sdk, nil
		}
		r := &Refusal{
			Role: role,
			Why:  "SDKROOT is the first place the shim takes the SDK from, and it does not fall past a set SDKROOT to another SDK",
		}
		if !absent(err) {
			r.Missing = "the macOS SDK named by SDKROOT: " + sdk + " cannot be read: " + err.Error()
			r.Repair = "grant this build read access to " + sdk + " (in a kottera sandbox, the consumer's ReadPaths entry for the SDKs directory), or unset SDKROOT"
			return "", r
		}
		r.Missing = "the macOS SDK named by SDKROOT: " + sdk + " is not a directory"
		switch other, err := unrootedSDK(role, p); {
		case err == nil:
			r.Repair = "unset SDKROOT, and the shim takes " + other + ", or point SDKROOT at an installed SDK"
		case err == errNoSDK:
			r.Repair = "point SDKROOT at an installed SDK, or unset it; this host has no SDK in the places the shim looks, so unsetting it needs one installed with `xcode-select --install` (an operator action on this host)"
		default:
			r.Repair = "point SDKROOT at an installed SDK, or unset it"
		}
		return "", r
	}
	sdk, err := unrootedSDK(role, p)
	if err == errNoSDK {
		return "", &Refusal{
			Role:    role,
			Missing: "a macOS SDK: SDKROOT and DEVELOPER_DIR are unset, and neither " + CommandLineToolsSDK + " nor " + XcodeSDK + " exists",
			Why:     "zig does not ship the macOS frameworks' headers and link stubs; they come from the host's SDK, which Apple does not permit redistributing (kottera ADR-155 D2)",
			Repair:  "install the Command Line Tools with `xcode-select --install` (an operator action on this host); or, if Xcode is installed somewhere other than /Applications/Xcode.app, set DEVELOPER_DIR to its Contents/Developer",
		}
	}
	return sdk, err
}

// errNoSDK is unrootedSDK finding no SDK at all: DEVELOPER_DIR unset and
// nothing present at either default location.
var errNoSDK = errors.New("no macOS SDK")

// unrootedSDK is ResolveSDK's order after SDKROOT.
func unrootedSDK(role string, p SDKProbe) (string, error) {
	if dev := p.Getenv("DEVELOPER_DIR"); dev != "" {
		return developerDirSDK(role, dev, p)
	}
	for _, sdk := range []string{CommandLineToolsSDK, XcodeSDK} {
		err := p.Dir(sdk)
		if err == nil {
			return sdk, nil
		}
		if !absent(err) {
			return "", &Refusal{
				Role:    role,
				Missing: "a readable macOS SDK: " + sdk + " is present but cannot be read: " + err.Error(),
				Why:     "the shim takes the first SDK present in its search order (SDKROOT, DEVELOPER_DIR, " + CommandLineToolsSDK + ", " + XcodeSDK + "), and does not skip one it cannot read, so a sandbox's grants never change which SDK a build uses",
				Repair:  "grant this build read access to " + sdk + " (in a kottera sandbox, the consumer's ReadPaths entry for the SDKs directory), or set SDKROOT to a readable SDK",
			}
		}
	}
	return "", errNoSDK
}

// developerDirSDK is the SDK inside the developer directory DEVELOPER_DIR
// selects, accepting what xcode-select accepts: an Xcode's Contents/Developer,
// the Xcode.app itself, or a Command Line Tools directory.
func developerDirSDK(role, dev string, p SDKProbe) (string, error) {
	dev = strings.TrimRight(dev, "/")
	var candidates []string
	for _, d := range []string{dev, dev + "/Contents/Developer"} {
		candidates = append(candidates, d+"/"+xcodeSDKInDeveloperDir, d+"/"+cltSDKInDeveloperDir)
	}
	for _, sdk := range candidates {
		err := p.Dir(sdk)
		if err == nil {
			return sdk, nil
		}
		if !absent(err) {
			return "", &Refusal{
				Role:    role,
				Missing: "a readable macOS SDK in DEVELOPER_DIR: " + sdk + " is present but cannot be read: " + err.Error(),
				Why:     "DEVELOPER_DIR selects the developer directory the SDK comes from, and the shim never uses an SDK from another one",
				Repair:  "grant this build read access to " + sdk + " (in a kottera sandbox, the consumer's ReadPaths entry for the SDKs directory)",
			}
		}
	}
	return "", &Refusal{
		Role:    role,
		Missing: "a macOS SDK in the developer directory DEVELOPER_DIR selects: " + dev + " contains none of " + strings.Join(candidates, ", "),
		Why:     "DEVELOPER_DIR selects the developer directory the SDK comes from, and the shim never uses an SDK from another one",
		Repair:  "set DEVELOPER_DIR to an Xcode (its Contents/Developer) or a Command Line Tools directory that contains a macOS SDK, or unset it",
	}
}

// absent reports whether a Dir error means nothing is there, as opposed to
// something being there that cannot be read.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// statDir is SDKProbe.Dir on the real filesystem.
func statDir(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return &fs.PathError{Op: "stat", Path: p, Err: syscall.ENOTDIR}
	}
	return nil
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
