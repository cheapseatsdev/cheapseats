// cheapseats asks AI assistants the questions a brand's buyers ask and grades whether they
// recommend it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/app"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/env"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/opinion"
	"github.com/cheapseatsdev/cheapseats/internal/plan"
	"github.com/cheapseatsdev/cheapseats/internal/report"
	"github.com/cheapseatsdev/cheapseats/internal/run"
	"github.com/cheapseatsdev/cheapseats/internal/server"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
	"github.com/cheapseatsdev/cheapseats/internal/store"
	"github.com/cheapseatsdev/cheapseats/internal/version"
)

const usage = `cheapseats: how AI assistants answer your buyers' questions.

  cheapseats init <url> [dir]               a new config repo from the brand's website (-dry-run,
                                         -name, -competitors a,b, -suggest)
  cheapseats serve [-addr 127.0.0.1:3000]   web UI, JSON API and the daily schedule
  cheapseats sets                           list prompt sets
  cheapseats version                        this build's version
  cheapseats backup <file>                  a consistent copy of the database, safe while serve runs
  cheapseats check [-no-jev]                validate cheapseats.toml and every prompt set
  cheapseats plan <before> [<after>]        what a config change does to trends, for its pull request
  cheapseats migrate-config [dir]           give every prompt an id (prompt sets from before ids)
  cheapseats run <set-id>                   run one set and wait for it (costs money)
  cheapseats report <run-id> [-json]        rates, share of voice and top pages for a run
  cheapseats next-steps <run-id> [-rebuild] actions, investigations and market findings as markdown
  cheapseats fixes <run-id> [-rebuild]      alias for next-steps
  cheapseats themes <run-id> [-rebuild]     what the run's answers praise and object to, grouped
  cheapseats regrade <set-id|run-id> [-since 28d] [-dry-run=false]
                                         grade stored answers again with today's grader, brand
                                         and competitors (a dry run, its cost, unless -dry-run=false)
  cheapseats suggest-prompts <set-id> [-n 10]  new buyer questions for a set, as TOML to paste
                                         (one analyst call; writes nothing)
  cheapseats review <set-id> [-days 30] [-json] [-suggest] [-jev]
                                         flat, noisy, off-target and duplicate prompts, and gaps,
                                         from stored answers (free; -suggest: one analyst call;
                                         -jev: ask Jev about the prompts' wording first)
  cheapseats discover [-json]               competitors to consider, from sites similar to the
                                         configured ones, as TOML to paste ([exa]; writes nothing)
  cheapseats audit [site...]                what AI crawlers can read on the brand's sites (free)
  cheapseats check-citations <run-id>       which pages the run cited name the brand, and which rivals
  cheapseats probe [-engine exa|claude|both] [-domains a,b] "<query>"
                                         one query through the answerers' web search tool, Exa's
                                         neutral index ([exa] in the config), or both compared
  cheapseats crawl import <logfile>...      count AI crawler fetches and AI referrals from access logs
  cheapseats crawl report [-days 30]        imported counts per page, beside how the latest runs cite it
  cheapseats golden export <snapshot.db> [-n 80] [-set id] [-o dir]
                                         sample graded answers from a database snapshot into the
                                         grader's golden set, for a human to label (docs/grading.md)
  cheapseats evaluation import [file.jsonl] import saved review evidence (the bundled fixtures
                                         when no file is supplied); no provider calls

Config: CHEAPSEATS_CONFIG, else the example in examples/cheapseats-config; prompt sets beside it,
data in ./data (CHEAPSEATS_DATA_DIR). Secrets from the environment or .env: ANTHROPIC_API_KEY
(or CHEAPSEATS_ANTHROPIC_BASE_URL, a proxy that adds it), OPENAI_API_KEY and CHEAPSEATS_TOKENS.
From the real environment only (.env lines setting them are ignored with a warning): base URLs,
proxies, certificate files, CHEAPSEATS_AUTH_HEADER, CHEAPSEATS_ALLOWED_EMAILS, CHEAPSEATS_CLI_ENV
and startup variables (LD_*, DYLD_*, NODE_OPTIONS...).
`

