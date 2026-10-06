// Package shim is the argument rewriting and environment checking behind
// zig-cc-shim: one static program that answers to the names a C toolchain is
// called by and replaces itself with `zig cc`, `zig c++`, `zig ar` or
// `zig ranlib` (kottera ADR-155 D3).
//
// Everything that depends on the platform takes the platform as a parameter,
// so the rewriting for darwin and for Linux is tested on any host and with no
// zig present.
package shim

import (
	"sort"
	"strings"
)

// Driver is the zig subcommand a role replaces itself with.
type Driver string

const (
	DriverCC     Driver = "cc"
	DriverCXX    Driver = "c++"
	DriverAR     Driver = "ar"
	DriverRanlib Driver = "ranlib"
)

// Platform is an operating system and architecture, spelt as GOOS and GOARCH.
type Platform struct {
	OS   string
	Arch string
}

// Role is what the shim was invoked as.
type Role struct {
	// Name is the basename of argv[0].
	Name   string
	Driver Driver
	// Fixed is the platform a `<triple>-cc` or `<triple>-c++` role always
	// targets. It is nil for the host roles, which target the host, and for
	// `ar` and `ranlib`, which have no target.
	Fixed *Platform
}

// Compiles reports whether the role drives the compiler, and so needs a
// target.
func (r Role) Compiles() bool { return r.Driver == DriverCC || r.Driver == DriverCXX }

// Triples are the Rust target triples with a fixed-target role each, so that
// Cargo's per-target linker variables can each name the right one.
var Triples = map[string]Platform{
	"x86_64-unknown-linux-gnu":  {OS: "linux", Arch: "amd64"},
	"aarch64-unknown-linux-gnu": {OS: "linux", Arch: "arm64"},
	"aarch64-apple-darwin":      {OS: "darwin", Arch: "arm64"},
	"x86_64-apple-darwin":       {OS: "darwin", Arch: "amd64"},
}

var hostRoles = map[string]Driver{
	"cc":      DriverCC,
	"gcc":     DriverCC,
	"clang":   DriverCC,
	"c++":     DriverCXX,
	"g++":     DriverCXX,
	"clang++": DriverCXX,
	"ar":      DriverAR,
	"ranlib":  DriverRanlib,
}

// RoleNames is every name the shim answers to, sorted. The staging command
// creates one entry in bin/ for each.
func RoleNames() []string {
	var names []string
	for n := range hostRoles {
		names = append(names, n)
	}
	for t := range Triples {
		names = append(names, t+"-cc", t+"-c++")
	}
	sort.Strings(names)
	return names
}

// ParseRole maps the basename of argv[0] to a role, or refuses it, naming
// every role.
func ParseRole(name string) (Role, error) {
	if d, ok := hostRoles[name]; ok {
		return Role{Name: name, Driver: d}, nil
	}
	for t, p := range Triples {
		switch name {
		case t + "-cc":
			p := p
			return Role{Name: name, Driver: DriverCC, Fixed: &p}, nil
		case t + "-c++":
			p := p
			return Role{Name: name, Driver: DriverCXX, Fixed: &p}, nil
		}
	}
	return Role{}, &Refusal{
		Role:    name,
		Missing: "zig-cc-shim does not answer to the name " + quote(name),
		Why:     "the shim chooses what to run from the name it was invoked as, and this name is none of its roles: " + strings.Join(RoleNames(), ", "),
		Repair:  "invoke it through one of the role entries in the store's bin/ (for example bin/cc), not under another name",
	}
}

func quote(s string) string { return "\"" + s + "\"" }
