package shim

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// glibcFiles are the host files the Linux floor check reads, in order, per
// GOARCH. The first is the dynamic loader at the path the architecture's ABI
// fixes, so it is the same on every glibc distro; the rest are libc.so.6 in
// the places distros put it. They are read, never passed to the compiler: the
// shim adds no host search path (kottera ADR-155 D4).
var glibcFiles = map[string][]string{
	"amd64": {
		"/lib64/ld-linux-x86-64.so.2",
		"/lib/x86_64-linux-gnu/libc.so.6",
		"/lib64/libc.so.6",
		"/usr/lib64/libc.so.6",
		"/usr/lib/x86_64-linux-gnu/libc.so.6",
	},
	"arm64": {
		"/lib/ld-linux-aarch64.so.1",
		"/lib/aarch64-linux-gnu/libc.so.6",
		"/lib64/libc.so.6",
		"/usr/lib64/libc.so.6",
		"/usr/lib/aarch64-linux-gnu/libc.so.6",
	},
}

// glibc's loader and libc.so.6 both carry the banner they print for
// --version: "... stable release version 2.39." or "... release version 2.39".
var glibcBanner = regexp.MustCompile(`release version ([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)

// HostGlibc finds the host's glibc version by reading the banner out of the
// loader or libc.so.6. It returns the version and the file it came from, or
// the files it looked at.
func HostGlibc(arch string, readFile func(string) ([]byte, error)) (version, source string, looked []string) {
	for _, f := range glibcFiles[arch] {
		looked = append(looked, f)
		b, err := readFile(f)
		if err != nil {
			continue
		}
		if m := glibcBanner.FindSubmatch(b); m != nil {
			return string(m[1]), f, looked
		}
	}
	return "", "", looked
}

// CheckGlibcFloor refuses when the host's glibc is below the floor the target
// names, because a binary built for the floor is then one this host's own
// test runs cannot load (kottera ADR-155 D2, D3 item 5).
func CheckGlibcFloor(role, floor, hostVersion, source string, looked []string) error {
	if hostVersion == "" {
		return &Refusal{
			Role:    role,
			Missing: "this host's glibc version: none of " + strings.Join(looked, ", ") + " carries a glibc release banner",
			Why:     fmt.Sprintf("the shim checks that this host's glibc is at least the floor %s=%s before building for it, and it cannot tell what this host has (a musl or non-glibc host cannot run what it would build)", EnvGlibc, floor),
			Repair:  "run this work on a glibc Linux host (an operator action: route it to another runner)",
		}
	}
	if CompareVersions(hostVersion, floor) < 0 {
		return &Refusal{
			Role:    role,
			Missing: fmt.Sprintf("a glibc at or above the floor: this host's glibc is %s (read from %s), below the floor %s=%s", hostVersion, source, EnvGlibc, floor),
			Why:     "binaries built here target the floor, and tests built here would run against this host's older glibc and fail to load (kottera ADR-155 D2)",
			Repair:  fmt.Sprintf("run this work on a host with glibc %s or newer, or lower the floor in the `cc` definition's env block; both are operator actions", floor),
		}
	}
	return nil
}

// CompareVersions compares dotted decimal versions numerically: -1, 0 or 1.
// A missing component counts as zero.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}
