package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/cheapseatsdev/cheapseats/internal/audit"
	"github.com/cheapseatsdev/cheapseats/internal/bots"
	"github.com/cheapseatsdev/cheapseats/internal/crawl"
)

func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

// auditCmd: cheapseats audit [-pages N] [-json] [site...]
func auditCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	pages := fs.Int("pages", 10, "pages from each sitemap to check besides the home page")
	js := fs.Bool("json", false, "print JSON")
	info := fs.Bool("info", false, "list info findings too")
	fs.Parse(args)
	a, err := open()
	if err != nil {
		return err
	}
	n := *pages
	if n == 0 {
		n = -1 // Options reads 0 as the default
	}
	res, err := a.Audit(ctx, fs.Args(), audit.Options{Pages: n})
	if err != nil {
		return err
	}
	if *js {
		return printJSON(res)
	}
	for _, s := range res.Sites {
		fmt.Printf("%s  %s\n", s.Site, s.Home)
		var parts []string
		if s.RobotsStatus > 0 {
			parts = append(parts, fmt.Sprintf("robots.txt %d", s.RobotsStatus))
		}
		if s.LLMsStatus > 0 {
			parts = append(parts, fmt.Sprintf("llms.txt %d", s.LLMsStatus))
		}
		if s.Sitemap != "" {
			parts = append(parts, fmt.Sprintf("sitemap %d pages, newest %s", s.SitemapURLs, or(s.Newest, "undated")))
		}
		parts = append(parts, fmt.Sprintf("%d pages checked", len(s.Pages)))
		fmt.Printf("  %s\n", strings.Join(parts, " · "))
		var blocked []string
		for _, b := range s.Bots {
			if !b.Allowed {
				blocked = append(blocked, fmt.Sprintf("%s (%s, robots.txt)", b.Bot, b.Class))
			}
			if b.Blocked {
				blocked = append(blocked, fmt.Sprintf("%s (%s, firewall %d)", b.Bot, b.Class, b.Status))
			}
		}
		if len(blocked) > 0 {
			fmt.Printf("  blocked: %s\n", strings.Join(blocked, ", "))
		}
		for _, f := range res.Findings {
			if f.Site != s.Site || f.Severity == audit.Info && !*info {
				continue
			}
			fmt.Printf("  %-7s %-26s %s%s\n          %s\n          fix: %s\n", strings.ToUpper(string(f.Severity)), f.Check, f.URL, viaMark(f.Via), f.Message, f.Fix)
		}
		fmt.Println()
	}
	fmt.Printf("%d errors, %d warnings, %d info", res.Count(audit.Error), res.Count(audit.Warning), res.Count(audit.Info))
	if !*info && res.Count(audit.Info) > 0 {
		fmt.Print(" (-info lists them)")
	}
	fmt.Println()
	return nil
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// viaMark says when a page's text was read through Exa rather than plain HTTP.
func viaMark(via string) string {
	if via == audit.ViaExa {
		return " (via Exa)"
	}
	return ""
}

// checkCitationsCmd: cheapseats check-citations [-n 25] [-json] <run-id>
func checkCitationsCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check-citations", flag.ExitOnError)
	n := fs.Int("n", 25, "how many of the most-cited third-party pages to check")
	js := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: cheapseats check-citations [-n 25] <run-id>")
	}
	a, err := open()
	if err != nil {
		return err
	}
	c, err := a.CheckCitations(ctx, fs.Arg(0), *n, audit.CiteOptions{})
	if err != nil {
		return err
	}
	if *js {
		return printJSON(c)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "CITED\tSURFACED\tNAMES %s\tCOMPETITORS\tURL\n", strings.ToUpper(a.Cfg.Brand.Name))
	for _, p := range c.Pages {
		names := "no"
		switch {
		case p.Skipped != "":
			names = "?"
		case p.NamesBrand:
			names = "yes"
		}
		comps := strings.Join(p.Competitors, ", ")
		if p.Skipped != "" {
			comps = "(" + p.Skipped + ")"
		}
		mark := viaMark(p.Via)
		if p.Outreach {
			mark += "  <- outreach"
		}
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s%s\n", p.Cited, p.Surfaced, names, comps, p.URL, mark)
	}
	w.Flush()
	fmt.Printf("\n%d of %d pages name competitors but not %s: outreach targets.\n", c.Outreach(), len(c.Pages), a.Cfg.Brand.Name)
	return nil
}

