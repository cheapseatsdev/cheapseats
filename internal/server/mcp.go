package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/metcalfc/popcorn/internal/analysis"
	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/audit"
	"github.com/metcalfc/popcorn/internal/crawl"
	"github.com/metcalfc/popcorn/internal/opinion"
	"github.com/metcalfc/popcorn/internal/probe"
	"github.com/metcalfc/popcorn/internal/report"
	"github.com/metcalfc/popcorn/internal/run"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpHandler serves the same reads and actions as the JSON API over MCP (Streamable HTTP), so
// Claude Code or any MCP client can start runs and read results. It sits behind the same bearer
// tokens as /api/*.
func (s *Server) mcpHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "popcorn-" + s.App.Cfg.Brand.Slug(), Title: "Popcorn: " + s.App.Cfg.Brand.Name, Version: "1"}, nil)
	type none struct{}
	type setArg struct {
		Set string `json:"set" jsonschema:"prompt set id, from list_prompt_sets"`
	}
	type runArg struct {
		Run string `json:"run" jsonschema:"run id, from list_runs or start_run"`
	}
	addTool(srv, "get_started", "Call this first. What this Popcorn tracks and where it stands, in one small result: the brand and its sites, each prompt set with its tier, last run and status, today's spend against the budget, which answerers and the grader can run here, the latest site audit's errors and warnings, open and urgent fixes, fix attempts awaiting an outcome, data_state (no_runs, running, stale or ready) and next_steps: the tool calls worth making now, with their arguments. Free.",
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
	addTool(srv, "start_run", "Start a run over a prompt set. Costs money: every prompt is answered by each configured assistant and graded. Call it with dry_run=true first: that returns the plan (answers per answerer and track, answerers skipped, estimated cost from recent runs, today's remaining budget, and whether it would be refused) and starts nothing. Without dry_run it returns the run id; poll get_run.",
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
			return map[string]string{"id": r.ID, "status": r.Status}, nil
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
				out = append(out, row{x.ID, x.Name, x.SetID, x.Status, x.CreatedAt, sum.Overall, sum.BrandRank()})
			}
			return out, nil
		})
	addTool(srv, "get_run", "One run: rates per answerer and track (and without branded prompts), share of voice with positions, head to head, objections, sources by kind and host, an outreach list of hosts cited without the brand, the brand's pages found but not cited, the fix list, net sentiment with top praise and objections, the run's themes, and one line per prompt. Use get_result for a full answer.",
		func(ctx context.Context, a runArg) (any, error) {
			d, err := s.runData(ctx, a.Run)
			if err != nil {
				return nil, err
			}
			return runDigest(d), nil
		})
	addTool(srv, "get_result", "One answer in full: the text, its grade, every search, result URL and citation.",
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
					return x, nil
				}
			}
			return nil, fmt.Errorf("run %s has no result %d", a.Run, a.Result)
		})
	addTool(srv, "get_quick_wins", "The ranked fixes from the latest run of every prompt set, fastest ranking gain first, each with a ready-to-paste prompt for Claude Code, its status (open, proposed or accepted) and the work submitted for it or earlier for the same page.",
		func(ctx context.Context, a struct {
			Gap      string `json:"gap,omitempty" jsonschema:"documentation, positioning, third-party or product"`
			Set      string `json:"set,omitempty" jsonschema:"only fixes from this prompt set"`
			Site     string `json:"site,omitempty" jsonschema:"only fixes on this site of the brand's, by url or kind (docs, blog, marketing...), or elsewhere for fixes off its sites"`
			Status   string `json:"status,omitempty" jsonschema:"open (nothing submitted yet), proposed or accepted"`
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
			if a.Brief {
				for i := range out {
					out[i].ClaudePrompt = ""
					out[i].Attempts, out[i].Earlier = briefAttempts(out[i].Attempts), briefAttempts(out[i].Earlier)
				}
			}
			return out, nil
		})
	addTool(srv, "mark_fix", "Record work submitted for a fix: the commit or pull request, as proposed (open PR) or accepted (merged). The fix then leaves the open quick wins; a later fix for the same page stays open and shows this attempt and its outcome.",
		func(ctx context.Context, a struct {
			Run   string `json:"run" jsonschema:"the run the fix is from (run_id on a quick win)"`
			Rank  int    `json:"rank" jsonschema:"the fix's rank in that run"`
			URL   string `json:"url" jsonschema:"the commit or pull request URL"`
			State string `json:"state,omitempty" jsonschema:"proposed (default) or accepted"`
			Note  string `json:"note,omitempty" jsonschema:"what was done, briefly"`
		}) (any, error) {
			at, err := s.App.MarkFix(ctx, a.Run, a.Rank, a.URL, a.State, a.Note, "mcp")
			if err != nil {
				return nil, err
			}
			return briefAttempts([]app.Attempt{at})[0], nil
		})
	addTool(srv, "set_attempt_state", "Move work submitted for a fix on: accepted when its pull request merges, withdrawn when it's closed (which reopens the fix).",
		func(ctx context.Context, a struct {
			ID    int64  `json:"id" jsonschema:"the attempt id, from mark_fix or list_attempts"`
			State string `json:"state" jsonschema:"proposed, accepted or withdrawn"`
		}) (any, error) {
			if err := s.App.SetAttemptState(ctx, a.ID, a.State); err != nil {
				return nil, err
			}
			return map[string]any{"id": a.ID, "state": a.State}, nil
		})
	addTool(srv, "list_attempts", "Work submitted for fixes, newest first, with each one's outcome: for the prompts the fix meant to flip, how often they were named and recommended in its run against the newest run since, and whether its target page turned up.",
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
	addTool(srv, "search_probe", "Run one query through the web search tool the answerers use and return the result exactly as the model sees it. Costs a few cents.",
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
	addTool(srv, "suggest_prompts", "Draft new buyer questions for a prompt set, across funnel stages, as a TOML snippet in the set file's format, with repeats of existing prompts dropped. Costs one analyst model call (nothing on the claude-code provider). Writes nothing: paste the snippet into the set's file by pull request.",
		func(ctx context.Context, a struct {
			Set string `json:"set" jsonschema:"prompt set id, from list_prompt_sets"`
			N   int    `json:"n,omitempty" jsonschema:"how many prompts (default 10, max 50)"`
		}) (any, error) {
			if a.N <= 0 {
				a.N = 10
			}
			return s.App.SuggestPrompts(ctx, a.Set, a.N)
		})
	addTool(srv, "cancel_run", "Stop an in-progress run. Finished answers are kept; retry from the web UI to finish it.",
		func(ctx context.Context, a runArg) (any, error) {
			return map[string]bool{"cancelled": s.App.Runs.Cancel(a.Run)}, nil
		})
	addTool(srv, "get_search_queries", "The web searches the assistants ran in a run's search-track answers: per prompt how many answers searched and with what queries, the top queries with whether their results reached the brand's sites or named competitors, names the assistants added to searches the prompt didn't have, the words they add, and queries worth adding as prompts (a TOML entry to paste). From stored answers; costs nothing.",
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
	addTool(srv, "audit_sites", "Audit the brand's sites for AI crawlers, free (plain HTTP, no model calls): robots.txt and firewall blocks per AI crawler, llms.txt, the sitemap, and each page's title, description, H1, lang, noindex, canonical, JSON-LD, Open Graph and whether it renders without JavaScript. Returns the errors and warnings with a fix for each.",
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
						return nil, fmt.Errorf("%q isn't one of the brand's sites (%s); audit other sites with popcorn audit on the command line", a.Site, strings.Join(s.App.AuditSites(), ", "))
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
	addTool(srv, "check_citations", "For a run, fetch the third-party pages its answers cited most (free, plain HTTP; each page at most once a day) and say which name the brand and which competitors. Pages naming competitors but not the brand are outreach targets.",
		func(ctx context.Context, a struct {
			Run    string `json:"run" jsonschema:"run id, from list_runs"`
			Limit  int    `json:"limit,omitempty" jsonschema:"how many of the most-cited pages (default 25)"`
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
	addTool(srv, "get_crawlers", "AI crawler fetches and AI-referred visits per page of the brand's, from imported access logs (by user agent only, unverified), beside how often the latest runs cited and surfaced each page. Flags pages crawled but never cited, and cited but never fetched by an assistant or AI search crawler.",
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
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && loopback(local.String()) &&
			!loopback(r.Host) && !slices.Contains(s.hosts, hostOnly(r.Host)) {
			http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q (add it to POPCORN_HOSTS if a proxy serves Popcorn under it)", r.Host), http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
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
		b, err := json.Marshal(out)
		if err != nil {
			return nil, nil, err
		}
		if len(b) == 0 || b[0] != '{' {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
		}
		return nil, out, nil
	}
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
		Results     []string       `json:"results"`           // "id answerer/track: verdict"
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
		for _, r := range d.Results {
			if r.PromptIdx != p.Idx {
				continue
			}
			v := r.Status
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
	// The snapshot is everything that shaped the run, which /api/runs/{id} has in full.
	r := *d.Run
	r.Snapshot = run.Snapshot{}
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
		Hosts          []report.HostUse    `json:"top_hosts"`
		Outreach       []report.Outreach   `json:"outreach"`
		Uncited        []string            `json:"found_not_cited"`
		Pages          []report.Page       `json:"top_pages"`
		Fixes          []analysis.Fix      `json:"fixes"` // without claude_prompt: get_quick_wins has it
		Insights       string              `json:"insights"`
		Opinion        opinion.Report      `json:"opinion"`
		Prompts        []prompt            `json:"prompts"`
	}{&r, sum.Overall, sum.Slices, sum.Unbranded, sum.BrandedPrompts, sum.BrandRank(), voice, head(sum.HeadToHead, 10), head(sum.Objections, 10),
		sum.Classes, head(sum.Hosts, 10), head(sum.Outreach, 10), uncited, pages, fixes, d.Insights, d.Opinion, prompts}
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
