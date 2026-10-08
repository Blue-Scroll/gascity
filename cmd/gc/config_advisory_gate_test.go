package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// alwaysFreshAdvisory is one of the eight lines the town's own city.toml
// produced before every gc command. It passes shouldEmitLoadCityConfigWarning,
// so it is the right warning to prove the gate with.
const alwaysFreshAdvisory = `named_session "gastown.mayor": mode "always" with wake_mode "fresh" on template ` +
	`"gastown.mayor" starts a fresh provider session after every drain; use only for a deliberate restart-per-cycle actor`

// TestEmitLoadCityConfigWarningsSilentByDefault is the heart of vn-ha102kr.
// Eight identical advisory lines on every command trained every gc-reading
// script in the town to write `2>/dev/null`, which then swallowed gc's real
// errors. With no operator opt-in, a one-shot command writes nothing.
func TestEmitLoadCityConfigWarningsSilentByDefault(t *testing.T) {
	t.Setenv(configAdvisoryEnv, "")
	var stderr bytes.Buffer
	emitLoadCityConfigWarnings(&stderr, &config.Provenance{
		Warnings: []string{alwaysFreshAdvisory},
	})
	if got := stderr.String(); got != "" {
		t.Fatalf("advisories must be off by default, got %q", got)
	}
}

// TestEmitLoadCityConfigWarningsOptIn proves the advisory is not lost: an
// operator who sets the env var still gets it, unchanged.
func TestEmitLoadCityConfigWarningsOptIn(t *testing.T) {
	t.Setenv(configAdvisoryEnv, "1")
	var stderr bytes.Buffer
	emitLoadCityConfigWarnings(&stderr, &config.Provenance{
		Warnings: []string{alwaysFreshAdvisory},
	})
	if !strings.Contains(stderr.String(), alwaysFreshAdvisory) {
		t.Fatalf("%s=1 must print the advisory, got %q", configAdvisoryEnv, stderr.String())
	}
}

func TestConfigAdvisoriesEnabled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset stays quiet", value: "", want: false},
		{name: "blanks stay quiet", value: "   ", want: false},
		{name: "zero stays quiet", value: "0", want: false},
		{name: "false stays quiet", value: "false", want: false},
		{name: "garbage stays quiet", value: "yes please", want: false},
		{name: "one opts in", value: "1", want: true},
		{name: "true opts in", value: "true", want: true},
		{name: "padded true opts in", value: " true ", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(configAdvisoryEnv, tc.value)
			if got := configAdvisoriesEnabled(); got != tc.want {
				t.Fatalf("configAdvisoriesEnabled() with %s=%q = %v, want %v",
					configAdvisoryEnv, tc.value, got, tc.want)
			}
		})
	}
}
