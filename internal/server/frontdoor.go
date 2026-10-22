package server

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The front door: get_started is the first call an MCP client makes, a page of what this Popcorn
// tracks, what it has, what can run here and what to call next, small enough to read whole.

// Data states, from nothing to read to fresh.
const (
	NoRuns  = "no_runs" // no set has a finished run yet
	Running = "running" // a run is in progress
	Stale   = "stale"   // a set never ran, or its newest finished run is older than its schedule
	Ready   = "ready"   // every set has a recent finished run
)

// staleGrace is how far past its schedule a set's newest run may be before it's stale: the
// scheduler runs at one hour of the day, and a run takes a while.
const staleGrace = 12 * time.Hour

type FrontDoor struct {
	Brand     string     `json:"brand"`
	Sites     []doorSite `json:"sites"`
	DataState string     `json:"data_state"`
	Sets      []doorSet  `json:"sets"`
	Spend     doorSpend  `json:"spend_today"`
	// Which providers can run here, as popcorn check says.
	Readiness app.Readiness `json:"readiness"`
	Audit     *doorAudit    `json:"latest_audit"` // null when the sites were never audited
	// Fixes from the latest runs nobody has submitted work for, and the urgent ones among them.
	OpenFixes   int `json:"open_fixes"`
	UrgentFixes int `json:"open_urgent_fixes"`
	// Work submitted for fixes whose outcome isn't measured yet (the set hasn't run since, or not
	// for long enough).
	AwaitingOutcome int        `json:"attempts_awaiting_outcome"`
	NextSteps       []nextStep `json:"next_steps"`
	// MCP prompts this server offers, for a client that shows them.
	Prompts []string `json:"prompts"`
}

type doorSite struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

type doorSet struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tier      string `json:"tier"`
	Prompts   int    `json:"prompts"`
	EveryDays int    `json:"every_days"`
	// never_run, running, stale or fresh.
	Status string `json:"status"`
	// Its newest run of any status, and its newest finished one with the headline (unbranded) rates.
	LastRun      *doorRun `json:"last_run,omitempty"`
	LastFinished *doorRun `json:"last_finished,omitempty"`
}

type doorRun struct {
	ID          string   `json:"id"`
	Status      string   `json:"status"`
	CreatedAt   string   `json:"created_at"`
	AgeDays     float64  `json:"age_days"`
	Named       *float64 `json:"named,omitempty"`
	Recommended *float64 `json:"recommended,omitempty"`
	Rank        int      `json:"share_of_voice_rank,omitempty"`
}

type doorSpend struct {
	Spent     float64  `json:"spent_usd"`
	Budget    float64  `json:"daily_budget_usd"`
	Remaining *float64 `json:"remaining_usd,omitempty"` // absent with no daily budget
}

type doorAudit struct {
	At       string  `json:"at"`
	AgeDays  float64 `json:"age_days"`
	Errors   int     `json:"errors"`
	Warnings int     `json:"warnings"`
}

// nextStep is a tool call worth making now, with its arguments and why.
type nextStep struct {
	Tool string         `json:"tool,omitempty"` // empty for something to do outside MCP
	Args map[string]any `json:"args,omitempty"`
	Why  string         `json:"why"`
}

// dueSteps is how many sets due a run next_steps lists.
const dueSteps = 3

// mcpPrompts are the prompt templates registered by addPrompts, by name.
var mcpPrompts = []string{"weekly_pulse", "competitor_radar", "why_not_recommended"}

func ageDays(at string, now time.Time) float64 {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return 0
	}
	return math.Round(now.Sub(t).Hours()/24*10) / 10
}

func (s *Server) everyDays(st sets.Set) int {
	if st.Tier == sets.PLG {
		return s.App.Cfg.Tiers.PLG.EveryDays
	}
	return s.App.Cfg.Tiers.Core.EveryDays
}