func main() {
	loadDotEnv(".env")
	level := slog.LevelInfo
	if env.Get("DEBUG") != "" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := dispatch(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "cheapseats:", err)
		os.Exit(1)
	}
}

// loadConfigDir reads a checkout of a config repo: cheapseats.toml and the prompt sets beside it.
func loadConfigDir(dir string) (plan.Config, error) {
	cfg, err := config.Load(filepath.Join(dir, "cheapseats.toml"))
	if err != nil {
		return plan.Config{}, err
	}
	all, err := sets.LoadAll(filepath.Join(dir, "prompt-sets"))
	return plan.Config{Cfg: cfg, Sets: sets.Share(all, cfg.Competitors)}, err
}

// configPath is CHEAPSEATS_CONFIG, or the example config, so a fresh checkout runs as-is and never
// picks up a real deployment's config (its schedule, its subscriptions) by accident.
func configPath() string { return env.Or("CONFIG", "examples/cheapseats-config/cheapseats.toml") }

func open() (*app.App, error) {
	cfg, err := config.Load(configPath())
	if err != nil {
		return nil, err
	}
	dataDir := env.Or("DATA_DIR", "data")
	db, err := store.Open(dataDir)
	if err != nil {
		return nil, err
	}
	a, err := app.New(cfg, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}

func dispatch(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "evaluation":
		return evaluationCmd(ctx, args)
	case "help", "-h", "--help":
		fmt.Print(usage + "\n" + passUsage())
		return nil
	case "backup":
		// A consistent copy of the live database (SQLite's VACUUM INTO), safe while serve runs.
		// Copying cheapseats.db itself can miss what's still in its write-ahead log.
		if len(args) != 1 {
			return errors.New("usage: cheapseats backup <file>")
		}
		if _, err := os.Stat(args[0]); err == nil {
			return fmt.Errorf("%s already exists", args[0])
		}
		db, err := store.Open(env.Or("DATA_DIR", "data"))
		if err != nil {
			return err
		}
		defer db.Close()
		if err := db.Backup(ctx, args[0]); err != nil {
			return err
		}
		fmt.Println("backed up to", args[0])
		return nil
	case "version":
		fmt.Printf("cheapseats %s (database schema %d)\n", version.String(), store.Schema())
		return nil
	case "check":
		cfg, err := config.Load(configPath())
		if err != nil {
			return err
		}
		dir := sets.Dir(cfg.Dir)
		var errs []error
		if err := sets.Check(dir, append([]string{cfg.Brand.Name}, cfg.Brand.Aliases...)); err != nil {
			errs = append(errs, err)
		}
		// A subscription answerer needs its CLI where Cheap Seats runs, and a run skips one that's
		// missing: fine for an optional one (codex beside claude), but if none can run, as with
		// CLI answerers in the image, that fails here rather than skipping every answer later.
		// API keys are deploy-time secrets, so a missing key is only reported.
		var noCLI []config.Answerer
		runnable := 0
		for _, a := range cfg.Answerers {
			if config.CLI(a.Provider) != "" && config.Missing(a.Provider) != "" {
				noCLI = append(noCLI, a)
				continue
			}
			runnable++
			if m := config.Missing(a.Provider); m != "" {
				fmt.Printf("note: answerer %s needs %s at runtime\n", a.ID, config.KeyVar(a.Provider))
			}
		}
		for _, a := range noCLI {
			if runnable == 0 {
				errs = append(errs, fmt.Errorf("answerer %s: %s is not installed here; use an anthropic or openai answerer in a container", a.ID, config.CLI(a.Provider)))
			} else {
				fmt.Printf("note: answerer %s: %s isn't installed here, so runs skip it\n", a.ID, config.CLI(a.Provider))
			}
		}
		// The same for the grader and the analyst, which can run on the claude CLI too.
		for name, m := range map[string]config.Model{"grader": cfg.Grader, "analyst": cfg.Analyst} {
			if m.Via() == config.ClaudeCode && config.Missing(config.ClaudeCode) != "" {
				errs = append(errs, fmt.Errorf("[%s] provider is claude-code, but claude is not installed here; use the anthropic provider in a container", name))
			} else if config.Missing(m.Via()) != "" {
				fmt.Printf("note: the %s needs %s (or %s) at runtime\n", name, config.KeyVar(config.Anthropic), config.AnthropicBaseURLVar)
			}
		}
		// The optional providers: configured but not callable here is a note, like a missing key.
		if cfg.TypeSafe != nil {
			if m := config.Missing(config.TypeSafeProvider); m != "" {
				fmt.Printf("note: [typesafe] needs %s (or %s) at runtime; without it, these passes are skipped: %s\n", config.KeyVar(config.TypeSafeProvider), config.TypeSafeBaseURLVar, strings.Join(app.Configured(cfg, config.TypeSafeProvider), ", "))
			}
			if cfg.PromptReviewOn() && cfg.Brand.Category == "" {
				fmt.Printf("note: [brand] category is empty, so the prompt review's off-target question is not asked; set it to the market in a noun phrase (\"AI search visibility tools\")\n")
			}
			if p, ok := cfg.Pricing[cfg.TypeSafeModel()]; !ok || p.Input <= 0 {
				errs = append(errs, fmt.Errorf("[typesafe] model %q has no [pricing.%q] entry with a positive input price", cfg.TypeSafeModel(), cfg.TypeSafeModel()))
			}
		}
		if cfg.Exa != nil {
			if m := config.Missing(config.ExaProvider); m != "" {
				fmt.Printf("note: [exa] needs %s (or %s) at runtime; without it, these passes are skipped: %s\n", config.KeyVar(config.ExaProvider), config.ExaBaseURLVar, strings.Join(app.Configured(cfg, config.ExaProvider), ", "))
			}
			if p, ok := cfg.Pricing["exa-search"]; !ok || p.SearchPer1K <= 0 {
				errs = append(errs, fmt.Errorf("[exa] needs a [pricing.exa-search] entry with a positive search_per_1k"))
			}
			if p, ok := cfg.Pricing["exa-contents"]; cfg.ContentsFallbackOn() && (!ok || p.PagePer1K <= 0) {
				fmt.Printf("note: [exa] contents_fallback needs a [pricing.exa-contents] entry with page_per_1k; without one no page is read through Exa\n")
			}
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
		all, _ := sets.LoadAll(dir)
		rev := cfg.Rev
		if rev == "" {
			rev = "unknown commit"
		}
		fmt.Printf("ok: %s and %d prompt sets in %s (%s)\n", configPath(), len(all), dir, rev)
		// Jev's suggestions about the prompts themselves (docs/prompt-review.md), when [typesafe]
		// is configured and callable here; never a failure. -no-jev skips them.
		judgeSuggestions(ctx, cfg, args)
		return nil
	case "plan":
		// What a config change does, for its pull request: cheapseats plan <before> [<after>], each a
		// checkout of the config repo (the directory holding cheapseats.toml).
		if len(args) < 1 {
			return errors.New("usage: cheapseats plan <config dir before> [<config dir after>]")
		}
		after := filepath.Dir(configPath())
		if len(args) > 1 {
			after = args[1]
		}
		b, err := loadConfigDir(args[0])
		if err != nil {
			return fmt.Errorf("before: %w", err)
		}
		a, err := loadConfigDir(after)
		if err != nil {
			return fmt.Errorf("after: %w", err)
		}
		fmt.Print(plan.Markdown(b, a))
		return nil
	case "migrate-config":
		// Before prompts had ids: give each one, so results, trends and fixes can follow it.
		dir := sets.Dir(filepath.Dir(configPath()))
		if len(args) > 0 {
			dir = args[0]
		}
		changed, err := sets.Migrate(dir)
		for _, f := range changed {
			fmt.Println("rewrote", f)
		}
		if err != nil {
			return err
		}
		if len(changed) == 0 {
			fmt.Println("nothing to migrate in", dir)
		} else {
			fmt.Println("Review the generated prompt ids before committing: they're permanent. Then run cheapseats check.")
		}
		return nil
	case "sets":
		a, err := open()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTIER\tPROMPTS\tVERSION\tNAME")
		for _, s := range a.Sets() {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", s.ID, s.Tier, len(s.Prompts), s.Version, s.Name)
		}
		return w.Flush()
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", env.Or("ADDR", "127.0.0.1:3000"), "listen address")
		fs.Parse(args)
		return serve(ctx, *addr)
	case "run":
		if len(args) != 1 {
			return errors.New("usage: cheapseats run <set-id>")
		}
		a, err := open()
		if err != nil {
			return err
		}
		s, ok := a.Set(args[0])
		if !ok {
			return fmt.Errorf("no prompt set %q (see cheapseats sets)", args[0])
		}
		r, err := a.Runs.Create(ctx, s, "manual")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "run %s: %d prompts, answerers %v\n", r.ID, len(s.Prompts), answererIDs(r.Snapshot.Answerers))
		if len(r.Snapshot.Skipped) > 0 {
			fmt.Fprintf(os.Stderr, "  skipped (no API key): %v\n", r.Snapshot.Skipped)
		}
		done := make(chan struct{})
		go progress(a, r.ID, done)
		err = a.Runs.Execute(ctx, r.ID)
		close(done)
		if err != nil {
			return err
		}
		if err := printReport(a, r.ID, false); err != nil {
			return err
		}
		completed, err := a.Runs.Get(context.Background(), r.ID)
		if err != nil {
			return err
		}
		if completed.Status != run.Done {
			return fmt.Errorf("study ended %s; inspect errors and retry unfinished results", completed.Status)
		}
		return nil
	case "report":
		fs := flag.NewFlagSet("report", flag.ExitOnError)
		js := fs.Bool("json", false, "print JSON")
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("usage: cheapseats report <run-id>")
		}
		a, err := open()
		if err != nil {
			return err
		}
		return printReport(a, fs.Arg(0), *js)
	case "fixes", "next-steps":
		fs := flag.NewFlagSet("fixes", flag.ExitOnError)
		rebuild := fs.Bool("rebuild", false, "generate and verify next steps first (analyst calls and bounded page checks)")
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("usage: cheapseats fixes <run-id>")
		}
		a, err := open()
		if err != nil {
			return err
		}
		if *rebuild {
			if err := a.BuildFixes(ctx, fs.Arg(0)); err != nil {
				return err
			}
		}
		r, err := a.Runs.Get(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		fx, fe, err := a.Fixes(ctx, r.ID)
		if err != nil {
			return err
		}
		if fe != nil {
			fmt.Fprintln(os.Stderr, "next steps analysis error:", *fe)
		}
		next, err := a.RunNextSteps(ctx, r.ID)
		if err != nil {
			return err
		}
		if fx == nil && len(next.Investigations) == 0 && len(next.Findings) == 0 {
			fmt.Println("No completed Next steps analysis yet.")
			return nil
		}
		fmt.Printf("# Next steps: %s\n\n%d actions · %d investigations · %d market findings\n\n", r.Snapshot.Set.Name, len(next.Actions), len(next.Investigations), len(next.Findings))
		fmt.Println("## Actions")
		for _, q := range next.Actions {
			fmt.Printf("\n- **%s**\n  - Change: %s\n  - Where: %s\n  - Done when: %s\n  - Verification: %s\n", q.Title, q.Claim, q.Target, q.DoneWhen, q.Verification.Summary)
		}
		fmt.Println("\n## Investigations")
		for _, inv := range next.Investigations {
			fmt.Printf("\n- **%s**\n  - Why: %s\n  - Read-only check: %s\n  - Decision: %s\n", inv.Title, inv.Why, inv.Check, inv.Decision)
		}
		fmt.Println("\n## Market findings")
		for _, f := range next.Findings {
			fmt.Printf("\n- **%s**: %s\n  - Evidence: %s\n", f.Title, f.Detail, f.Evidence)
		}
		return nil
	case "themes":
		fs := flag.NewFlagSet("themes", flag.ExitOnError)
		rebuild := fs.Bool("rebuild", false, "run the theme pass again first (one grader call)")
		fs.Parse(positionalLast(args))
		if fs.NArg() != 1 {
			return errors.New("usage: cheapseats themes <run-id> [-rebuild]")
		}
		a, err := open()
		if err != nil {
			return err
		}
		if *rebuild {
			if _, err := a.Runs.BuildThemes(ctx, fs.Arg(0)); err != nil {
				return err
			}
		}
		t, err := a.Runs.Themes(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		if t == nil {
			return errors.New("this run has no themes; -rebuild makes them")
		}
		return printThemes(a, fs.Arg(0), *t)
	case "regrade":
		return regradeCmd(ctx, args)
	case "suggest-prompts":
		fs := flag.NewFlagSet("suggest-prompts", flag.ExitOnError)
		n := fs.Int("n", 10, "how many prompts")
		fs.Parse(positionalLast(args))
		if fs.NArg() != 1 {
			return errors.New("usage: cheapseats suggest-prompts <set-id> [-n 10]")
		}
		a, err := open()
		if err != nil {
			return err
		}
		res, err := a.SuggestPrompts(ctx, fs.Arg(0), *n)
		if err != nil {
			return err
		}
		if res.Note != "" {
			fmt.Fprintln(os.Stderr, res.Note)
		}
		fmt.Print(res.Snippet)
		fmt.Fprintf(os.Stderr, "%d prompts for %s (%d dropped as repeats), $%.3f. Paste into its file and open a pull request.\n", len(res.Prompts), fs.Arg(0), res.Dropped, res.CostUSD)
		return nil
	case "review":
		return reviewCmd(ctx, args)
	case "discover":
		return discoverCmd(ctx, args)
	case "init":
		return initCmd(ctx, args)
	case "audit":
		return auditCmd(ctx, args)
	case "check-citations":
		return checkCitationsCmd(ctx, args)
	case "probe":
		return probeCmd(ctx, args)
	case "crawl":
		return crawlCmd(ctx, args)
	case "golden":
		return goldenCmd(ctx, args)
	}
	// The optional providers' passes, each on one finished run: cheapseats fold <run-id> and the
	// rest. The clients budget through a.Runs.Budget themselves.
	if p, ok := app.PassByCommand(cmd); ok {
		if len(args) != 1 {
			return fmt.Errorf("usage: cheapseats %s <run-id>", cmd)
		}
		a, err := open()
		if err != nil {
			return err
		}
		n, err := a.RunPass(ctx, p, args[0])
		if p.Print != nil {
			p.Print(a, ctx, args[0], n)
		}
		return err
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, passUsage()+usage)
}

