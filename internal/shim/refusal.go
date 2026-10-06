package shim

import "strings"

// Refusal is the shim declining to run, in the shape of kottera ADR-109 D7: what
// is missing, why that stops the shim, and the repair. A refusal is never
// transient, so retrying the same command does not help.
type Refusal struct {
	// Role is the name the shim was invoked as.
	Role    string
	Missing string
	Why     string
	Repair  string
}

func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("zig-cc-shim")
	if r.Role != "" {
		b.WriteString(" (" + r.Role + ")")
	}
	b.WriteString(": refused: " + r.Missing + "\n")
	b.WriteString("  why: " + r.Why + "\n")
	b.WriteString("  repair: " + r.Repair + "\n")
	b.WriteString("  This is not transient: retrying the same command will refuse again until the repair is made.\n")
	return b.String()
}
