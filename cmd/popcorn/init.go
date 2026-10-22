package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/metcalfc/popcorn/internal/audit"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/scaffold"
)

const initUsage = "usage: popcorn init <url> [dir] [-name X] [-competitors a,b] [-suggest [-n 10]] [-dry-run]"

// initCmd: popcorn init <url> [dir]: a new config repo from the brand's website.
func initCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "the brand's name (default: what the site calls itself)")
	comps := fs.String("competitors", "", "competitors, comma-separated")
	sug := fs.Bool("suggest", false, "add suggested prompts to the starter sets (one analyst call; nothing on claude-code)")
	n := fs.Int("n", 10, "with -suggest, how many prompts")
	dry := fs.Bool("dry-run", false, "print what was found and would be written; write nothing")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New(initUsage)
	}

	site, err := scaffold.Discover(ctx, audit.NewFetcher(nil, 4), pos[0])
	if err != nil {
		return err
	}
	if *name != "" {
		site.Name, site.NameFrom = strings.TrimSpace(*name), "-name"
		site.Aliases = scaffold.Aliases(site.Name, site.Host)
	}
	var competitors []string
	for _, c := range strings.Split(*comps, ",") {
		if c = strings.TrimSpace(c); c != "" {
			competitors = append(competitors, c)
		}
	}
	dir := (config.Brand{Name: site.Name}).Slug() + "-popcorn-config"
	if len(pos) == 2 {
		dir = pos[1]
	}

	p := scaffold.Build(site, competitors)
	cfg, err := scaffold.Check(p.Files())
	if err != nil {
		return fmt.Errorf("the generated config doesn't pass popcorn check (try -name or -competitors): %w", err)
	}
	if *sug {
		if config.Missing(config.ClaudeCode) != "" {
			return errors.New("-suggest runs the analyst on the claude CLI, which isn't installed here")
		}
		if err := p.Suggest(ctx, cfg, *n); err != nil {
			return fmt.Errorf("suggest: %w", err)
		}
		if _, err := scaffold.Check(p.Files()); err != nil {
			return fmt.Errorf("with the suggested prompts: %w", err)
		}
	}

	fmt.Printf("%s  %s\n", site.URL, site.Host)
	fmt.Printf("  name      %s (from %s)\n", site.Name, site.NameFrom)
	fmt.Printf("  aliases   %s\n", strings.Join(site.Aliases, ", "))
	fmt.Printf("  about     %s\n", or(site.About, "(none found)"))
	guess := ""
	if p.Guessed {
		guess = " (a stand-in: no product noun in the description)"
	}
	fmt.Printf("  category  %s%s\n", p.Category, guess)
	for _, s := range site.Sites {
		fmt.Printf("  site      %-9s %s\n", s.Kind, s.URL)
	}
	found := []string{}
	if site.LLMs {
		found = append(found, "llms.txt")
	}
	if site.Sitemap > 0 {
		found = append(found, fmt.Sprintf("sitemap (%d pages)", site.Sitemap))
	}
	if len(found) > 0 {
		fmt.Printf("  read      %s\n", strings.Join(found, ", "))
	}
	if *sug {
		fmt.Printf("  suggested %d prompts, $%.3f\n", p.Suggested, p.CostUSD)
	}
	fmt.Println()

	files := p.Files()
	if *dry {
		fmt.Printf("Would write to %s (dry run, nothing written):\n", dir)
		for _, f := range files {
			fmt.Printf("\n==> %s <==\n%s", filepath.Join(dir, f.Path), f.Body)
		}
		return nil
	}
	if err := scaffold.Write(dir, files); err != nil {
		return err
	}
	fmt.Printf("Wrote %s:\n", dir)
	for _, f := range files {
		fmt.Printf("  %s\n", f.Path)
	}
	toml := filepath.Join(dir, "popcorn.toml")
	fmt.Printf(`
Next:
  1. Review the TODOs: grep -rn TODO %[1]s
  2. POPCORN_CONFIG=%[2]s popcorn check
  3. POPCORN_CONFIG=%[2]s popcorn audit          (free: what AI crawlers can read on the sites)
  4. POPCORN_CONFIG=%[2]s popcorn run discovery
     (%[4]d prompts x 3 answers on your Claude subscription, no API spend. popcorn run has no dry
     run: it asks for real, so read the prompts first.)
  5. cd %[1]s && git init && git add . && git commit -m "Popcorn config for %[3]s"
`, dir, toml, site.Name, len(p.Sets[0].Prompts))
	return nil
}

// parseInterleaved parses flags wherever they sit among the positional arguments, so
// "init example.com -dry-run" works as well as "init -dry-run example.com".
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
