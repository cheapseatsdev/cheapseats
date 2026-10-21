// popcorn asks AI assistants the questions a brand's buyers ask and grades whether they
// recommend it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/metcalfc/popcorn/internal/analysis"
	"github.com/metcalfc/popcorn/internal/app"
	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/plan"
	"github.com/metcalfc/popcorn/internal/report"
	"github.com/metcalfc/popcorn/internal/server"
	"github.com/metcalfc/popcorn/internal/sets"
	"github.com/metcalfc/popcorn/internal/store"
	"github.com/metcalfc/popcorn/internal/version"
)

const usage = `popcorn: how AI assistants answer your buyers' questions.

  popcorn serve [-addr 127.0.0.1:3000]   web UI, JSON API and the daily schedule
  popcorn sets                           list prompt sets
  popcorn version                        this build's version
  popcorn backup <file>                  a consistent copy of the database, safe while serve runs
  popcorn check                          validate popcorn.toml and every prompt set
  popcorn plan <before> [<after>]        what a config change does to trends, for its pull request
  popcorn migrate-config [dir]           give every prompt an id (prompt sets from before ids)
  popcorn run <set-id>                   run one set and wait for it (costs money)
  popcorn report <run-id> [-json]        rates, share of voice and top pages for a run
  popcorn fixes <run-id> [-rebuild]      the run's fix list as markdown

Config: POPCORN_CONFIG, else the example in examples/popcorn-config; prompt sets beside it,
data in ./data (POPCORN_DATA_DIR). Secrets from the environment or .env: ANTHROPIC_API_KEY,
OPENAI_API_KEY, POPCORN_TOKENS, POPCORN_AUTH_HEADER and POPCORN_ALLOWED_EMAILS.
`

