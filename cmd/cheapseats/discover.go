package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/cheapseatsdev/cheapseats/internal/app"
)

// discoverCmd is cheapseats discover [-json]: find hosts similar to the configured competitors'
// and the brand's home pages on Exa, and print them as [[competitors]] TOML to paste (or JSON).
// It costs one Exa find-similar per seed and, with [typesafe], a Jev call per new host; it never
// edits the config.
func discoverCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the suggestions as JSON")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return errors.New("usage: cheapseats discover [-json]")
	}
	a, err := open()
	if err != nil {
		return err
	}
	d, err := a.DiscoverCompetitors(ctx)
	if err != nil {
		return err
	}
	ss, err := a.CompetitorSuggestions(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			app.Discovered
			Suggestions []app.Suggestion `json:"suggestions"`
		}{d, ss})
	}
	fmt.Print(app.SuggestionsTOML(ss))
	fmt.Fprintf(os.Stderr, "%d suggestions from %d hosts similar to %d home pages, $%.3f. Paste the ones you agree with into cheapseats.toml by pull request.\n", len(ss), d.Hosts, d.Seeds, d.CostUSD)
	return nil
}
