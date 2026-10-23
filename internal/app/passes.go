package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// A Pass is one use of an optional provider over a finished run (docs/optional-providers.md):
// it runs after the run, or on demand as cheapseats <Command> <run-id>, writes what it decides
// to SQLite, and is skipped with a log line when its provider isn't configured or callable.
// Report pages read its table and never run it. Each pass registers itself from init() in its
// own file, so adding one touches no shared code.
type Pass struct {
	// Name is the ledger and log name, such as fold-names: spend events are recorded under
	// provider-attempt/<Name>/<run-id>.
	Name string
	// Command is the CLI subcommand that runs it on a finished run, or "" for none.
	Command string
	// Usage is the one-line description for cheapseats help.
	Usage string
	// Order sorts the passes after a run; those under 100 run before the fix list is built, so
	// it sees their results, and those at 100 or over run after it.
	Order int
	// Provider is config.TypeSafeProvider or config.ExaProvider.
	Provider string
	// On reports whether the config switches the pass on.
	On func(*config.Config) bool
	// Run does the work for one run and returns how many items it decided, searched or read.
	// Items already decided are skipped, so running it again costs nothing.
	Run func(a *App, ctx context.Context, runID string) (int, error)
	// Print, when set, prints the pass's result for the CLI after Run.
	Print func(a *App, ctx context.Context, runID string, n int)
}

var (
	passMu sync.Mutex
	passes []Pass
)

// RegisterPass adds a pass; a second pass with the same Name or Command is a programming error.
func RegisterPass(p Pass) {
	passMu.Lock()
	defer passMu.Unlock()
	for _, q := range passes {
		if q.Name == p.Name || (p.Command != "" && q.Command == p.Command) {
			panic(fmt.Sprintf("pass %q registered twice", p.Name))
		}
	}
	passes = append(passes, p)
	sort.SliceStable(passes, func(i, j int) bool { return passes[i].Order < passes[j].Order })
}

// Passes lists the registered passes in run order.
func Passes() []Pass {
	passMu.Lock()
	defer passMu.Unlock()
	return append([]Pass(nil), passes...)
}

// PassByCommand finds the pass that cheapseats <cmd> runs.
func PassByCommand(cmd string) (Pass, bool) {
	for _, p := range Passes() {
		if p.Command != "" && p.Command == cmd {
			return p, true
		}
	}
	return Pass{}, false
}

// Configured lists the names of the passes the config switches on for a provider.
func Configured(cfg *config.Config, provider string) []string {
	var out []string
	for _, p := range Passes() {
		if p.Provider == provider && p.On(cfg) {
			out = append(out, p.Name)
		}
	}
	return out
}

// runPasses runs the passes the config switches on, in order, over a finished run: those with
// Order under 100 when before is true, the rest otherwise. A pass whose provider isn't callable
// here is skipped with a log line, like an answerer whose key is missing; a pass that fails is
// logged and the next one runs.
func (a *App) runPasses(ctx context.Context, id string, before bool) {
	for _, p := range Passes() {
		if (p.Order < 100) != before || !p.On(a.Cfg) {
			continue
		}
		if m := config.Missing(p.Provider); m != "" {
			slog.Info("pass skipped", "pass", p.Name, "run", id, "reason", m)
			continue
		}
		if _, err := p.Run(a, ctx, id); err != nil {
			slog.Error(p.Name, "run", id, "err", err)
		}
	}
}

// RunPass runs one pass on demand, as the CLI does, and returns its count. The pass runs even
// when its switch is off: the switch governs what happens after a run, not what you ask for.
func (a *App) RunPass(ctx context.Context, p Pass, id string) (int, error) {
	if p.Provider == config.TypeSafeProvider && a.Cfg.TypeSafe == nil {
		return 0, fmt.Errorf("no [typesafe] in the config")
	}
	if p.Provider == config.ExaProvider && a.Cfg.Exa == nil {
		return 0, fmt.Errorf("no [exa] in the config")
	}
	return p.Run(a, ctx, id)
}
