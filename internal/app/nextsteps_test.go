package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

func TestNextStepsKeepsLegacyWorkWithoutPromotingIt(t *testing.T) {
	a, runs := fixMatchApp(t, "https://cheapseats.dev/plans")
	ctx := context.Background()
	fixes, _, _ := a.Fixes(ctx, runs["s"])
	fixes[0].Effort = ""           // older JSON need not include today's ranking metadata
	body, _ := json.Marshal(fixes) // the array format had no trustworthy verification record
	if _, err := a.DB.Exec(`UPDATE runs SET fixes=? WHERE id=?`, string(body), runs["s"]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.MarkFix(ctx, runs["s"], 1, "https://example.org/commit/1", "accepted", "Already shipped", "test"); err != nil {
		t.Fatal(err)
	}
	next, err := a.NextSteps(ctx)
	if err != nil || len(next.Actions) != 2 || len(next.Investigations) != 2 {
		t.Fatalf("next steps: %+v %v", next, err)
	}
	var legacy *QuickWin
	for _, inv := range next.Investigations {
		if inv.Legacy != nil && inv.Legacy.Rank == 1 {
			legacy = inv.Legacy
		}
	}
	if legacy == nil || legacy.Status != "accepted" || len(legacy.Attempts) != 1 || legacy.ClaudePrompt != "" {
		t.Fatalf("legacy work status must survive without an implementation prompt: %+v", legacy)
	}
	if _, err := json.Marshal(next); err != nil {
		t.Fatalf("incomplete legacy effort broke the JSON advice API: %v", err)
	}
	runSteps, err := a.RunNextSteps(ctx, runs["s"])
	if err != nil || len(runSteps.Actions) != 0 || len(runSteps.Investigations) != 2 {
		t.Fatalf("historical run: %+v %v", runSteps, err)
	}
}

func TestNextStepsBoundaryChangeRequiresNewVerification(t *testing.T) {
	a, runs := fixMatchApp(t, "https://cheapseats.dev/plans")
	ctx := context.Background()
	before, _ := a.QuickWins(ctx)
	if len(before) != 4 {
		t.Fatalf("verified fixture: %d", len(before))
	}
	a.Cfg.Brand.Advisory.Boundaries = []string{"No enterprise procurement features"}
	ready, _ := a.QuickWins(ctx)
	next, err := a.NextSteps(ctx)
	if err != nil || len(ready) != 0 || len(next.Investigations) != 4 {
		t.Fatalf("advisory change must invalidate readiness: %+v %v", next, err)
	}
	for _, inv := range next.Investigations {
		if inv.Legacy.ClaudePrompt != "" {
			t.Fatal("stale implementation prompt exposed")
		}
	}
	r, _ := a.Runs.Get(ctx, runs["s"])
	method, design := run.Method(r.Snapshot), run.Design(r.Snapshot)
	r.Snapshot.Brand.Advisory = a.Cfg.Brand.Advisory
	if run.Method(r.Snapshot) != method || run.Design(r.Snapshot) != design {
		t.Fatal("advisory context changed measurement comparability")
	}
}

func TestNextStepsQuestionChangeRequiresNewVerification(t *testing.T) {
	a, runs := fixMatchApp(t, "https://cheapseats.dev/plans")
	for i := range a.sets {
		if a.sets[i].ID == "s" {
			a.sets[i].Prompts[0].Text = "Which tools meet enterprise security procurement?"
		}
	}
	ctx := context.Background()
	next, err := a.NextSteps(ctx)
	if err != nil || len(next.Actions) != 3 || len(next.Investigations) != 1 || next.Investigations[0].Legacy.ClaudePrompt != "" {
		t.Fatalf("changed buyer question kept stale readiness: %+v %v", next, err)
	}
	historical, err := a.RunNextSteps(ctx, runs["s"])
	if err != nil || len(historical.Actions) != 1 || len(historical.Investigations) != 1 {
		t.Fatalf("historical handoff did not flag the changed question: %+v %v", historical, err)
	}
}

func TestNextStepsEnvelopeSurvivesReopenWithFindingsAndChecks(t *testing.T) {
	a, runs := fixMatchApp(t, "https://cheapseats.dev/plans")
	ctx := context.Background()
	fixes, _, _ := a.Fixes(ctx, runs["s"])
	an := analysis.Analysis{Fixes: fixes,
		Investigations: []analysis.Investigation{{Title: "Check listing eligibility", Why: "A useful directory has submission rules", Check: "Read its rules", Decision: "Decide whether to submit", Evidence: "The answer cited the directory", PromptIDs: []string{"s1"}}},
		Findings:       []analysis.Finding{{Title: "Hosted buyers prefer a managed tool", Detail: "Self-hosting is an accepted tradeoff", Evidence: "The buyer asks for a managed service", PromptIDs: []string{"s1"}}}}
	if _, err := a.DB.Exec(`UPDATE runs SET fixes=? WHERE id=?`, analysis.Encode(an), runs["s"]); err != nil {
		t.Fatal(err)
	}
	// New performs the same backwards-compatible read/backfill path used on restart.
	reopened, err := New(a.Cfg, a.DB)
	if err != nil {
		t.Fatal(err)
	}
	next, err := reopened.NextSteps(ctx)
	if err != nil || len(next.Actions) != 4 || len(next.Investigations) != 1 || len(next.Findings) != 1 {
		t.Fatalf("stored next steps: %+v %v", next, err)
	}
}