// passUsage is one help line per registered pass, in the usage text's layout.
func passUsage() string {
	var b strings.Builder
	for _, p := range app.Passes() {
		if p.Command != "" {
			fmt.Fprintf(&b, "  %-41s %s\n", "cheapseats "+p.Command+" <run-id>", p.Usage)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "Passes over a finished run (each also runs after every run when its provider is configured):\n\n" + b.String() + "\n"
}

// positionalLast moves a leading argument that isn't a flag to the end, so "themes <id> -rebuild"
// parses as well as "themes -rebuild <id>".
func positionalLast(args []string) []string {
	if len(args) > 1 && !strings.HasPrefix(args[0], "-") {
		return append(append([]string{}, args[1:]...), args[0])
	}
	return args
}

func printThemes(a *app.App, id string, t opinion.Themes) error {
	ctx := context.Background()
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return err
	}
	res, err := a.Runs.Results(ctx, id)
	if err != nil {
		return err
	}
	var grades []*grade.Grade
	for _, x := range res {
		grades = append(grades, x.Grade)
	}
	s := opinion.SummarizeWith(grades, a.PhraseOptions())
	fmt.Printf("%s\n\nNet sentiment %s over %d answers naming %s with a tone:", r.Name, s.NetText(), s.Toned, a.Cfg.Brand.Name)
	for _, c := range s.Tones {
		fmt.Printf(" %s %d", c.Tone, c.N)
	}
	fmt.Println()
	switch {
	case t.Error != "":
		fmt.Println("\nTheme pass failed:", t.Error)
	case t.Skipped != "":
		fmt.Println("\nNo themes:", t.Skipped)
	}
	for _, th := range t.Themes {
		who := ""
		if th.Beneficiary != "" {
			who = ", helps " + th.Beneficiary
		}
		fmt.Printf("\n%-11s %s (%d answers%s)\n            “%s”\n", th.Kind, th.Name, th.Answers, who, th.Quote)
	}
	return nil
}

func regradeCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("regrade", flag.ExitOnError)
	sinceFlag := fs.String("since", "28d", "for a set, only runs from this long ago on: 28d, 72h, or all")
	dry := fs.Bool("dry-run", true, "say what would be re-graded and what it would cost, and grade nothing")
	fs.Parse(positionalLast(args))
	if fs.NArg() != 1 {
		return errors.New("usage: cheapseats regrade <set-id|run-id> [-since 28d] [-dry-run=false]")
	}
	since, err := parseSince(*sinceFlag)
	if err != nil {
		return err
	}
	a, err := open()
	if err != nil {
		return err
	}
	p, err := a.PlanRegrade(ctx, fs.Arg(0), since)
	if err != nil {
		return err
	}
	printRegradePlan(p)
	if *dry || len(p.Runs) == 0 {
		if len(p.Runs) > 0 {
			fmt.Println("\nDry run: nothing graded. Run again with -dry-run=false to re-grade.")
		}
		return nil
	}
	rep, err := a.Regrade(ctx, fs.Arg(0), since)
	if err != nil {
		return err
	}
	fmt.Printf("\nRe-graded %d runs, %d answers, $%.2f.\n", len(rep.Regraded), rep.Answers, rep.CostUSD)
	for id, why := range rep.Failed {
		fmt.Printf("  %s left as it was: %s\n", id, why)
	}
	if rep.Stopped != "" {
		return fmt.Errorf("stopped: %s", rep.Stopped)
	}
	if len(rep.Failed) > 0 {
		return fmt.Errorf("%d runs failed to re-grade", len(rep.Failed))
	}
	return nil
}

