package app

import (
	"context"
	"fmt"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/suggest"
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

// SuggestReview asks the analyst (one call, within the daily budget) for rewrites, retirements and
// additions that answer a prompt review, as a diff against the set's file. It writes nothing.
func (a *App) SuggestReview(ctx context.Context, rev Review) (suggest.Edits, error) {
	s, ok := a.Set(rev.SetID)
	if !ok {
		return suggest.Edits{}, fmt.Errorf("no prompt set %q", rev.SetID)
	}
	ctx = llm.WithOperation(llm.WithAccounting(ctx, a.Runs.Budget), "review/"+rev.SetID)
	return suggest.Review(ctx, a.Cfg, s, a.Sets(), rev.Brief())
}
