package app

import (
	"context"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

func TestPassesRunInOrderAroundTheFixList(t *testing.T) {
	// The shipped passes are registered from their files' init.
	fold, ok := PassByCommand("fold")
	if !ok || fold.Name != "fold-names" || fold.Provider != config.TypeSafeProvider {
		t.Fatalf("fold: %+v %v", fold, ok)
	}
	if _, ok := PassByCommand("coverage"); !ok {
		t.Fatal("coverage")
	}
	var ran []string
	mark := func(name string) func(*App, context.Context, string) (int, error) {
		return func(*App, context.Context, string) (int, error) { ran = append(ran, name); return 1, nil }
	}
	RegisterPass(Pass{Name: "t-late", Order: 150, Provider: config.ExaProvider, On: func(*config.Config) bool { return true }, Run: mark("late")})
	RegisterPass(Pass{Name: "t-early", Order: 5, Provider: config.ExaProvider, On: func(*config.Config) bool { return true }, Run: mark("early")})
	RegisterPass(Pass{Name: "t-off", Order: 6, Provider: config.ExaProvider, On: func(*config.Config) bool { return false }, Run: mark("off")})
	RegisterPass(Pass{Name: "t-nokey", Order: 7, Provider: config.TypeSafeProvider, On: func(*config.Config) bool { return true }, Run: mark("nokey")})
	t.Setenv("EXA_API_KEY", "k")
	t.Setenv(config.ExaBaseURLVar, "")
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(config.TypeSafeBaseURLVar, "")
	a := &App{Cfg: &config.Config{}}
	a.runPasses(context.Background(), "r", true)
	ran = append(ran, "fixes")
	a.runPasses(context.Background(), "r", false)
	if got := join(ran); got != "early fixes late" {
		t.Errorf("ran %q: an early pass before the fix list, a late one after, a switched-off one and one with no key never", got)
	}
	if names := Configured(&config.Config{Exa: &config.Exa{}}, config.ExaProvider); len(names) == 0 || names[0] != "t-early" && names[0] != "coverage" {
		t.Errorf("configured passes %v", names)
	}
	// On demand, a pass needs its provider's table.
	if _, err := a.RunPass(context.Background(), fold, "r"); err == nil {
		t.Error("fold with no [typesafe] should refuse")
	}
	defer func() {
		if recover() == nil {
			t.Error("a second pass with the same name should panic")
		}
	}()
	RegisterPass(Pass{Name: "t-early"})
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}