// parseSince reads -since: days ("28d"), a duration ("72h", rounded up to days), or "all" (0).
func parseSince(s string) (int, error) {
	switch {
	case s == "all" || s == "0":
		return 0, nil
	case strings.HasSuffix(s, "d"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("-since %q: want days like 28d, a duration like 72h, or all", s)
		}
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("-since %q: want days like 28d, a duration like 72h, or all", s)
	}
	return int((d + 24*time.Hour - 1) / (24 * time.Hour)), nil
}

func printRegradePlan(p run.RegradePlan) {
	how := "through the API"
	if p.Subscription {
		how = "on the claude CLI (subscription)"
	}
	fmt.Printf("Re-grading with %s, %s.\n", p.Grader, how)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if len(p.Runs) > 0 {
		fmt.Fprintln(w, "\nRUN\tCREATED\tANSWERS\tMETHOD\tEST. COST")
	}
	for _, r := range p.Runs {
		method := "unchanged"
		if r.FromMethod != r.ToMethod {
			method = shortHash(r.FromMethod) + " → " + shortHash(r.ToMethod)
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t$%.2f\n", r.ID, r.CreatedAt[:10], r.Answers, method, r.EstimatedUSD)
	}
	w.Flush()
	fmt.Printf("\n%d runs to re-grade, %d answers.", len(p.Runs), p.Answers)
	if p.Current > 0 {
		fmt.Printf(" %d runs (%d answers) already graded with today's inputs: skipped.", p.Current, p.CurrentAnswers)
	}
	if p.Unfinished > 0 {
		fmt.Printf(" %d unfinished or running: left alone.", p.Unfinished)
	}
	if p.AskedDifferently > 0 {
		fmt.Printf(" %d asked differently (another answer prompt or search policy): re-grading can't join them to today's trend, skipped.", p.AskedDifferently)
	}
	fmt.Println()
	switch {
	case p.Subscription:
		fmt.Println("Cost: $0 (the claude CLI runs on your subscription).")
	case p.Unpriced:
		fmt.Println("Cost: unknown, the grader's model has no [pricing]; add it before re-grading.")
	default:
		fmt.Printf("Estimated cost: $%.2f (from token counts and [pricing]), within the daily budget.\n", p.EstimatedUSD)
	}
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func answererIDs(as []config.Answerer) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

func progress(a *app.App, id string, done chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-time.After(20 * time.Second):
		}
		var n, fin int
		a.DB.QueryRow(`SELECT COUNT(*), SUM(status IN ('done','error')) FROM results WHERE run_id=?`, id).Scan(&n, &fin)
		fmt.Fprintf(os.Stderr, "  %d/%d answers\n", fin, n)
	}
}

