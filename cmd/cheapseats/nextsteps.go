package main

import (
	"fmt"
	"io"

	"github.com/cheapseatsdev/cheapseats/internal/app"
)

// printNextSteps prints a run's next steps for `cheapseats fixes`: analysis errors to stderr,
// the lists to stdout. kept is the failed rebuild that left the run's earlier analysis in place,
// returned as the error after the lists; it says which analysis was shown only when no analysis
// error above already did.
func printNextSteps(stdout, stderr io.Writer, setName string, hasFixes bool, next app.NextSteps, kept *app.KeptAnalysisError) error {
	said := false
	for _, e := range next.Errors {
		msg := e.Message
		if e.RequestID != "" {
			msg += " (request " + e.RequestID + ")"
		}
		fmt.Fprintln(stderr, "next steps analysis error:", msg)
		fmt.Fprintln(stderr, e.Showing())
		said = said || e.Showing() == app.KeptAnalysisNote
	}
	if !hasFixes && len(next.Investigations) == 0 && len(next.Findings) == 0 {
		fmt.Fprintln(stdout, "No completed Next steps analysis yet.")
		return nil
	}
	fmt.Fprintf(stdout, "# Next steps: %s\n\n%d actions · %d investigations · %d market findings\n\n", setName, len(next.Actions), len(next.Investigations), len(next.Findings))
	fmt.Fprintln(stdout, "## Actions")
	for _, q := range next.Actions {
		fmt.Fprintf(stdout, "\n- **%s**\n  - Change: %s\n  - Where: %s\n  - Done when: %s\n  - Verification: %s\n", q.Title, q.Claim, q.Target, q.DoneWhen, q.Verification.Summary)
	}
	fmt.Fprintln(stdout, "\n## Investigations")
	for _, inv := range next.Investigations {
		fmt.Fprintf(stdout, "\n- **%s**\n  - Why: %s\n  - Read-only check: %s\n  - Decision: %s\n", inv.Title, inv.Why, inv.Check, inv.Decision)
	}
	fmt.Fprintln(stdout, "\n## Market findings")
	for _, f := range next.Findings {
		fmt.Fprintf(stdout, "\n- **%s**: %s\n  - Evidence: %s\n", f.Title, f.Detail, f.Evidence)
	}
	switch {
	case kept == nil:
		return nil
	case said:
		return fmt.Errorf("next steps rebuild failed: %w", kept)
	default:
		return fmt.Errorf("next steps rebuild failed: %w\n%s", kept, app.KeptAnalysisNote)
	}
}