func (s *Server) frontDoor(ctx context.Context) FrontDoor {
	now := time.Now().UTC()
	fd := FrontDoor{Brand: s.App.Cfg.Brand.Name, Sites: []doorSite{}, Sets: []doorSet{}, Readiness: s.App.Readiness(), Prompts: mcpPrompts}
	for _, st := range s.App.Cfg.Brand.AllSites() {
		fd.Sites = append(fd.Sites, doorSite{st.URL, st.Kind})
	}
	spent, _ := s.App.DB.SpentToday(ctx)
	fd.Spend = doorSpend{Spent: round2(spent), Budget: s.App.Cfg.Budget.DailyUSD}
	if d := fd.Spend.Budget; d > 0 {
		left := round2(math.Max(0, d-spent))
		fd.Spend.Remaining = &left
	}

	progress, _ := s.App.Progress(ctx)
	var running, never, stale, fresh []doorSet
	finished := 0
	for _, c := range s.cards(ctx, progress) {
		ds := doorSet{ID: c.Set.ID, Name: c.Set.Name, Tier: string(c.Set.Tier), Prompts: len(c.Set.Prompts), EveryDays: s.everyDays(c.Set)}
		if r := c.Latest; r != nil {
			ds.LastRun = &doorRun{ID: r.ID, Status: r.Status, CreatedAt: r.CreatedAt, AgeDays: ageDays(r.CreatedAt, now)}
		}
		if r := c.Baseline; r != nil && c.Summary != nil {
			u := c.Summary.Unbranded
			named, rec := round2(u.Visible.P), round2(u.Recommended.P)
			ds.LastFinished = &doorRun{ID: r.ID, Status: r.Status, CreatedAt: r.CreatedAt, AgeDays: ageDays(r.CreatedAt, now), Named: &named, Recommended: &rec, Rank: c.Rank}
			finished++
		}
		switch {
		case c.Running != nil || ds.LastRun != nil && (ds.LastRun.Status == "queued" || ds.LastRun.Status == "running"):
			ds.Status = Running
			running = append(running, ds)
		case ds.LastFinished == nil:
			ds.Status = "never_run"
			never = append(never, ds)
		case ds.LastFinished.AgeDays*24 > float64(ds.EveryDays)*24+staleGrace.Hours():
			ds.Status = Stale
			stale = append(stale, ds)
		default:
			ds.Status = "fresh"
			fresh = append(fresh, ds)
		}
		fd.Sets = append(fd.Sets, ds)
	}
	switch {
	case len(running) > 0:
		fd.DataState = Running
	case finished == 0:
		fd.DataState = NoRuns
	case len(stale)+len(never) > 0: // a set that never ran is as out of date as can be
		fd.DataState = Stale
	default:
		fd.DataState = Ready
	}

	if a, _ := s.App.LatestAudit(ctx); a != nil {
		fd.Audit = &doorAudit{At: a.At, AgeDays: ageDays(a.At, now), Errors: a.Errors(), Warnings: a.Warnings()}
	}
	qw, _ := s.App.QuickWins(ctx)
	for _, q := range qw {
		if q.Status == app.Open {
			fd.OpenFixes++
			if q.Priority == "urgent" {
				fd.UrgentFixes++
			}
		}
	}
	at, _ := s.App.Attempts(ctx, "")
	for _, x := range at {
		if st := x.Outcome.Status(); x.State != app.Withdrawn && (st == app.OutcomePending || st == app.OutcomeTooEarly) {
			fd.AwaitingOutcome++
		}
	}
	fd.NextSteps = fd.steps(running, never, stale, fresh)
	return fd
}

// steps are the calls worth making next, most useful first.
func (fd FrontDoor) steps(running, never, stale, fresh []doorSet) []nextStep {
	out := []nextStep{}
	add := func(tool string, args map[string]any, why string, a ...any) {
		out = append(out, nextStep{tool, args, fmt.Sprintf(why, a...)})
	}
	rd := fd.Readiness
	if !rd.CanRun {
		why := "no answerer can run here"
		if !rd.Grader.Usable {
			why = "the grader can't run here: " + rd.Grader.Missing
		}
		add("", nil, "Runs can't start: %s. Fix the environment (popcorn check on the command line says what's missing); the reads below still work.", why)
	}
	for _, r := range head(running, 2) {
		add("get_run", map[string]any{"run": r.LastRun.ID}, "%s is running: its rates fill in as answers are graded. Poll every few minutes, not faster.", r.Name)
	}
	// Sets due a run, never-run ones first, at most dueSteps of them.
	if rd.CanRun {
		for i, r := range head(append(append([]doorSet(nil), never...), stale...), dueSteps) {
			if i < len(never) {
				add("start_run", map[string]any{"set": r.ID, "dry_run": true}, "%s has never finished a run. Dry-run it to see its answers and estimated cost, then call start_run again without dry_run to start it.", r.Name)
			} else {
				add("start_run", map[string]any{"set": r.ID, "dry_run": true}, "%s last finished %.0f days ago (it's due every %d). Dry-run it, then start it.", r.Name, r.LastFinished.AgeDays, r.EveryDays)
			}
		}
	}
	// The newest finished report, fresh sets first.
	if done := append(append([]doorSet(nil), fresh...), stale...); len(done) > 0 {
		add("get_run", map[string]any{"run": done[0].LastFinished.ID}, "The latest finished report for %s: rates, share of voice, fixes and one line per prompt.", done[0].Name)
	}
	switch {
	case fd.UrgentFixes > 0:
		add("get_quick_wins", map[string]any{"status": "open", "priority": "urgent", "brief": true}, "%d urgent fixes are open. Pick one, then get it again with limit to read its Claude Code prompt.", fd.UrgentFixes)
	case fd.OpenFixes > 0:
		add("get_quick_wins", map[string]any{"status": "open", "brief": true, "limit": 5}, "%d fixes are open; these are the fastest ranking gains.", fd.OpenFixes)
	}
	switch {
	case fd.Audit == nil:
		add("audit_sites", nil, "The brand's sites were never audited for AI crawlers. Free: plain HTTP, no model calls.")
	case fd.Audit.AgeDays > 7:
		add("audit_sites", nil, "The last site audit is %.0f days old. Free.", fd.Audit.AgeDays)
	case fd.Audit.Errors > 0:
		add("audit_sites", map[string]any{"cached": true}, "The latest site audit found %d errors and %d warnings; read them with their fixes.", fd.Audit.Errors, fd.Audit.Warnings)
	}
	if fd.AwaitingOutcome > 0 {
		add("list_attempts", nil, "%d submitted fixes are waiting for the set to run again before their outcome can be measured.", fd.AwaitingOutcome)
	}
	return out
}