func printReport(a *app.App, id string, asJSON bool) error {
	ctx := context.Background()
	r, err := a.Runs.Get(ctx, id)
	if err != nil {
		return err
	}
	res, err := a.Runs.Results(ctx, id)
	if err != nil {
		return err
	}
	s := a.Summary(r, res)
	if asJSON {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(map[string]any{"run": r, "summary": s})
	}
	fmt.Printf("%s  [%s]\n", r.Name, r.Status)
	if r.Error != nil {
		fmt.Println(*r.Error)
	}
	pr := func(x report.Rate) string {
		if x.N == 0 {
			return "–"
		}
		return fmt.Sprintf("%3.0f%% (%.0f–%.0f)", 100*x.P, 100*x.Lo, 100*x.Hi)
	}
	fmt.Printf("\n%d graded, %d errors, $%.2f\n", s.Done, s.Errors, s.CostUSD)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "\nANSWERER\tTRACK\tTOP PICK\tRECOMMENDED\tNAMED")
	fmt.Fprintf(w, "all\t\t%s\t%s\t%s\n", pr(s.Overall.TopPick), pr(s.Overall.Recommended), pr(s.Overall.Visible))
	if s.BrandedPrompts > 0 {
		fmt.Fprintf(w, "unbranded\t\t%s\t%s\t%s\n", pr(s.Unbranded.TopPick), pr(s.Unbranded.Recommended), pr(s.Unbranded.Visible))
	}
	for _, x := range s.Slices {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", x.Answerer, x.Track, pr(x.TopPick), pr(x.Recommended), pr(x.Visible))
	}
	w.Flush()
	if rank := s.BrandRank(); rank == 0 {
		fmt.Printf("\nShare of voice (%s not named in any answer):\n", a.Cfg.Brand.Name)
	} else {
		fmt.Printf("\nShare of voice (%s is #%d):\n", a.Cfg.Brand.Name, rank)
	}
	for i, v := range s.Voice {
		if i == 10 {
			break
		}
		fmt.Printf("  %2d. %-32s %3d answers (%3.0f%%)  %3.0f%% of mentions  %d top picks\n", i+1, v.Name, v.Answers, 100*v.NamedRate, 100*v.ShareOfVoice, v.TopPick)
	}
	if len(s.Outreach) > 0 {
		fmt.Printf("\nCited without %s (outreach):\n", a.Cfg.Brand.Name)
		for i, o := range s.Outreach {
			if i == 10 {
				break
			}
			fmt.Printf("  %3d cited  %-32s %s\n", o.Citations, o.Host, strings.Join(o.Competitors, ", "))
		}
	}
	fmt.Println("\nMost-cited pages:")
	for i, p := range s.Pages {
		if i == 10 {
			break
		}
		fp := ""
		if p.FirstParty {
			fp = "  [first-party]"
		}
		fmt.Printf("  %3d cited  %3d surfaced  %s%s\n", p.Cited, p.Surfaced, p.URL, fp)
	}
	return nil
}

