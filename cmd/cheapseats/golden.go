package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// goldenDir is where the grader's golden set lives, and where TestGolden reads it.
const goldenDir = "internal/grade/testdata/golden"

// goldenCmd is cheapseats golden export: graded answers sampled from a database snapshot into
// golden records for the grader test (docs/grading.md). It reads the snapshot and writes only
// the .jsonl files.
func goldenCmd(ctx context.Context, args []string) error {
	use := errors.New("usage: cheapseats golden export <snapshot.db> [-n 80] [-set id] [-o " + goldenDir + "]")
	if len(args) < 1 || args[0] != "export" {
		return use
	}
	fs := flag.NewFlagSet("golden export", flag.ExitOnError)
	n := fs.Int("n", 80, "how many records to add, spread over sets and verdicts")
	set := fs.String("set", "", "only this prompt set")
	out := fs.String("o", goldenDir, "the directory of .jsonl files, one per set; records already there are kept")
	fs.Parse(positionalLast(args[1:]))
	if fs.NArg() != 1 {
		return use
	}
	if _, err := os.Stat(fs.Arg(0)); err != nil {
		return err
	}
	cfg, err := config.Load(configPath())
	if err != nil {
		return err
	}
	all, err := sets.LoadAll(sets.Dir(cfg.Dir))
	if err != nil {
		return err
	}
	sum, err := run.ExportGolden(ctx, fs.Arg(0), cfg, sets.Share(all, cfg.Competitors), run.GoldenExport{N: *n, Set: *set, Dir: *out})
	if err != nil {
		return err
	}
	printGoldenSummary(sum, *out)
	return nil
}

func printGoldenSummary(sum run.GoldenSummary, dir string) {
	ids := make([]string, 0, len(sum.Added))
	total := 0
	for id, strata := range sum.Added {
		ids = append(ids, id)
		for _, n := range strata {
			total += n
		}
	}
	sort.Strings(ids)
	fmt.Printf("%d records added to %s (%d already there)\n", total, dir, sum.Kept)
	for _, id := range ids {
		var parts []string
		for _, st := range run.GoldenStrata {
			if n := sum.Added[id][st]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, st))
			}
		}
		fmt.Printf("  %s: %s\n", id, strings.Join(parts, ", "))
	}
	var pool []string
	for _, st := range run.GoldenStrata {
		pool = append(pool, fmt.Sprintf("%d %s", sum.Candidates[st], st))
	}
	fmt.Printf("sampled from %s\n", strings.Join(pool, ", "))
	if sum.Retired > 0 {
		fmt.Printf("%d answers left out for naming an alias the brand has since retired\n", sum.Retired)
	}
	if len(sum.UnknownSets) > 0 {
		fmt.Printf("sets not in today's config, left out: %s\n", strings.Join(sum.UnknownSets, ", "))
	}
	fmt.Println("Every label is the current grader's (labeled_by \"claude\"): check them and flip the wrong ones; see docs/grading.md.")
}
