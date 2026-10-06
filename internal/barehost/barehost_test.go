package barehost

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestDeclaredAcceptsOnlyTheDocumentedSpellings(t *testing.T) {
	for _, tc := range []struct {
		value string
		bare  bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {" yes ", true}, {"on", true},
		{"", false}, {"0", false}, {"false", false}, {"No", false}, {"off", false},
	} {
		bare, err := Declared(tc.value)
		if err != nil || bare != tc.bare {
			t.Errorf("Declared(%q) = %v, %v; want %v, nil", tc.value, bare, err, tc.bare)
		}
	}
	for _, value := range []string{"2", "y", "bare", "ture"} {
		if _, err := Declared(value); err == nil || !strings.Contains(err.Error(), EnvVar) {
			t.Errorf("Declared(%q): want an error naming %s, got %v", value, EnvVar, err)
		}
	}
}

// recorder is a testing.TB that records how Missing stopped the test. Its
// Fatalf and Skipf end the goroutine as the real ones do, so Missing returning
// at all is itself a finding.
type recorder struct {
	testing.TB
	how, msg string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.how, r.msg = "fail", fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Skipf(format string, args ...any) {
	r.how, r.msg = "skip", fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestMissingFailsOnADeclaredHostAndSkipsElsewhere(t *testing.T) {
	const cause = "THE_VARIABLE is not set; fix: set it"
	ptr := func(s string) *string { return &s }
	for _, tc := range []struct {
		name  string
		value *string
		how   string
		names string
	}{
		{name: "unset", how: "skip", names: EnvVar + "=1"},
		{name: "falsy", value: ptr("0"), how: "skip", names: EnvVar + `="0"`},
		{name: "truthy", value: ptr("1"), how: "fail", names: "ADR-151 D3"},
		{name: "misspelt", value: ptr("ture"), how: "fail", names: "not a spelling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvVar, "")
			if tc.value == nil {
				os.Unsetenv(EnvVar)
			} else {
				os.Setenv(EnvVar, *tc.value)
			}
			r := &recorder{TB: t}
			done := make(chan struct{})
			go func() {
				defer close(done)
				Missing(r, "%s", cause)
				r.how = "returned"
			}()
			<-done
			if r.how != tc.how || !strings.Contains(r.msg, cause) || !strings.Contains(r.msg, tc.names) {
				t.Errorf("Missing: %s with %q; want %s naming %q and %q", r.how, r.msg, tc.how, cause, tc.names)
			}
		})
	}
}