// crawlCmd: cheapseats crawl import [-host h] <logfile>... | cheapseats crawl report [-days 30] [-json]
func crawlCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cheapseats crawl import [-host example.com] <logfile>... | cheapseats crawl report [-days 30]")
	}
	a, err := open()
	if err != nil {
		return err
	}
	switch args[0] {
	case "import":
		fs := flag.NewFlagSet("crawl import", flag.ExitOnError)
		host := fs.String("host", "", "the site, for log lines that don't record one (common and combined logs); default the brand's first site")
		fs.Parse(args[1:])
		if fs.NArg() == 0 {
			return errors.New("usage: cheapseats crawl import [-host example.com] <logfile>... (- for stdin; .gz is read as gzip)")
		}
		h := *host
		if h == "" && len(a.AuditSites()) > 0 {
			h, _ = crawl.Key(a.AuditSites()[0])
		}
		for _, name := range fs.Args() {
			st, err := importFile(ctx, a.ImportCrawl, name, h)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			fmt.Printf("%s: %d lines, %d counted (%d assistant, %d search, %d training, %d engine, %d AI referrals, %d other visits), %d already imported, %d over 400 days old, %d unreadable\n",
				name, st.Lines, st.Counted, st.ByClass[bots.Assistant], st.ByClass[bots.Search], st.ByClass[bots.Training], st.ByClass[bots.Engine], st.ByClass[bots.Referral], st.ByClass[bots.Visit], st.Duplicate, st.TooOld, st.Unread)
			if st.Future > 0 {
				fmt.Printf("  %d lines are dated after tomorrow (UTC) and weren't counted\n", st.Future)
			}
			if st.OverLimit > 0 {
				fmt.Printf("  %d requests past this import's limit weren't counted; import the log again for the rest\n", st.OverLimit)
			}
			if st.NoHost {
				fmt.Println("  some lines name no host; pass -host to say which site the log is for")
			}
		}
		fmt.Println("Crawlers and people are told apart by user agent only (unverified): anyone can send one.")
		return nil
	case "report":
		fs := flag.NewFlagSet("crawl report", flag.ExitOnError)
		days := fs.Int("days", 30, "the last N days")
		js := fs.Bool("json", false, "print JSON")
		fs.Parse(args[1:])
		rep, err := a.Crawlers(ctx, *days)
		if err != nil {
			return err
		}
		if *js {
			return printJSON(rep)
		}
		fmt.Printf("Since %s, hosts %s (user agent only, unverified)\n\n", rep.Since, strings.Join(rep.Hosts, ", "))
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "CITED\tSURFACED\tASSISTANT\tSEARCH\tTRAINING\tENGINE\tREFERRALS\tVISITS\tPAGE\tFLAG")
		for i, p := range rep.Pages {
			if i == 50 {
				break
			}
			fmt.Fprintf(w, "%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s%s\t%s\n", p.Cited, p.Surfaced, p.Assistant, p.Search, p.Training, p.Engine, p.Referrals, p.Visits, p.Host, p.Path, p.Flag)
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown crawl command %q: import or report", args[0])
}

func importFile(ctx context.Context, imp func(context.Context, io.Reader, string) (crawl.Stats, error), name, host string) (crawl.Stats, error) {
	var r io.Reader = os.Stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return crawl.Stats{}, err
		}
		defer f.Close()
		r = f
	}
	if strings.HasSuffix(name, ".gz") {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return crawl.Stats{}, err
		}
		defer zr.Close()
		r = zr
	}
	return imp(ctx, r, host)
}
