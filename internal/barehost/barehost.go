// Package barehost is kottera ADR-151 D3's rule for tests, as kottera's own
// internal/testutil/barehost states it: a test that needs a bare-host
// prerequisite fails rather than skips when [EnvVar] declares the host bare,
// and skips with its cause otherwise. A host that claims to be bare therefore
// cannot skip such a test silently, and CI's skips stay visible as skips.
//
// kottera's package is internal to a private module, so this is a copy of its
// rule rather than an import. The variable and its spellings are the same, so
// one declaration covers both repositories' tests on a host.
package barehost

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// EnvVar declares the host bare.
const EnvVar = "KOTTERA_TEST_BARE_HOST"

// Declared reports what value says about the host: true for a truthy
// spelling (1, true, yes, on), false for unset or a falsy one (0, false, no,
// off), and an error for anything else. A misspelt declaration is refused
// rather than read as "not bare", because reading it that way would be the
// silent skip the rule exists to prevent.
func Declared(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "", "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s=%q is not a spelling this rule accepts: set 1, true, yes or on to declare the "+
		"host bare, or 0, false, no, off or nothing to leave it undeclared", EnvVar, value)
}

// Missing reports that the calling test lacks a bare-host prerequisite. The
// cause is format and args, worded as the skip would be, with its fix. On a
// host that declares itself bare it fails the test with that cause; on any
// other host it skips it with the same cause. Either way the test stops.
//
// It never returns.
func Missing(tb testing.TB, format string, args ...any) {
	tb.Helper()
	cause := fmt.Sprintf(format, args...)
	value, set := os.LookupEnv(EnvVar)
	bare, err := Declared(value)
	switch {
	case err != nil:
		tb.Fatalf("%s\n\n%v", cause, err)
	case bare:
		tb.Fatalf("%s\n\nThis is a bare-host prerequisite and this host declares itself bare (%s=%q), so its "+
			"absence is a failure, not a skip (kottera ADR-151 D3). Fix the host.", cause, EnvVar, value)
	case set:
		tb.Skipf("%s\n\nA bare-host prerequisite; %s=%q does not declare this host bare.", cause, EnvVar, value)
	default:
		tb.Skipf("%s\n\nA bare-host prerequisite; set %s=1 on a host that claims to be bare to make its "+
			"absence a failure.", cause, EnvVar)
	}
}
