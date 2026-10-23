package probe

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/exa"
	"github.com/cheapseatsdev/cheapseats/internal/report"
)

// The engines. Claude is the answerers' web search tool, what a run's answers see; Exa is a
// neutral index, what the web has. Both runs the two and says which URLs they share.
const (
	EngineClaude = "claude"
	EngineExa    = "exa"
	EngineBoth   = "both"
)

// Engines lists the engine names, the default first.
var Engines = []string{EngineClaude, EngineExa, EngineBoth}

// ExaResults is how many results an Exa probe asks for: what Claude's tool returns at most, so
// the two sides of a comparison are the same size.
const ExaResults = 10

// Compare is what engine both returns: each engine's result, and the URLs in both, only in
// Claude's and only in Exa's, compared by report.NormalizeURL (a fragment or trailing slash
// doesn't make a different page) and in the order the engine returned them.
type Compare struct {
	Query      string   `json:"query"`
	Claude     Result   `json:"claude"`
	Exa        Result   `json:"exa"`
	Overlap    []string `json:"overlap"`
	OnlyClaude []string `json:"only_claude"`
	OnlyExa    []string `json:"only_exa"`
	CostUSD    float64  `json:"cost_usd"`
}

// Probe runs the input's engine and returns a Result for claude and exa, or a Compare for both,
// for callers that only encode or print it.
func Probe(ctx context.Context, d Deps, in Input) (any, error) {
	if in.Engine == EngineBoth {
		return Both(ctx, d, in)
	}
	return Run(ctx, d, in)
}

// Both runs the query on Claude and Exa at once and compares what they return. Either engine
// failing fails the comparison: half a comparison would read as the other half's finding.
func Both(ctx context.Context, d Deps, in Input) (Compare, error) {
	if err := exaReady(d); err != nil {
		return Compare{}, err // before the Claude call, which would cost money for nothing
	}
	type out struct {
		res Result
		err error
	}
	ch := make(chan out, 1)
	go func() {
		c := in
		c.Engine = EngineClaude
		r, err := Run(ctx, d, c)
		ch <- out{r, err}
	}()
	e := in
	e.Engine = EngineExa
	exaRes, exaErr := Run(ctx, d, e)
	claude := <-ch
	if claude.err != nil {
		return Compare{}, claude.err
	}
	if exaErr != nil {
		return Compare{}, exaErr
	}
	cmp := Compare{Query: claude.res.Query, Claude: claude.res, Exa: exaRes, CostUSD: claude.res.CostUSD + exaRes.CostUSD}
	cmp.Overlap, cmp.OnlyClaude, cmp.OnlyExa = compare(claude.res.Links, exaRes.Links)
	return cmp, nil
}

// compare splits two link lists into the URLs in both, only in the first and only in the
// second, by report.NormalizeURL, each in its list's order and once each. The URL kept is the
// first spelling seen, so a comparison still links to what the engine returned.
func compare(a, b []answer.Link) (both, onlyA, onlyB []string) {
	both, onlyA, onlyB = []string{}, []string{}, []string{}
	inB := map[string]bool{}
	for _, l := range b {
		inB[report.NormalizeURL(l.URL)] = true
	}
	seen := map[string]bool{}
	for _, l := range a {
		k := report.NormalizeURL(l.URL)
		if seen[k] {
			continue
		}
		seen[k] = true
		if inB[k] {
			both = append(both, l.URL)
		} else {
			onlyA = append(onlyA, l.URL)
		}
	}
	for _, l := range b {
		k := report.NormalizeURL(l.URL)
		if seen[k] {
			continue
		}
		seen[k] = true
		onlyB = append(onlyB, l.URL)
	}
	return both, onlyA, onlyB
}

// exaReady is what the Exa engine needs before anything is called.
func exaReady(d Deps) error {
	if d.Cfg.Exa == nil {
		return errors.New("no [exa] in the config")
	}
	if d.Exa == nil {
		return errors.New("the probe was given no Exa client")
	}
	return nil
}

// runExa is the query on Exa with the allowed domains as includeDomains and the blocked ones as
// excludeDomains. It needs [exa] in the config and a client from the caller; the client itself
// wants a key (or a proxy) and a [pricing.exa-search] entry when it is budgeted.
func runExa(ctx context.Context, d Deps, in Input, q string) (Result, error) {
	if err := exaReady(d); err != nil {
		return Result{}, err
	}
	start := time.Now()
	res := Result{Query: q, Model: exa.Model, RanAt: start.UTC().Format(time.RFC3339), ToolInput: map[string]any{"query": q, "numResults": ExaResults}}
	if len(in.AllowedDomains) > 0 {
		res.ToolInput["includeDomains"] = in.AllowedDomains
	}
	if len(in.BlockedDomains) > 0 {
		res.ToolInput["excludeDomains"] = in.BlockedDomains
	}
	resp, err := d.Exa.Run(ctx, "search-probe", exa.Query{Text: q, Include: in.AllowedDomains, Exclude: in.BlockedDomains, N: ExaResults})
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		return Result{}, err
	}
	res.CostUSD = resp.CostUSD
	res.Output, _ = json.Marshal(resp.Results)
	res.Links = []answer.Link{}
	for _, r := range resp.Results {
		res.Links = append(res.Links, answer.Link{URL: r.URL, Title: r.Title})
	}
	return res, nil
}
