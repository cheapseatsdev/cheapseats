package answer

import (
	"slices"
	"strings"
	"testing"
)

// CHEAPSEATS_CLI_ENV passes the variables it names to the CLI, except those that would load code
// into it as it starts: the CLI is an agent, and LD_PRELOAD or NODE_OPTIONS runs code before any
// of its own safeguards.
func TestCLIEnvNeverPassesStartupCode(t *testing.T) {
	t.Setenv("CHEAPSEATS_CLI_ENV", "KEPT,LD_PRELOAD,DYLD_INSERT_LIBRARIES,NODE_OPTIONS,PYTHONSTARTUP,BASH_ENV,RUBYOPT")
	for _, n := range []string{"KEPT", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS", "PYTHONSTARTUP", "BASH_ENV", "RUBYOPT"} {
		t.Setenv(n, "x")
	}
	got := cliVars("")
	if !slices.Contains(got, "KEPT=x") {
		t.Errorf("KEPT not passed: %v", got)
	}
	for _, v := range got {
		for _, bad := range []string{"LD_PRELOAD=", "DYLD_INSERT_LIBRARIES=", "NODE_OPTIONS=", "PYTHONSTARTUP=", "BASH_ENV=", "RUBYOPT="} {
			if strings.HasPrefix(v, bad) {
				t.Errorf("passed %s", v)
			}
		}
	}
}
