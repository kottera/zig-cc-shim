package shim

import (
	"fmt"
	"strings"
)

// DescribeFlag, as the only argument, makes the shim say what it is instead
// of running zig (kottera ADR-155 D3 item 6).
const DescribeFlag = "--tera-describe"

// DescribeFormat is the version of the --tera-describe format. It changes
// only when a key is removed or a value's meaning changes; adding a key does
// not change it.
const DescribeFormat = "1"

// Description is what --tera-describe reports.
type Description struct {
	ShimVersion  string
	ShimRevision string
	Role         string
	Zig          string
	ZigVersion   string
	// Target is the zig -target, or empty for ar and ranlib.
	Target string
	// SDK and SDKVersion are set for a darwin target only.
	SDK        string
	SDKVersion string
	// HostGlibc is set on a Linux host only, "unknown" when it cannot be read.
	HostGlibc string
}

// String renders the stable format the README documents: one `key: value`
// per line, in this order, with optional lines omitted.
func (d Description) String() string {
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s: %s\n", k, v) }
	line("zig-cc-shim-describe", DescribeFormat)
	line("shim", d.ShimVersion)
	line("shim-revision", d.ShimRevision)
	line("role", d.Role)
	line("zig", d.Zig)
	line("zig-version", d.ZigVersion)
	if d.Target == "" {
		line("target", "none")
	} else {
		line("target", d.Target)
	}
	if d.SDK != "" {
		line("sdk", d.SDK)
		line("sdk-version", d.SDKVersion)
	}
	if d.HostGlibc != "" {
		line("host-glibc", d.HostGlibc)
	}
	return b.String()
}