func serve(ctx context.Context, addr string) error {
	a, err := open()
	if err != nil {
		return err
	}
	// A read-only copy (CHEAPSEATS_READONLY) serves a database another Cheap Seats writes: it
	// resumes and schedules nothing, and needs no sign-in, since it can't change anything.
	readOnly := env.Get("READONLY") != ""
	var interrupted []string
	if !readOnly {
		if interrupted, err = a.Runs.Recover(ctx); err != nil {
			return err
		}
	}
	srv, err := server.New(a)
	if err != nil {
		return err
	}
	if err := srv.CheckListen(addr); err != nil {
		return err
	}
	// Runs get their own context, so shutdown can first mark them interrupted (to resume on the
	// next start) rather than have the signal cancel them.
	runs, stopRuns := context.WithCancel(context.Background())
	if !readOnly {
		go func() {
			a.Resume(runs, interrupted)
			a.Schedule(runs)
		}()
	}
	// A client gets a minute to send its whole request and two to send the next on a kept-alive
	// connection. Nothing limits writing the response: wait_for_run holds an MCP call open for up to
	// ten minutes, and MCP's event stream stays open. (Go clears the read deadline once a body is
	// read, so the read limit doesn't cut those short either.)
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		slog.Info("shutting down; runs in progress resume on the next start")
		a.Runs.Stop(20 * time.Second)
		stopRuns()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	slog.Info("cheapseats listening", "addr", "http://"+addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadDotEnv sets KEY=VALUE lines from a file without overriding the real environment. It is
// the .env of whatever directory cheapseats runs in, a config checkout included, so it holds keys
// and settings only: a variable that sends requests somewhere else, runs code or decides who
// signs in (dotenvRefused) is ignored with a warning, and must come from the real environment.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	dotenvApply(path, data, os.Stderr)
}

// dotenvRefused reports whether .env may not set the variable named k: a proxy or base URL would
// send the real API keys in the environment to whoever runs it, a certificate file would let a
// proxy read them, CHEAPSEATS_CLI_ENV picks what the CLIs inherit, a startup variable
// (env.LoadsCode) runs code in every program Cheap Seats starts, and CHEAPSEATS_AUTH_HEADER and
// CHEAPSEATS_ALLOWED_EMAILS decide who is signed in by a header. CHEAPSEATS_TOKENS stays allowed:
// it is the documented local setup (.env.example), and a token only lets in whoever holds it.
func dotenvRefused(k string) bool {
	u := strings.ToUpper(k)
	switch {
	case strings.HasSuffix(u, "_BASE_URL"), strings.HasSuffix(u, "_PROXY"), env.LoadsCode(u):
		return true
	}
	switch u {
	case env.Prefix + "CLI_ENV", env.Prefix + "AUTH_HEADER", env.Prefix + "ALLOWED_EMAILS",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS":
		return true
	}
	return false
}

// dotenvApply is loadDotEnv on the file's contents, warning on w for each line it refuses.
func dotenvApply(path string, data []byte, w io.Writer) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); set || v == "" {
			continue
		}
		if dotenvRefused(k) {
			fmt.Fprintf(w, "cheapseats: WARNING: ignoring %s from %s: it would send requests elsewhere, run code or decide who signs in, so it must be set in the real environment\n", k, path)
			continue
		}
		os.Setenv(k, v)
	}
}
