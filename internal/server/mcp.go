package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/analysis"
	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/crawl"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/probe"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpHandler serves the same reads and actions as the JSON API over MCP (Streamable HTTP), so
// Claude Code or any MCP client can start runs and read results. It sits behind the same bearer
// tokens as /api/*.
func (s *Server) mcpHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "cheapseats-" + s.App.Cfg.Brand.Slug(), Title: "Cheap Seats: " + s.App.Cfg.Brand.Name, Version: "1"}, nil)
	type none struct{}
	type setArg struct {
		Set string `json:"set" jsonschema:"prompt set id, from list_prompt_sets"`
	}
	type runArg struct {
		Run string `json:"run" jsonschema:"run id, from list_runs or start_run"`
	}
	addTool(srv, "get_started", "Call this first. What this Cheap Seats tracks and where it stands, in one small result: the brand and its sites, each prompt set with its tier, last run and status, today's spend against the budget, which answerers and the grader can run here, the latest site audit's errors and warnings, open and urgent fixes, fix attempts awaiting an outcome, data_state (no_runs, running, stale or ready) and next_steps: the tool calls worth making now, with their arguments. Free.",
		func(ctx context.Context, _ none) (any, error) {
			return s.frontDoor(ctx), nil
		})
	addTool(srv, "list_prompt_sets", "The checked-in prompt sets: id, name, tier, description and prompt count.",
		func(ctx context.Context, _ none) (any, error) {
			type row struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Tier        string `json:"tier"`
				Description string `json:"description"`
				Prompts     int    `json:"prompts"`
			}
			out := []row{}
			for _, st := range s.App.Sets() {
				out = append(out, row{st.ID, st.Name, string(st.Tier), st.Description, len(st.Prompts)})
			}
			return out, nil
		})
	addTool(srv, "get_prompt_set", "One prompt set in full: its prompts and the competitor names the grader folds onto.",
		func(ctx context.Context, a setArg) (any, error) {
			st, ok := s.App.Set(a.Set)
			if !ok {
				return nil, fmt.Errorf("no prompt set %q", a.Set)
			}
			return st, nil
		})
	addTool(srv, "start_run", "Start a run over a prompt set. Costs money: every prompt is answered by each configured assistant and graded. Call it with dry_run=true first: that returns the plan (answers per answerer and track, answerers skipped, estimated cost from recent runs, today's remaining budget, and whether it would be refused) and starts nothing. Without dry_run it returns the run id; wait_for_run blocks until it finishes, or poll get_run.",
		func(ctx context.Context, a struct {
			Set    string `json:"set" jsonschema:"prompt set id, from list_prompt_sets"`
			DryRun bool   `json:"dry_run,omitempty" jsonschema:"plan the run and estimate its cost without starting it"`
		}) (any, error) {
			if a.DryRun {
				return s.App.DryRun(ctx, a.Set)
			}
			r, err := s.App.StartRun(ctx, a.Set, "mcp")
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": r.ID, "status": string(r.Status)}, nil
		})
	addTool(srv, "list_runs", "Recent runs, newest first, with status and headline rates.",
		func(ctx context.Context, a struct {
			Set   string `json:"set,omitempty" jsonschema:"only runs over this prompt set"`
			Limit int    `json:"limit,omitempty" jsonschema:"how many (default 20, max 100)"`
		}) (any, error) {
			if a.Limit <= 0 || a.Limit > 100 {
				a.Limit = 20
			}
			runs, err := s.App.Runs.List(ctx, a.Set, a.Limit)
			if err != nil {
				return nil, err
			}
			type row struct {
				ID, Name, SetID, Status, CreatedAt string
				Rates                              report.Rates
				Rank                               int
			}
			out := []row{}
			for _, x := range runs {
				res, _ := s.App.Runs.Results(ctx, x.ID)
				sum := s.App.Summary(&x, res)
				out = append(out, row{x.ID, x.Name, x.SetID, string(x.Status), x.CreatedAt, sum.Overall, sum.BrandRank()})
			}
			return out, nil
		})
	addTool(srv, "wait_for_run", "Wait for a run to finish, then return its status, headline rates and rank (the list_runs row). Finished means every answer is graded and the fix list is built, so get_run is complete. Blocks up to timeout_seconds (default 90, at most 600; keep it under your client's own tool timeout) and otherwise returns with timed_out=true and the run's progress so far (stage, answers done, errors, time left): call it again to keep waiting. A run that is queued with nothing running it (left from a restart) says so in note. Free.",
		func(ctx context.Context, a struct {
			Run            string `json:"run" jsonschema:"run id, from list_runs or start_run"`
			TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to wait before returning timed_out (default 90, max 600)"`
		}) (any, error) {
			return s.waitForRun(ctx, a.Run, a.TimeoutSeconds)
		})
	addTool(srv, "get_run", "One run: rates per answerer and track (and without branded prompts), share of voice with positions, head to head, objections (wordings Jev judged the same count as one, with a wordings count; each with the top two pages the answers raising it cited, owned or earned, and how many were recall answers from memory), sources by kind and host, cited pages by type (listicle, comparison, review, docs, forum...), an outreach list of hosts cited without the brand (and, when [exa] has run the prospects pass, prospects: pages like the cited listicles that no answer cited, with whom each names), cited_page_changes: cited pages that started or stopped naming the brand or a competitor since the set's previous citation check (when both runs were checked), the brand's pages found but not cited, the fix list, net sentiment with top praise and objections (each with the same pages behind it), the run's themes, the facts answers got wrong about the brand (when brand.facts are checked), one line per prompt, and, when [exa] is configured, the neutral retrieval baseline per host (prompts a neutral index ranks it for against prompts the assistants found it for, read as content, query or both), and, when [typesafe] has read the run, second_opinion: how many grades Jev agreed with, disagreed with or was unsure about, the agreement rate, and up to 10 disagreements. Use get_result for a full answer.",
		func(ctx context.Context, a runArg) (any, error) {
			d, err := s.runData(ctx, a.Run)
			if err != nil {
				return nil, err
			}
			return runDigest(d), nil
		})
	addTool(srv, "get_result", "One answer in full: the text, its grade (with evidence, the sentence of the answer that most supports the verdict, checked to be in the text), every search, result URL and citation, and, when [typesafe] has read the run, second_opinion: Jev's verdict and evidence sentence beside the grader's, and whether they agree.",
		func(ctx context.Context, a struct {
			Run    string `json:"run" jsonschema:"run id"`
			Result int64  `json:"result" jsonschema:"result id, from get_run"`
		}) (any, error) {
			res, err := s.App.Runs.Results(ctx, a.Run)
			if err != nil {
				return nil, err
			}
			for _, x := range res {
				if x.ID == a.Result {
					return struct {
						run.Result
						SecondOpinion *app.Opinion `json:"second_opinion,omitempty"`
					}{x, s.App.Opinions(ctx, a.Run, res)[x.ID]}, nil
				}
			}
			return nil, fmt.Errorf("run %s has no result %d", a.Run, a.Result)
		})
	addTool(srv, "get_next_steps", "Next steps from each set's latest analysis, separated into verified actions, read-only investigations and market findings. Only actions include implementation prompts. Legacy suggestions without recorded verification remain investigations. Respect the brand's chosen scope: an intentional limitation may need no change. Use kind to focus on one category, and brief to omit action handoffs. Free; uses stored analysis.",
		func(ctx context.Context, a struct {
			Set      string `json:"set,omitempty" jsonschema:"only this prompt set"`
			Kind     string `json:"kind,omitempty" jsonschema:"actions, investigations, market_findings, or empty for all"`
			Status   string `json:"status,omitempty" jsonschema:"action work status: open, proposed, accepted or dismissed"`
			Priority string `json:"priority,omitempty" jsonschema:"action priority: urgent, high, medium or low"`
			Limit    int    `json:"limit,omitempty" jsonschema:"maximum items per category"`
			Brief    bool   `json:"brief,omitempty" jsonschema:"omit action implementation prompts"`
		}) (any, error) {
			steps, err := s.App.NextSteps(ctx)
			if err != nil {
				return nil, err
			}
			return s.selectNextSteps(steps, app.QuickWinFilter{Set: a.Set, Status: a.Status, Priority: a.Priority}, a.Kind, a.Limit, a.Brief), nil
		})
	addTool(srv, "get_quick_wins", "Compatibility view of verified actions only, from the latest analysis of every prompt set. Use get_next_steps for investigations and market findings. Each action has a ready-to-paste prompt for Claude Code, its status (open, proposed or accepted; dismissed fixes only when asked for) and the work submitted for it or earlier for the same page. A page that several sets' lists ask for is one card, with the other sets' fixes under also_in (each with the run_id and rank mark_fix takes). When a set's latest analysis failed, a second text block holds analysis_errors, as get_next_steps gives them: the error and which analysis the list shows instead.",
		func(ctx context.Context, a struct {
			Gap      string `json:"gap,omitempty" jsonschema:"documentation, positioning, third-party or product"`
			Set      string `json:"set,omitempty" jsonschema:"only fixes from this prompt set"`
			Site     string `json:"site,omitempty" jsonschema:"only fixes on this site of the brand's, by url or kind (docs, blog, marketing...), or elsewhere for fixes off its sites"`
			Status   string `json:"status,omitempty" jsonschema:"open (nothing submitted yet), proposed, accepted or dismissed"`
			Priority string `json:"priority,omitempty" jsonschema:"urgent, high, medium or low"`
			Limit    int    `json:"limit,omitempty" jsonschema:"at most this many, from the top"`
			Brief    bool   `json:"brief,omitempty" jsonschema:"leave out each fix's Claude Code prompt, the bulk of the result; get it again with a limit when you take a fix on"`
		}) (any, error) {
			qw, err := s.App.QuickWins(ctx)
			if err != nil {
				return nil, err
			}
			out := s.App.FilterQuickWins(qw, app.QuickWinFilter{Gap: a.Gap, Set: a.Set, Site: a.Site, Status: a.Status, Priority: a.Priority})
			if a.Limit > 0 && len(out) > a.Limit {
				out = out[:a.Limit]
			}
			steps, err := s.App.NextSteps(ctx)
			if err != nil {
				return nil, err
			}
			failed := s.selectNextSteps(steps, app.QuickWinFilter{Set: a.Set}, "", 0, true).Errors
			if len(failed) == 0 {
				return actionHandoffs(out, a.Brief), nil
			}
			return besideList{actionHandoffs(out, a.Brief), map[string]any{"analysis_errors": failed}}, nil
		})
	addTool(srv, "mark_fix", "Record work submitted for a fix: the commit or pull request, as proposed (open PR) or accepted (merged). The fix then leaves the open actions; a later fix for the same page stays open and shows this attempt and its outcome. Or dismiss a fix the team won't do: state dismissed, no url, and the reason as the note. The analyst is told every dismissal's reason and stops proposing fixes that rest on it.",
		func(ctx context.Context, a struct {
			Run   string `json:"run" jsonschema:"the run the fix is from (run_id on a quick win)"`
			Rank  int    `json:"rank" jsonschema:"the fix's rank in that run"`
			URL   string `json:"url,omitempty" jsonschema:"the commit or pull request URL; not needed to dismiss"`
			State string `json:"state,omitempty" jsonschema:"proposed (default), accepted or dismissed"`
			Note  string `json:"note,omitempty" jsonschema:"what was done, briefly; for a dismissal, why (required)"`
		}) (any, error) {
			at, err := s.App.MarkFix(ctx, a.Run, a.Rank, a.URL, a.State, a.Note, "mcp")
			if err != nil {
				return nil, err
			}
			return briefAttempts([]app.Attempt{at})[0], nil
		})
	addTool(srv, "set_attempt_state", "Move work submitted for a fix on: accepted when its pull request merges, withdrawn when it's closed (which reopens the fix). Withdrawing a dismissal undoes it.",
		func(ctx context.Context, a struct {
			ID    int64  `json:"id" jsonschema:"the attempt id, from mark_fix or list_attempts"`
			State string `json:"state" jsonschema:"proposed, accepted, withdrawn or dismissed"`
		}) (any, error) {
			if err := s.App.SetAttemptState(ctx, a.ID, a.State); err != nil {
				return nil, err
			}
			return map[string]any{"id": a.ID, "state": a.State}, nil
		})
	addTool(srv, "list_attempts", "Work submitted for fixes, newest first, with each one's outcome: for the prompts the fix meant to flip, how often they were named and recommended in its run against the newest run since, and whether its target page turned up. An outcome whose status is needs regrade lists in regrade_runs the runs to re-grade (the set has only run since under another grading method); the regrade tool with its set and regrade_since_days covers them.",
		func(ctx context.Context, a struct {
			Set string `json:"set,omitempty" jsonschema:"only this prompt set"`
		}) (any, error) {
			at, err := s.App.Attempts(ctx, a.Set)
			return briefAttempts(at), err
		})
	addTool(srv, "get_citations", "Every URL linked from an answer across runs, most cited first, with the answers scanned as the denominator.",
		func(ctx context.Context, a struct {
			URLContains string `json:"url_contains,omitempty" jsonschema:"case-insensitive substring of the URL, e.g. a docs host"`
			LastDays    int    `json:"last_days,omitempty" jsonschema:"only runs from the last N days"`
			Set         string `json:"set,omitempty" jsonschema:"only runs over this prompt set"`
		}) (any, error) {
			c, err := s.App.Citations(ctx, app.CitationQuery{URLContains: a.URLContains, LastDays: a.LastDays, SetID: a.Set})
			return c, err
		})
	addTool(srv, "search_probe", "Run one query through the web search tool the answerers use and return the result exactly as the model sees it. Costs a few cents. With [exa] configured, engine exa runs it on Exa's neutral index instead (links, exa-search as the model, under a cent), and engine both runs the two and adds overlap, only_claude and only_exa: the URLs they share and the ones only one returned, so a result reads as the assistant's or the web's.",
		func(ctx context.Context, a probe.Input) (any, error) {
			res, err := s.probe(ctx, a)
			return res, err
		})
	addTool(srv, "get_rankings", "The brand's daily share-of-voice rank and rates in each prompt set, with rolling averages and 95% bands, and day-over-day change with its margin (within_noise when it could be chance).",
		func(ctx context.Context, a struct {
			Set      string `json:"set,omitempty" jsonschema:"only this prompt set"`
			LastDays int    `json:"last_days,omitempty" jsonschema:"default 30"`
			Window   int    `json:"window,omitempty" jsonschema:"rolling window in days: 7 (default), 14 or 28"`
		}) (any, error) {
			if a.LastDays <= 0 {
				a.LastDays = 30
			}
			rk, err := s.App.Rankings(ctx, a.LastDays, a.Window, a.Set)
			if err != nil {
				return nil, err
			}
			return rankingsDigest(rk), nil
		})
	addTool(srv, "get_citation_stability", "How much the hosts a prompt set's answers cite move from run to run: Bray–Curtis similarity (×100) of consecutive runs' host citation shares and Jaccard overlap (×100) of the hosts cited, with a headline score once there are 3 transitions of the same method, and the hosts new and gone in the newest. From stored answers; costs nothing.",
		func(ctx context.Context, a struct {
			Set      string `json:"set" jsonschema:"prompt set id, from list_prompt_sets"`
			Track    string `json:"track,omitempty" jsonschema:"search (default) or recall"`
			LastDays int    `json:"last_days,omitempty" jsonschema:"default 30"`
		}) (any, error) {
			st, ok := s.App.Set(a.Set)
			if !ok {
				return nil, fmt.Errorf("no prompt set %q", a.Set)
			}
			if a.LastDays <= 0 {
				a.LastDays = 30
			}
			if a.Track != "recall" {
				a.Track = "search"
			}
			cs, err := s.App.CitationStability(ctx, st, a.Track, a.LastDays)
			if err != nil {
				return nil, err
			}
			return stabilityDigest(cs), nil
		})
	addTool(srv, "suggest_prompts", "Draft buyer questions that earn measurement cost, with buyer, learning, decision and repeat-value notes, as a TOML snippet in the set file's format, with repeats of existing prompts dropped. Costs one analyst model call (nothing on the claude-code provider). Writes nothing: paste the snippet into the set's file by pull request.",
		func(ctx context.Context, a struct {
			Set string `json:"set" jsonschema:"prompt set id, from list_prompt_sets"`
			N   int    `json:"n,omitempty" jsonschema:"how many prompts (default 10, max 50)"`
		}) (any, error) {
			if a.N <= 0 {
				a.N = 10
			}
			return s.App.SuggestPrompts(ctx, a.Set, a.N)
		})
	addTool(srv, "suggest_competitors", "Find products worth adding to [[competitors]]: hosts a neutral web index (Exa) finds similar to each configured competitor's home page and the brand's own, cross-referenced with the share-of-voice board (on_board: answers naming it on the latest runs) and, with [typesafe] on, Jev's probability that each is a product in the category (in_category). Returns the top 10 with a reason each and a TOML snippet to paste. Needs [exa]; costs one Exa find-similar per configured competitor plus one for the brand (about $0.004 each) and a fraction of a cent of Jev. Writes nothing to the config: dismiss a host with POST /api/competitors/suggestions/{host}/dismiss.",
		func(ctx context.Context, _ none) (any, error) {
			return s.suggestCompetitors(ctx)
		})
	addTool(srv, "review_prompts", "Review a prompt set's prompts from the answers they got over the last 30, 60 or 90 days: flat (never varied), noisy (moves more than sampling explains), off target (answered about other products), duplicates, rewritten by the assistants' searches, and branded leaks, each with its numbers and a one-line suggestion, plus gaps (competitors, products and search topics no prompt asks about) with prompts to paste as TOML. Free: computed from stored answers. With suggest=true it also asks the analyst for rewrites, retirements and additions as a diff against the set's file, which costs one analyst model call (nothing on the claude-code provider). With [typesafe] configured, jev lists Jev's stored judgments of the prompts' wording (duplicates, off target, intent, branded), made by cheapseats check, review -jev or after a run; this tool asks nothing. Writes nothing.",
		func(ctx context.Context, a struct {
			Set     string `json:"set" jsonschema:"prompt set id, from list_prompt_sets"`
			Days    int    `json:"days,omitempty" jsonschema:"window in days: 30 (default), 60 or 90"`
			Suggest bool   `json:"suggest,omitempty" jsonschema:"also propose edits to the set file (one analyst call)"`
		}) (any, error) {
			if a.Days == 0 {
				a.Days = app.ReviewWindows[0]
			}
			rev, err := s.App.Review(ctx, a.Set, a.Days)
			if err != nil {
				return nil, err
			}
			s.App.MarkReviewed(ctx, rev.SetID)
			if !a.Suggest {
				return reviewDigest(rev, nil), nil
			}
			e, err := s.App.SuggestReview(ctx, rev)
			if err != nil {
				return nil, err
			}
			return reviewDigest(rev, &e), nil
		})
	addTool(srv, "regrade", "Grade a set's (or one run's) stored answers again with today's grader prompt and model, brand and competitors, so older runs join the current method's trend; answers are not asked again, and runs already graded that way are skipped unless force is true (for a fix to the grading code itself; forced_runs, forced_answers and forced_usd say what it adds). dry_run (the default) only says how many answers it would grade and the estimated cost. With dry_run false it costs one grader call per answer (nothing on the claude-code provider), within the daily budget, and runs in the background.",
		func(ctx context.Context, a struct {
			Target    string `json:"target" jsonschema:"prompt set id, or a run id"`
			SinceDays *int   `json:"since_days,omitempty" jsonschema:"for a set, only runs from the last N days (default 28; 0 for all)"`
			DryRun    *bool  `json:"dry_run,omitempty" jsonschema:"only plan it, grading nothing (default true)"`
			Force     bool   `json:"force,omitempty" jsonschema:"also re-grade runs already graded with today's inputs, after a fix to the grading code (default false)"`
		}) (any, error) {
			since := 28
			if a.SinceDays != nil {
				since = *a.SinceDays
			}
			if since < 0 {
				return nil, fmt.Errorf("since_days %d: want 0 (all) or more", since)
			}
			if a.DryRun == nil || *a.DryRun {
				p, err := s.App.PlanRegrade(ctx, a.Target, since, a.Force)
				return regradeDigest(p, false), err
			}
			p, err := s.App.StartRegrade(a.Target, since, a.Force)
			if err != nil {
				return nil, err
			}
			return regradeDigest(p, len(p.Runs) > 0), nil
		})
	addTool(srv, "cancel_run", "Stop an in-progress run. Finished answers are kept; retry from the web UI to finish it.",
		func(ctx context.Context, a runArg) (any, error) {
			return map[string]bool{"cancelled": s.App.Runs.Cancel(a.Run)}, nil
		})
	addTool(srv, "get_search_queries", "The web searches the assistants ran in a run's search-track answers: per prompt how many answers searched and with what queries, the top queries with whether their results reached the brand's sites or named competitors, names the assistants added to searches the prompt didn't have, the words they add, and research leads needing buyer, decision and intent review before proposing a prompt. From stored answers; costs nothing.",
		func(ctx context.Context, a struct {
			Run   string `json:"run" jsonschema:"run id"`
			Pool  bool   `json:"pool,omitempty" jsonschema:"also read the set's earlier finished runs asked and graded the same way (up to 7 runs)"`
			Limit int    `json:"limit,omitempty" jsonschema:"rows per list (default 15)"`
		}) (any, error) {
			d, err := s.searchData(ctx, a.Run, a.Pool)
			if err != nil {
				return nil, err
			}
			return searchDigest(d, a.Limit), nil
		})
	addTool(srv, "audit_sites", "Audit the brand's sites for AI crawlers, with plain HTTP and no model calls (free, unless [exa] contents_fallback is on: then a page that is empty without JavaScript, or behind a bot challenge, is read once a day through Exa's contents API, priced per page under [pricing.exa-contents], and its finding carries via: exa): robots.txt and firewall blocks per AI crawler, llms.txt, the sitemap, and each page's title, description, H1, lang, noindex, canonical, JSON-LD, Open Graph and whether it renders without JavaScript. Returns the errors and warnings with a fix for each.",
		func(ctx context.Context, a struct {
			Site   string `json:"site,omitempty" jsonschema:"audit only this one of the brand's sites (as listed in its config); default all of them"`
			Cached bool   `json:"cached,omitempty" jsonschema:"return the latest kept audit instead of running one"`
			Info   bool   `json:"info,omitempty" jsonschema:"include info findings (often a choice, like blocking training crawlers)"`
		}) (any, error) {
			var res *audit.Result
			if a.Cached {
				latest, err := s.App.LatestAudit(ctx)
				if err != nil {
					return nil, err
				}
				if latest == nil {
					return nil, errors.New("no audit yet; call again without cached")
				}
				res = latest
			} else {
				var sites []string
				if a.Site != "" {
					// Only the brand's own sites: anything else is for the CLI, run by someone at
					// the machine, not a request that can make this server fetch any address.
					if !s.App.IsBrandSite(a.Site) {
						return nil, fmt.Errorf("%q isn't one of the brand's sites (%s); audit other sites with cheapseats audit on the command line", a.Site, strings.Join(s.App.AuditSites(), ", "))
					}
					sites = []string{a.Site}
				}
				r, err := s.App.Audit(ctx, sites, audit.Options{})
				if err != nil {
					return nil, err
				}
				res = &r
			}
			return auditDigest(res, a.Info), nil
		})
	addTool(srv, "check_citations", "For a run, fetch the third-party pages its answers cited most (plain HTTP; each page at most once a day) and say which name the brand and which competitors. Free by default; with [exa] contents_fallback on, a page that is empty without JavaScript, or behind a bot challenge, is read through Exa's contents API instead, priced per page under [pricing.exa-contents], and carries via: exa. Pages naming competitors but not the brand are outreach targets.",
		func(ctx context.Context, a struct {
			Run    string `json:"run" jsonschema:"run id, from list_runs"`
			Limit  int    `json:"limit,omitempty" jsonschema:"how many of the most-cited pages (default 25, max 100)"`
			Cached bool   `json:"cached,omitempty" jsonschema:"return the run's latest check instead of running one"`
		}) (any, error) {
			if a.Cached {
				c, err := s.App.CitationCheck(ctx, a.Run)
				if err == nil && c == nil {
					err = fmt.Errorf("run %s hasn't been checked; call again without cached", a.Run)
				}
				return c, err
			}
			c, err := s.App.CheckCitations(ctx, a.Run, a.Limit, audit.CiteOptions{})
			return c, err
		})
	addTool(srv, "get_crawlers", "AI crawler fetches, AI-referred visits and all people's visits (visits, the AI referrals among them; 0 when the logs counted none) per page of the brand's, from imported access logs (by user agent only, unverified), beside how often the latest runs cited and surfaced each page. Flags pages crawled but never cited, and cited but never fetched by an assistant or AI search crawler.",
		func(ctx context.Context, a struct {
			LastDays int    `json:"last_days,omitempty" jsonschema:"default 30"`
			Flag     string `json:"flag,omitempty" jsonschema:"only pages flagged crawled-not-cited or cited-not-crawled"`
			Limit    int    `json:"limit,omitempty" jsonschema:"at most this many pages (default 30)"`
		}) (any, error) {
			rep, err := s.App.Crawlers(ctx, a.LastDays)
			if err != nil {
				return nil, err
			}
			if a.Limit <= 0 {
				a.Limit = 30
			}
			pages := []crawl.PageRow{}
			for _, p := range rep.Pages {
				if (a.Flag == "" || p.Flag == a.Flag) && len(pages) < a.Limit {
					pages = append(pages, p)
				}
			}
			rep.Pages = pages
			return rep, nil
		})

	// share_run builds its link from the request's host when no public URL is configured, and
	// the SDK passes tools the request's headers but not its Host: hand both over as headers.
	mcp.AddTool(srv, &mcp.Tool{Name: "share_run", Description: "Make a read-only link to a run's report for someone without a sign-in: headline rates with intervals, share of voice, the strongest and weakest prompts, themes and the top fixes' titles and targets (never Claude Code prompts, config or other runs). Give run, or set for its newest finished run. Returns the URL, shown only this once; revoke it from the run page or DELETE /api/shares/{id}."},
		func(ctx context.Context, req *mcp.CallToolRequest, a struct {
			Run            string `json:"run,omitempty" jsonschema:"run id, from list_runs"`
			Set            string `json:"set,omitempty" jsonschema:"instead of run: share this prompt set's newest finished run"`
			Days           int    `json:"days,omitempty" jsonschema:"how long the link works (default 30, max 365)"`
			IncludeAnswers bool   `json:"include_answers,omitempty" jsonschema:"also show the answers' text for the prompts the report highlights"`
		}) (*mcp.CallToolResult, any, error) {
			id := a.Run
			if id == "" && a.Set != "" {
				var err error
				if id, err = s.App.LatestFinishedRun(ctx, a.Set); err != nil {
					return nil, nil, err
				}
			}
			if id == "" {
				return nil, nil, errors.New("give run or set")
			}
			sh, token, err := s.App.CreateShare(ctx, id, a.Days, a.IncludeAnswers, "mcp")
			if err != nil {
				return nil, nil, err
			}
			var host, scheme string
			if req != nil && req.Extra != nil && req.Extra.Header != nil {
				host, scheme = req.Extra.Header.Get(hostHeader), req.Extra.Header.Get(schemeHeader)
			}
			return nil, s.newShare(sh, s.linkBase(host, scheme), token), nil
		})
	addPrompts(srv)

	withHost := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set(hostHeader, requestHost(r))
			r.Header.Set(schemeHeader, requestScheme(r))
			h.ServeHTTP(w, r)
		})
	}
	h := withHost(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	if len(s.hosts) == 0 {
		return h // the SDK's own guard: loopback requests must name a loopback host
	}
	// A proxy on the same machine (exe.dev, a sidecar) delivers requests on loopback under the
	// public host name, which the SDK's guard refuses. Do the same check here, also allowing the
	// listed hosts, so a DNS-rebinding page under any other name is still refused.
	h = withHost(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rebound(r) {
			http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q (add it to CHEAPSEATS_HOSTS if a proxy serves Cheap Seats under it)", r.Host), http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// regradeDigest is a re-grade plan with its runs cut to the first 20, and whether it started.
func regradeDigest(p run.RegradePlan, started bool) any {
	more := len(p.Runs) - len(head(p.Runs, 20))
	p.Runs = head(p.Runs, 20)
	return struct {
		run.RegradePlan
		MoreRuns int  `json:"more_runs,omitempty"`
		Started  bool `json:"started"`
	}{p, more, started}
}

// briefAttempts drops the Claude Code prompt from each attempt's copy of its fix: the caller has
// the fix already, and the prompt is most of its size.
func briefAttempts(at []app.Attempt) []app.Attempt {
	out := make([]app.Attempt, len(at))
	for i, a := range at {
		a.Fix.ClaudePrompt = ""
		out[i] = a
	}
	return out
}

// hostOnly is a Host header without its port, lowercased.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		hostport = h
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

// loopback reports whether a host or host:port is a loopback address or localhost.
func loopback(hostport string) bool {
	h := hostOnly(hostport)
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// addTool registers a tool whose input schema comes from In's json and jsonschema tags.
// RunWait is what wait_for_run returns: the run's state when it finished or when the wait ran
// out. Rates and Rank are set only once it has finished; Progress only while it hasn't.
type RunWait struct {
	ID            string        `json:"id"`
	SetID         string        `json:"set_id"`
	Name          string        `json:"name"`
	Status        run.Status    `json:"status"`
	Finished      bool          `json:"finished"`
	TimedOut      bool          `json:"timed_out"`
	WaitedSeconds int           `json:"waited_seconds"`
	Error         string        `json:"error,omitempty"`
	Note          string        `json:"note,omitempty"`
	Progress      *app.Progress `json:"progress,omitempty"`
	Rates         *report.Rates `json:"rates,omitempty"`
	Rank          int           `json:"rank,omitempty"`
}

// waitPoll is how often wait_for_run looks again; a test shortens it.
var waitPoll = 2 * time.Second

// waitForRun blocks until the run is finished (its status is terminal, this process is no longer
// running it, and no fix list is being built for it) or timeout seconds pass, whichever is
// first, and returns where it stands. The client going away ends the wait.
func (s *Server) waitForRun(ctx context.Context, id string, timeout int) (*RunWait, error) {
	if timeout <= 0 {
		timeout = 90
	}
	if timeout > 600 {
		timeout = 600
	}
	if _, err := s.App.Runs.Get(ctx, id); err != nil {
		return nil, fmt.Errorf("no run %q", id)
	}
	start := time.Now()
	deadline := start.Add(time.Duration(timeout) * time.Second)
	for {
		r, err := s.App.Runs.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out := &RunWait{ID: r.ID, SetID: r.SetID, Name: r.Name, Status: r.Status, WaitedSeconds: int(time.Since(start).Seconds())}
		if r.Error != nil {
			out.Error = *r.Error
		}
		inFlight := r.Status.InFlight() || s.App.Runs.IsActive(id)
		if !inFlight {
			if p := s.App.RunProgress(ctx, id); p != nil {
				inFlight = true
				out.Progress = p
			}
		}
		if !inFlight {
			out.Finished = true
			res, _ := s.App.Runs.Results(ctx, id)
			sum := s.App.Summary(r, res)
			rates := sum.Overall
			out.Rates, out.Rank = &rates, sum.BrandRank()
			return out, nil
		}
		if out.Progress == nil {
			out.Progress = s.App.RunProgress(ctx, id)
		}
		if r.Status == run.Queued && !s.App.Runs.IsActive(id) {
			out.Note = "queued, and nothing is running it: a run left from a restart; retry it from the web UI"
		}
		if left := time.Until(deadline); left <= 0 {
			out.TimedOut = true
			return out, nil
		} else if left < waitPoll {
			if err := sleepCtx(ctx, left); err != nil {
				return nil, err
			}
			continue
		}
		if err := sleepCtx(ctx, waitPoll); err != nil {
			return nil, err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func addTool[In any](srv *mcp.Server, name, desc string, h func(context.Context, In) (any, error)) {
	mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc}, wrap(h))
}

// wrap adapts a plain handler to the SDK's: an error becomes a tool error the client can read.
// A result that isn't a JSON object (a list) goes back as text only: the SDK would put it in
// structuredContent, which clients on the 2025-06-18 spec (Claude Code among them) require to be
// an object, and they reject the whole result.
func wrap[In any](h func(context.Context, In) (any, error)) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := h(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		if out == nil {
			return nil, nil, errors.New("no result")
		}
		var beside []mcp.Content
		if bl, ok := out.(besideList); ok {
			out = bl.list
			b, err := json.Marshal(bl.beside)
			if err != nil {
				return nil, nil, err
			}
			beside = append(beside, &mcp.TextContent{Text: string(b)})
		}
		b, err := json.Marshal(out)
		if err != nil {
			return nil, nil, err
		}
		if len(b) == 0 || b[0] != '{' {
			return &mcp.CallToolResult{Content: append([]mcp.Content{&mcp.TextContent{Text: string(b)}}, beside...)}, nil, nil
		}
		return nil, out, nil
	}
}

// besideList is a list result with an object beside it, in a second text block, so the list
// keeps the shape clients already read. Only a list takes one.
type besideList struct {
	list   any
	beside any
}

// runDigest is get_run's answer: small enough to read in one go.
func runDigest(d *RunData) any {
	type prompt struct {
		N           int            `json:"n"`
		Prompt      string         `json:"prompt"`
		Intent      string         `json:"intent,omitempty"`
		Recommended string         `json:"recommended,omitempty"` // not scored for info prompts
		Named       string         `json:"named"`
		TopPicks    []report.Count `json:"top_picks"`
		FirstParty  bool           `json:"first_party_page_surfaced"`
		Branded     bool           `json:"branded,omitempty"` // names the brand: left out of unbranded
		// The brand's own pages a neutral index (Exa) finds for the prompt, each "url (status)",
		// present only when the run was coverage-checked; empty means none found.
		OwnPages []string `json:"own_pages,omitempty"`
		Results  []string `json:"results"` // "id answerer/track: verdict"
	}
	sum := d.Summary
	prompts := []prompt{}
	for _, p := range sum.Prompts {
		row := prompt{N: p.Idx + 1, Prompt: p.Prompt, TopPicks: p.TopPicks, FirstParty: p.FirstHand, Branded: p.Branded,
			Named: fmt.Sprintf("%d/%d", p.Rates.Visible.Hits, p.Rates.Visible.N)}
		if p.Info {
			row.Intent = "info"
		} else {
			row.Recommended = fmt.Sprintf("%d/%d", p.Rates.Recommended.Hits, p.Rates.Recommended.N)
		}
		if c, ok := d.Coverage[p.Idx]; ok {
			row.OwnPages = []string{}
			for _, pg := range c.Pages {
				row.OwnPages = append(row.OwnPages, pg.URL+" ("+pg.Status+")")
			}
		}
		for _, r := range d.Results {
			if r.PromptIdx != p.Idx {
				continue
			}
			v := string(r.Status)
			if r.Grade != nil {
				v = string(r.Grade.Verdict)
			}
			row.Results = append(row.Results, fmt.Sprintf("%d %s/%s: %s", r.ID, r.Answerer, r.Track, v))
		}
		prompts = append(prompts, row)
	}
	voice, pages := head(sum.Voice, 10), head(sum.Pages, 15)
	uncited := []string{}
	for _, p := range head(sum.Uncited, 10) {
		uncited = append(uncited, p.URL)
	}
	// Each fix's Claude prompt is most of a fix list's size: get_quick_wins has them.
	fixes := []analysis.Fix{}
	for _, f := range d.Fixes {
		f.ClaudePrompt = ""
		fixes = append(fixes, f)
	}
	var neutral []app.BaselineHost
	if d.Baseline != nil {
		neutral = head(d.Baseline.Hosts, 10)
	}
	// Prospects: the url, whom it names, and why it's listed; /api/runs/{id} has the rows.
	type prospect struct {
		URL   string   `json:"url"`
		Names []string `json:"names"`
		Why   string   `json:"why"`
	}
	var prospects []prospect
	for _, p := range head(d.Prospects, 10) {
		names := append([]string{}, p.Competitors...)
		if p.NamesBrand {
			names = append([]string{"the brand"}, names...)
		}
		prospects = append(prospects, prospect{p.URL, names, p.Why()})
	}
	// The snapshot is everything that shaped the run, which /api/runs/{id} has in full.
	r := *d.Run
	r.Snapshot = run.Snapshot{}
	// Jev's second opinion on the grades, with the disagreements cut to ten.
	var second *app.Agreement
	if d.SecondOpinion != nil {
		ag := *d.SecondOpinion
		ag.Disagreements = head(ag.Disagreements, 10)
		second = &ag
	}
	// The pages behind each objection and praise, cut to the top two; /api/runs/{id} has three.
	const behindRows = 2
	objections := append([]report.Objection{}, head(sum.Objections, 10)...)
	for i := range objections {
		objections[i].Sources = head(objections[i].Sources, behindRows)
	}
	op := d.Opinion
	for _, cs := range []*[]opinion.Count{&op.Sentiment.Praise, &op.Sentiment.Objections} {
		*cs = append([]opinion.Count{}, *cs...)
		for i, c := range *cs {
			if c.Behind != nil {
				b := *c.Behind
				b.Sources = head(b.Sources, behindRows)
				(*cs)[i].Behind = &b
			}
		}
	}
	return struct {
		Run     *run.Run       `json:"run"`
		Overall report.Rates   `json:"overall"`
		Slices  []report.Slice `json:"slices"`
		// Overall without the prompts that name the brand: the headline.
		Unbranded      report.Rates        `json:"unbranded"`
		BrandedPrompts int                 `json:"branded_prompts"`
		Rank           int                 `json:"share_of_voice_rank"`
		Voice          []report.Voice      `json:"share_of_voice"`
		HeadToHead     []report.HeadToHead `json:"head_to_head"`
		Objections     []report.Objection  `json:"top_objections"`
		Classes        []report.ClassUse   `json:"source_classes"`
		PageTypes      []report.TypeUse    `json:"page_types"`
		Hosts          []report.HostUse    `json:"top_hosts"`
		Outreach       []report.Outreach   `json:"outreach"`
		Prospects      []prospect          `json:"prospects,omitempty"`
		// Cited pages that started or stopped naming the brand or a competitor since the set's
		// previous citation check, when both runs were checked; /api/runs/{id} has them all.
		CitedChanges   []audit.PageFlip        `json:"cited_page_changes,omitempty"`
		Uncited        []string                `json:"found_not_cited"`
		Pages          []report.Page           `json:"top_pages"`
		Fixes          []analysis.Fix          `json:"fixes"` // compatibility: verified actions, without implementation prompts
		Investigations []app.NextInvestigation `json:"investigations"`
		MarketFindings []app.NextFinding       `json:"market_findings"`
		// The run's own analysis error, with which analysis the lists above are (shown), when
		// its latest build failed.
		AnalysisError *app.AnalysisError `json:"analysis_error,omitempty"`
		Insights      string             `json:"insights"`
		Opinion       opinion.Report     `json:"opinion"`
		SecondOpinion *app.Agreement     `json:"second_opinion,omitempty"`
		Misstatements *app.FactReport    `json:"misstatements,omitempty"` // the fact check, when the run was checked
		Prompts       []prompt           `json:"prompts"`
		// The neutral retrieval baseline's host table, present only when [exa] searched the run's
		// prompts: per host, the prompts a neutral index ranks it for against the prompts the
		// assistants found it for, and the reading (content, query, both).
		Neutral []app.BaselineHost `json:"neutral_retrieval,omitempty"`

		Opportunities []report.Opportunity `json:"opportunities"`
	}{&r, sum.Overall, sum.Slices, sum.Unbranded, sum.BrandedPrompts, sum.BrandRank(), voice, head(sum.HeadToHead, 10), objections,
		sum.Classes, sum.PageTypes, head(sum.Hosts, 10), head(sum.Outreach, 10), prospects, head(d.CitedChanges, 10), uncited, pages, fixes, d.Investigations, d.MarketFindings, d.AnalysisError, d.Insights, op, second, d.Misstatements, prompts, neutral, head(sum.Opportunities, 10)}
}

// auditDigest is audit_sites' answer: each site in a line or two, and the findings without info
// unless asked.
func auditDigest(r *audit.Result, info bool) any {
	type site struct {
		Site        string   `json:"site"`
		Robots      int      `json:"robots_status"`
		LLMs        int      `json:"llms_status,omitempty"`
		SitemapURLs int      `json:"sitemap_urls"`
		Newest      string   `json:"sitemap_newest,omitempty"`
		Pages       int      `json:"pages_checked"`
		Blocked     []string `json:"blocked"` // "GPTBot (training): robots.txt"
	}
	sites := []site{}
	for _, x := range r.Sites {
		st := site{Site: x.Site, Robots: x.RobotsStatus, LLMs: x.LLMsStatus, SitemapURLs: x.SitemapURLs, Newest: x.Newest, Pages: len(x.Pages), Blocked: []string{}}
		for _, b := range x.Bots {
			if !b.Allowed {
				st.Blocked = append(st.Blocked, fmt.Sprintf("%s (%s): robots.txt", b.Bot, b.Class))
			}
			if b.Blocked {
				st.Blocked = append(st.Blocked, fmt.Sprintf("%s (%s): firewall %d", b.Bot, b.Class, b.Status))
			}
		}
		sites = append(sites, st)
	}
	findings := []audit.Finding{}
	for _, f := range r.Findings {
		if info || f.Severity != audit.Info {
			findings = append(findings, f)
		}
	}
	return struct {
		At       string          `json:"at"`
		Errors   int             `json:"errors"`
		Warnings int             `json:"warnings"`
		Info     int             `json:"info"`
		Sites    []site          `json:"sites"`
		Findings []audit.Finding `json:"findings"`
	}{r.At, r.Errors(), r.Warnings(), r.Infos(), sites, findings}
}

// boardSize is how many products of a day's share-of-voice board get_rankings keeps.
const boardSize = 5

// boardRow is a product on a day's board, in get_rankings.
type boardRow struct {
	Name         string  `json:"name"`
	IsBrand      bool    `json:"is_brand,omitempty"`
	NamedRate    float64 `json:"named_rate"`
	ShareOfVoice float64 `json:"share_of_voice"`
	AvgPosition  float64 `json:"avg_position,omitempty"`
}

// rankingDigest is a ranking with each day's board cut to its leaders and the brand, in a few
// fields: every product with every measure, every day, runs to hundreds of kilobytes, more than an
// MCP client takes. /api/rankings has it all.
type rankingDigest struct {
	app.Ranking
	Days []dayDigest `json:"days"`
}

type dayDigest struct {
	app.Day
	Board []boardRow `json:"board"`
}

func rankingsDigest(rk []app.Ranking) []rankingDigest {
	out := make([]rankingDigest, len(rk))
	for i, r := range rk {
		out[i] = rankingDigest{Ranking: r, Days: []dayDigest{}}
		for _, d := range r.Days {
			dd := dayDigest{Day: d, Board: []boardRow{}}
			for k, v := range d.Board {
				if k < boardSize || v.IsBrand {
					dd.Board = append(dd.Board, boardRow{v.Name, v.IsBrand, v.NamedRate, v.ShareOfVoice, v.AvgPosition})
				}
			}
			out[i].Days = append(out[i].Days, dd)
		}
	}
	return out
}

// stabilityDigest is get_citation_stability's answer: the score, every transition's two numbers,
// and the hosts new and gone in the newest only, ten of each. /api/sets/{id}/citation-stability
// has every transition's hosts.
func stabilityDigest(cs app.CitationStability) any {
	type step struct {
		From           string  `json:"from"`
		To             string  `json:"to"`
		Similarity     float64 `json:"similarity"`
		Overlap        float64 `json:"overlap"`
		New            int     `json:"new"`
		Gone           int     `json:"gone"`
		PromptsChanged bool    `json:"prompts_changed,omitempty"`
	}
	round := func(x float64) float64 { return math.Round(x*10) / 10 }
	out := struct {
		Set         string   `json:"set"`
		Track       string   `json:"track"`
		LastDays    int      `json:"last_days"`
		Runs        int      `json:"runs"`
		Score       *float64 `json:"score,omitempty"`
		Overlap     *float64 `json:"overlap,omitempty"`
		Note        string   `json:"note,omitempty"`
		NewestNew   []string `json:"newest_new"`
		NewestGone  []string `json:"newest_gone"`
		Transitions []step   `json:"transitions"` // newest first
	}{Set: cs.SetID, Track: cs.Track, LastDays: cs.LastDays, Runs: cs.Runs, Note: cs.Note, NewestNew: []string{}, NewestGone: []string{}, Transitions: []step{}}
	if cs.Score != nil {
		sc, ov := round(*cs.Score), round(*cs.MeanOverlap)
		out.Score, out.Overlap = &sc, &ov
	}
	for i, t := range cs.Transitions {
		if i == 0 {
			out.NewestNew, out.NewestGone = head(t.New, 10), head(t.Gone, 10)
		}
		out.Transitions = append(out.Transitions, step{t.From, t.To, round(t.Similarity), round(t.Overlap), len(t.New), len(t.Gone), t.PromptsChanged})
	}
	return out
}
