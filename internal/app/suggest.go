package app

import (
	"context"
	"fmt"

	"github.com/metcalfc/popcorn/internal/llm"
	"github.com/metcalfc/popcorn/internal/suggest"
)

// SuggestPrompts drafts n new prompts for a set (one analyst call, within the daily budget) as a
// TOML snippet to paste into its file. It writes nothing.
func (a *App) SuggestPrompts(ctx context.Context, setID string, n int) (suggest.Result, error) {
	s, ok := a.Set(setID)
	if !ok {
		return suggest.Result{}, fmt.Errorf("no prompt set %q", setID)
	}
	ctx = llm.WithOperation(llm.WithAccounting(ctx, a.Runs.Budget), "suggest/"+setID)
	return suggest.Prompts(ctx, a.Cfg, s, a.Sets(), n)
}
