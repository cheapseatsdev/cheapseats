package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/app"
)

// reviewCmd is cheapseats review: a set's prompts reviewed from their stored answers, and with
// -suggest the analyst's proposed edits as a diff against the set's file. It writes nothing.
func reviewCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	days := fs.Int("days", app.ReviewWindows[0], "window in days: 30, 60 or 90")
	js := fs.Bool("json", false, "print JSON")
	sg := fs.Bool("suggest", false, "also propose rewrites, retirements and additions (one analyst call)")
	fs.Parse(positionalLast(args))
	if fs.NArg() != 1 {
		return errors.New("usage: cheapseats review <set-id> [-days 30] [-json] [-suggest]")
	}
	a, err := open()
	if err != nil {
		return err
	}
	rev, err := a.Review(ctx, fs.Arg(0), *days)
	if err != nil {
		return err
	}
	a.MarkReviewed(ctx, rev.SetID)
	var edits any
	if *sg {
		e, err := a.SuggestReview(ctx, rev)
		if err != nil {
			return err
		}
		edits = e
		if !*js {
			defer func() {
				fmt.Printf("\n## Proposed edits\n\n%s", e.Diff)
				fmt.Fprintf(os.Stderr, "%d rewrites, %d retirements, %d additions (%d dropped), $%.3f. Nothing was written.\n", len(e.Rewrites), len(e.Retire), len(e.Add), e.Dropped, e.CostUSD)
			}()
		}
	}
	if *js {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			app.Review
			Suggested any `json:"suggested,omitempty"`
		}{rev, edits})
	}
	printReview(rev)
	return nil
}

func printReview(rev app.Review) {
	fmt.Printf("# Prompt review: %s, last %d days\n\n%d runs since %s, %d graded answers, %d prompts: %d healthy, %d without enough data yet (needs %d runs and %d answers).\n",
		rev.SetName, rev.Days, rev.Runs, rev.Since, rev.Answers, len(rev.Prompts), len(rev.Healthy), len(rev.NotEnough), app.ReviewMinRuns, app.ReviewMinAnswers)
	for _, s := range rev.Sections() {
		fmt.Printf("\n## %s (%d)\n", s.Title, len(s.Items))
		for _, f := range s.Items {
			fmt.Printf("\n- %s: %s", f.Prompt, f.Text)
			if f.Variant != "" {
				fmt.Printf(" [%s]", f.Variant)
			}
			if f.Other != "" {
				fmt.Printf("\n  and %s: %s", f.Other, f.OtherText)
			}
			fmt.Printf("\n  %s\n  -> %s\n", f.Numbers, f.Suggestion)
		}
	}
	fmt.Printf("\n## Gaps (%d)\n", len(rev.Gaps))
	for _, g := range rev.Gaps {
		fmt.Printf("\n- %s %s: %s\n", g.Kind, g.Name, g.Numbers)
		for _, t := range []string{g.TOML, g.Competitor} {
			if t != "" {
				fmt.Printf("\n%s\n", indent(t))
			}
		}
	}
	if len(rev.Healthy) > 0 {
		fmt.Printf("\nHealthy: %s\n", strings.Join(rev.Healthy, ", "))
	}
	if len(rev.NotEnough) > 0 {
		fmt.Printf("Not enough data yet: %s\n", strings.Join(rev.NotEnough, ", "))
	}
}

func indent(s string) string { return "    " + strings.ReplaceAll(s, "\n", "\n    ") }
