package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/config"
)

// judgeSuggestions is check's last step: with [typesafe] configured and callable here, every
// set's prompts are put to Jev (duplicates, off target, intent, branded; docs/prompt-review.md)
// and each flag is printed as a suggestion: line. It asks only what hasn't been asked before,
// so a check that changes nothing costs nothing. It never fails check: a problem is a note.
// Without a key or proxy it prints one note and opens nothing, so CI stays fast; -no-jev skips
// it altogether.
func judgeSuggestions(ctx context.Context, cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noJev := fs.Bool("no-jev", false, "skip Jev's prompt suggestions")
	if fs.Parse(args) != nil || *noJev || cfg.TypeSafe == nil || !cfg.PromptReviewOn() {
		return
	}
	if m := config.Missing(config.TypeSafeProvider); m != "" {
		fmt.Printf("note: prompt suggestions skipped: %s\n", m)
		return
	}
	a, err := open()
	if err != nil {
		fmt.Printf("note: prompt suggestions skipped: %v\n", err)
		return
	}
	defer a.DB.Close()
	for _, s := range a.Sets() {
		j, err := a.JudgePrompts(ctx, s)
		printSuggestions(s.ID, j)
		if err != nil {
			fmt.Printf("note: Jev on set %s: %v\n", s.ID, err)
		}
	}
}

func printSuggestions(setID string, j app.PromptJudgments) {
	for _, f := range j.All() {
		fmt.Printf("suggestion: set %s: %s (%.2f)\n", setID, f.Line, f.Confidence)
	}
}
