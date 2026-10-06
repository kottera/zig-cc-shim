package shim

import (
	"fmt"
	"regexp"
)

// The variables the `cc` definition's env block contributes (kottera ADR-155
// D2, D3). They are the only source of a target: the shim has no default.
const (
	EnvGlibc = "CC_TOOLCHAIN_TARGET_GLIBC"
	EnvMacOS = "CC_TOOLCHAIN_TARGET_MACOS"
)

var (
	glibcVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)
	macosVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
)

var zigArch = map[string]string{
	"amd64": "x86_64",
	"arm64": "aarch64",
}

// Target is the zig `-target` for a compile role on host, read from the
// environment. A fixed-target role targets its triple's platform; a host role
// targets the host. The version always comes from the environment.
func Target(role Role, host Platform, getenv func(string) string) (Platform, string, error) {
	p := host
	if role.Fixed != nil {
		p = *role.Fixed
	}
	arch, ok := zigArch[p.Arch]
	if !ok || (p.OS != "linux" && p.OS != "darwin") {
		return p, "", &Refusal{
			Role:    role.Name,
			Missing: fmt.Sprintf("a target for %s/%s", p.OS, p.Arch),
			Why:     "the shim targets linux and darwin on amd64 and arm64 only",
			Repair:  "run the shim on a supported host, or invoke a <triple>-cc role that names a supported target",
		}
	}
	switch p.OS {
	case "linux":
		v := getenv(EnvGlibc)
		if err := checkVersion(role, EnvGlibc, v, glibcVersion, "glibc floor", "2.31", "the host's own glibc"); err != nil {
			return p, "", err
		}
		return p, arch + "-linux-gnu." + v, nil
	default:
		v := getenv(EnvMacOS)
		if err := checkVersion(role, EnvMacOS, v, macosVersion, "macOS minimum", "13.0", "the build host's own macOS version"); err != nil {
			return p, "", err
		}
		return p, arch + "-macos." + v, nil
	}
}

func checkVersion(role Role, name, v string, shape *regexp.Regexp, what, seeded, fallback string) error {
	if v == "" {
		return &Refusal{
			Role:    role.Name,
			Missing: fmt.Sprintf("%s is not set, so there is no %s to target", name, what),
			Why:     fmt.Sprintf("the target comes only from %s; the shim never falls back to %s, because that is what makes two hosts build differently (kottera ADR-155 D2)", name, fallback),
			Repair:  fmt.Sprintf("declare the `cc` toolchain, whose definition sets %s (seeded value %s); outside tera, export %s=%s", name, seeded, name, seeded),
		}
	}
	if !shape.MatchString(v) {
		return &Refusal{
			Role:    role.Name,
			Missing: fmt.Sprintf("a usable %s: %s=%s is not a version", what, name, quote(v)),
			Why:     "the value becomes part of zig's -target, and only a dotted version number is accepted",
			Repair:  fmt.Sprintf("set %s to a version such as %s in the `cc` definition's env block", name, seeded),
		}
	}
	return nil
}
