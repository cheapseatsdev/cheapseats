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
	addTool(srv, "start_run", "Start a run over a prompt set. Costs money: every prompt is answered by each configured assistant and graded. Returns the run id; poll get_run.",
		func(ctx context.Context, a setArg) (any, error) {
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
	addTool(srv, "get_run", "One run: rates per answerer and track, share of voice, top pages, the fix list, and one line per prompt. Use get_result for a full answer.",
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
	addTool(srv, "get_rankings", "The brand's daily share-of-voice rank and rates in each prompt set, with day-over-day change.",
		func(ctx context.Context, a struct {
			Set      string `json:"set,omitempty" jsonschema:"only this prompt set"`
			LastDays int    `json:"last_days,omitempty" jsonschema:"default 30"`
		}) (any, error) {
			if a.LastDays <= 0 {
				a.LastDays = 30
			}
			rk, err := s.App.Rankings(ctx, a.LastDays, a.Set)
			return rk, err
		})
	addTool(srv, "cancel_run", "Stop an in-progress run. Finished answers are kept; retry from the web UI to finish it.",
		func(ctx context.Context, a runArg) (any, error) {
			return map[string]bool{"cancelled": s.App.Runs.Cancel(a.Run)}, nil
		})

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	if len(s.hosts) == 0 {
		return h // the SDK's own guard: loopback requests must name a loopback host
	}
	// A proxy on the same machine (exe.dev, a sidecar) delivers requests on loopback under the
	// public host name, which the SDK's guard refuses. Do the same check here, also allowing the
	// listed hosts, so a DNS-rebinding page under any other name is still refused.
	h = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true})
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
		Results     []string       `json:"results"` // "id answerer/track: verdict"
	}
	sum := d.Summary
	prompts := []prompt{}
	for _, p := range sum.Prompts {
		row := prompt{N: p.Idx + 1, Prompt: p.Prompt, TopPicks: p.TopPicks, FirstParty: p.FirstHand,
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
	voice, pages := sum.Voice, sum.Pages
	if len(voice) > 10 {
		voice = voice[:10]
	}
	if len(pages) > 15 {
		pages = pages[:15]
	}
	fixes := d.Fixes
	if fixes == nil {
		fixes = []analysis.Fix{}
	}
	return struct {
		Run      *run.Run       `json:"run"`
		Overall  report.Rates   `json:"overall"`
		Slices   []report.Slice `json:"slices"`
		Rank     int            `json:"share_of_voice_rank"`
		Voice    []report.Voice `json:"share_of_voice"`
		Pages    []report.Page  `json:"top_pages"`
		Fixes    []analysis.Fix `json:"fixes"`
		Insights string         `json:"insights"`
		Prompts  []prompt       `json:"prompts"`
	}{d.Run, sum.Overall, sum.Slices, sum.BrandRank(), voice, pages, fixes, d.Insights, prompts}
}
