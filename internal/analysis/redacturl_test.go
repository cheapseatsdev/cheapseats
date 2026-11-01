package analysis

import (
	"strings"
	"testing"
)

// The verification error names the first target it tried; a target's query or user info can
// carry a token, so it is named without them.
func TestUnreadableNamesTheTargetWithoutCredentials(t *testing.T) {
	err := unreadable([]artifact{{URL: "https://u:secretpw@docs.example/p?token=SENTINELtok#x", Error: "current page unavailable"}})
	if s := err.Error(); strings.Contains(s, "SENTINELtok") || strings.Contains(s, "secretpw") || !strings.Contains(s, "https://docs.example/p: current page unavailable") {
		t.Errorf("%q", s)
	}
}