func main() {
	loadDotEnv(".env")
	level := slog.LevelInfo
	if os.Getenv("POPCORN_DEBUG") != "" {
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
		fmt.Fprintln(os.Stderr, "popcorn:", err)
		os.Exit(1)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// loadConfigDir reads a checkout of a config repo: popcorn.toml and the prompt sets beside it.
func loadConfigDir(dir string) (plan.Config, error) {
	cfg, err := config.Load(filepath.Join(dir, "popcorn.toml"))
	if err != nil {
		return plan.Config{}, err
	}
	all, err := sets.LoadAll(filepath.Join(dir, "prompt-sets"))
	return plan.Config{Cfg: cfg, Sets: all}, err
}

// configPath is POPCORN_CONFIG, or the example config, so a fresh checkout runs as-is and never
// picks up a real deployment's config (its schedule, its subscriptions) by accident.
func configPath() string { return env("POPCORN_CONFIG", "examples/popcorn-config/popcorn.toml") }

func open() (*app.App, error) {
	cfg, err := config.Load(configPath())
	if err != nil {
		return nil, err
	}
	dataDir := env("POPCORN_DATA_DIR", "data")
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
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "backup":
		// A consistent copy of the live database (SQLite's VACUUM INTO), safe while serve runs.
		// Copying popcorn.db itself can miss what's still in its write-ahead log.
		if len(args) != 1 {
			return errors.New("usage: popcorn backup <file>")
		}
		if _, err := os.Stat(args[0]); err == nil {
			return fmt.Errorf("%s already exists", args[0])
		}
		db, err := store.Open(env("POPCORN_DATA_DIR", "data"))
		if err != nil {
			return err
		}
		defer db.Close()
		if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, args[0]); err != nil {
			return err
		}
		fmt.Println("backed up to", args[0])
		return nil
	case "version":
		fmt.Printf("popcorn %s (database schema %d)\n", version.String(), store.Schema())
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
		// A subscription answerer needs its CLI where Popcorn runs; the image has none, so a
		// config meant for a container fails here rather than skipping every answer later. API
		// keys are deploy-time secrets, so a missing key is only reported.
		for _, a := range cfg.Answerers {
			if bin := config.CLI(a.Provider); bin != "" && config.Missing(a.Provider) != "" {
				errs = append(errs, fmt.Errorf("answerer %s: %s is not installed here; use an anthropic or openai answerer in a container", a.ID, bin))
			} else if m := config.Missing(a.Provider); m != "" {
				fmt.Printf("note: answerer %s needs %s at runtime\n", a.ID, config.KeyVar(a.Provider))
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
		return nil
	case "plan":
		// What a config change does, for its pull request: popcorn plan <before> [<after>], each a
		// checkout of the config repo (the directory holding popcorn.toml).
		if len(args) < 1 {
			return errors.New("usage: popcorn plan <config dir before> [<config dir after>]")
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
			fmt.Println("Review the generated prompt ids before committing: they're permanent. Then run popcorn check.")
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
		addr := fs.String("addr", env("POPCORN_ADDR", "127.0.0.1:3000"), "listen address")
		fs.Parse(args)
		return serve(ctx, *addr)
	case "run":
		if len(args) != 1 {
			return errors.New("usage: popcorn run <set-id>")
		}
		a, err := open()
		if err != nil {
			return err
		}
		s, ok := a.Set(args[0])
		if !ok {
			return fmt.Errorf("no prompt set %q (see popcorn sets)", args[0])
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
		if completed.Status != "done" {
			return fmt.Errorf("study ended %s; inspect errors and retry unfinished results", completed.Status)
		}
		return nil
	case "report":
		fs := flag.NewFlagSet("report", flag.ExitOnError)
		js := fs.Bool("json", false, "print JSON")
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("usage: popcorn report <run-id>")
		}
		a, err := open()
		if err != nil {
			return err
		}
		return printReport(a, fs.Arg(0), *js)
	case "fixes":
		fs := flag.NewFlagSet("fixes", flag.ExitOnError)
		rebuild := fs.Bool("rebuild", false, "rebuild the list first (one Opus call)")
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("usage: popcorn fixes <run-id>")
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
			fmt.Fprintln(os.Stderr, "fix list error:", *fe)
		}
		fmt.Print(analysis.Markdown(r.Snapshot.Set, fx))
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
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
	for _, x := range s.Slices {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", x.Answerer, x.Track, pr(x.TopPick), pr(x.Recommended), pr(x.Visible))
	}
	w.Flush()
	fmt.Printf("\nShare of voice (%s is #%d):\n", a.Cfg.Brand.Name, s.BrandRank())
	for i, v := range s.Voice {
		if i == 10 {
			break
		}
		fmt.Printf("  %2d. %-32s %3d answers  %d top picks\n", i+1, v.Name, v.Answers, v.TopPick)
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
	interrupted, err := a.Runs.Recover(ctx)
	if err != nil {
		return err
	}
	srv, err := server.New(a)
	if err != nil {
		return err
	}
	if os.Getenv("POPCORN_TOKENS") == "" && os.Getenv("POPCORN_AUTH_HEADER") == "" && !strings.HasPrefix(addr, "127.0.0.1") && !strings.HasPrefix(addr, "localhost") {
		return errors.New("refusing to listen beyond localhost without POPCORN_TOKENS or POPCORN_AUTH_HEADER set")
	}
	// Runs get their own context, so shutdown can first mark them interrupted (to resume on the
	// next start) rather than have the signal cancel them.
	runs, stopRuns := context.WithCancel(context.Background())
	go func() {
		a.Resume(runs, interrupted)
		a.Schedule(runs)
	}()
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		slog.Info("shutting down; runs in progress resume on the next start")
		a.Runs.Stop(20 * time.Second)
		stopRuns()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	slog.Info("popcorn listening", "addr", "http://"+addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadDotEnv sets KEY=VALUE lines from a file without overriding the real environment.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
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
		if _, set := os.LookupEnv(k); !set && v != "" {
			os.Setenv(k, v)
		}
	}
}