// addPrompts registers the MCP prompt templates: each tells the model which tools to call, in
// what order, and what to say.
func addPrompts(srv *mcp.Server) {
	text := func(desc, body string) *mcp.GetPromptResult {
		return &mcp.GetPromptResult{Description: desc, Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: body}}}}
	}
	setArg := &mcp.PromptArgument{Name: "set", Description: "a prompt set id, from list_prompt_sets (default: every set)"}
	scope := func(args map[string]string) string {
		if s := args["set"]; s != "" {
			return fmt.Sprintf("prompt set %q", s)
		}
		return "every prompt set"
	}
	srv.AddPrompt(&mcp.Prompt{Name: "weekly_pulse", Title: "Weekly pulse", Description: "A short weekly read-out: what moved, what's open, what to do this week.", Arguments: []*mcp.PromptArgument{setArg}},
		func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return text("Weekly pulse", fmt.Sprintf(`Give me this week's AI visibility pulse for %s.

1. Call get_started. If data_state is no_runs or stale, say so first and offer the dry run it suggests; don't start a run without asking.
2. Call get_rankings with last_days=14 (and set, if one is named). Report the change in named and recommended over the week, and say plainly when a change is within_noise.
3. Call get_run on the newest finished run of each set: the headline (unbranded) rates, the share-of-voice leaders, and the top two objections.
4. Call get_quick_wins with status=open, brief=true, limit=5, and list_attempts for work whose outcome came in.

Write it as five bullets at most, then "This week:" with the one or two fixes to take on and why. No tables, no rates without their intervals.`, scope(req.Params.Arguments))), nil
		})
	srv.AddPrompt(&mcp.Prompt{Name: "competitor_radar", Title: "Competitor radar", Description: "Who is winning which prompts, and from which sources.",
		Arguments: []*mcp.PromptArgument{setArg, {Name: "competitor", Description: "focus on this competitor (default: the leaders)"}}},
		func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			focus := "the products ahead of the brand"
			if c := req.Params.Arguments["competitor"]; c != "" {
				focus = fmt.Sprintf("%q", c)
			}
			return text("Competitor radar", fmt.Sprintf(`Build a competitor radar for %s, focused on %s.

1. Call list_runs (with set, if one is named) and take the newest run with status done for each set.
2. Call get_run on it. From share_of_voice and head_to_head, rank the competitors; from prompts, list the prompts each one is the top pick for while the brand isn't recommended.
3. From top_hosts and outreach, name the sources that cite them and never the brand.
4. Call get_rankings with last_days=28 to say whether each lead is growing or shrinking (and when that's within noise).

Answer as one short section per competitor: the prompts it wins, the sources behind it, and the trend. End with the single source or page most worth getting the brand onto.`, scope(req.Params.Arguments), focus)), nil
		})
	srv.AddPrompt(&mcp.Prompt{Name: "why_not_recommended", Title: "Why not recommended", Description: "Why the assistants don't recommend the brand for one prompt, and what would change that.",
		Arguments: []*mcp.PromptArgument{{Name: "prompt", Description: "the prompt's text, or its number in the set", Required: true}, setArg}},
		func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return text("Why not recommended", fmt.Sprintf(`Explain why the assistants don't recommend the brand for this prompt: %q (in %s).

1. Call list_runs and get_run on the newest finished run of the set it's in; find the prompt in prompts (match the text or the number).
2. For two or three of its results, call get_result: read what each answer recommended instead, its why_not_top and objections, and which pages its searches surfaced and cited.
3. Call get_search_queries on the run for what the assistants searched for this prompt, and whether the brand's pages came up.
4. Call get_quick_wins with brief=true and check whether an open fix already targets this prompt.

Answer with: who wins it and why (quote the answers), whether it's a documentation, positioning or third-party gap, and the one change most likely to flip it. If a fix exists, name it instead of inventing one.`, req.Params.Arguments["prompt"], scope(req.Params.Arguments))), nil
		})
}
