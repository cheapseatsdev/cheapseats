package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/llm"
	"github.com/cheapseatsdev/cheapseats/internal/probe"
)

// probeCmd is cheapseats probe [-engine claude|exa|both] [-domains a,b] [-exclude a,b] [-json]
// <query>: one query through the answerers' web search tool, Exa's index, or both compared.
func probeCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	engine := fs.String("engine", probe.EngineClaude, "claude (the answerers' web search tool), exa (needs [exa]), or both, compared")
	domains := fs.String("domains", "", "only results from these domains, comma-separated")
	exclude := fs.String("exclude", "", "never results from these domains, comma-separated")
	asJSON := fs.Bool("json", false, "the result as JSON, as the API returns it")
	fs.Parse(positionalLast(args))
	if fs.NArg() == 0 {
		return errors.New("usage: cheapseats probe [-engine claude|exa|both] [-domains a,b] [-exclude a,b] [-json] <query>")
	}
	in := probe.Input{Query: strings.Join(fs.Args(), " "), Engine: *engine, AllowedDomains: probe.Domains(*domains), BlockedDomains: probe.Domains(*exclude)}
	a, err := open()
	if err != nil {
		return err
	}
	ctx = llm.WithOperation(llm.WithAccounting(ctx, a.Runs.Budget), "search-probe")
	in.Exa = a.Exa()
	out, err := probe.Probe(ctx, a.Cfg, in)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	switch v := out.(type) {
	case probe.Result:
		printProbe(v)
	case probe.Compare:
		fmt.Printf("%d in both, %d only Claude, %d only Exa, $%.4f\n", len(v.Overlap), len(v.OnlyClaude), len(v.OnlyExa), v.CostUSD)
		for _, s := range []struct {
			name string
			urls []string
		}{{"In both", v.Overlap}, {"Only Claude", v.OnlyClaude}, {"Only Exa", v.OnlyExa}} {
			fmt.Printf("\n%s:\n", s.name)
			if len(s.urls) == 0 {
				fmt.Println("  none")
			}
			for _, u := range s.urls {
				fmt.Printf("  %s\n", u)
			}
		}
		fmt.Printf("\nClaude: ")
		printProbe(v.Claude)
		fmt.Printf("\nExa: ")
		printProbe(v.Exa)
	}
	return nil
}

func printProbe(r probe.Result) {
	fmt.Printf("%d results for %q (%s, $%.4f, %d ms)\n", len(r.Links), r.Query, r.Model, r.CostUSD, r.DurationMS)
	if r.Error != "" {
		fmt.Fprintf(os.Stderr, "  error: %s\n", r.Error)
	}
	for i, l := range r.Links {
		if l.Title != "" {
			fmt.Printf("%3d. %s\n     %s\n", i+1, l.Title, l.URL)
		} else {
			fmt.Printf("%3d. %s\n", i+1, l.URL)
		}
	}
}
