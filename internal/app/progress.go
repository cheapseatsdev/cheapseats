package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/run"
)

// Progress is where an in-flight run stands, for the dashboard and the run page.
type Progress struct {
	RunID   string `json:"run_id"`
	SetID   string `json:"set_id"`
	Name    string `json:"name"`
	Trigger string `json:"trigger"`
	// answering | building fix list
	Stage string `json:"stage"`
	Counts
	Answerers []AnswererProgress `json:"answerers"`
	StartedAt string             `json:"started_at"`
	Elapsed   string             `json:"elapsed"`
	// A rough time left from the pace so far; empty until there's a pace to go on.
	Left string `json:"left,omitempty"`
	// How long since the last answer came back, so a stall is visible.
	LastAnswer string `json:"last_answer,omitempty"`
	// Cancellable: this process is running it (not a run left over from another one).
	Cancellable bool `json:"cancellable"`
}

type Counts struct {
	Total   int `json:"total"`
	Done    int `json:"done"`
	Errors  int `json:"errors"`
	Running int `json:"running"`
	Waiting int `json:"waiting"`
}

// Percentages for the progress bar.
func (c Counts) DonePct() float64    { return pct(c.Done, c.Total) }
func (c Counts) ErrorPct() float64   { return pct(c.Errors, c.Total) }
func (c Counts) RunningPct() float64 { return pct(c.Running, c.Total) }

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

type AnswererProgress struct {
	Answerer string `json:"answerer"`
	Counts
}

func (c *Counts) add(status run.ResultStatus, n int) {
	c.Total += n
	switch status {
	case run.ResultDone:
		c.Done += n
	case run.ResultError:
		c.Errors += n
	case run.ResultRunning:
		c.Running += n
	default:
		c.Waiting += n
	}
}

// Progress lists runs that are queued or running, and finished runs whose fix list is still
// being built, oldest first.
func (a *App) Progress(ctx context.Context) ([]Progress, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT id, set_id, name, trigger, status, COALESCE(started_at, created_at)
		FROM runs WHERE status IN ('queued','running') OR (status = 'done' AND fixes IS NULL AND fixes_error IS NULL)
		ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	var out []Progress
	for rows.Next() {
		var p Progress
		var status run.Status
		if err := rows.Scan(&p.RunID, &p.SetID, &p.Name, &p.Trigger, &status, &p.StartedAt); err != nil {
			rows.Close()
			return nil, err
		}
		p.Stage = "answering"
		if status == run.Done {
			if _, building := a.analyses.Load(p.RunID); !building {
				continue // finished, and nothing is building its list: not in progress
			}
			p.Stage = "building fix list"
		}
		out = append(out, p)
	}
	rows.Close()
	now := time.Now().UTC()
	for i := range out {
		p := &out[i]
		if err := a.fillProgress(ctx, p, now); err != nil {
			return nil, err
		}
		p.Cancellable = p.Stage == "answering" && (a.Runs.IsActive(p.RunID) || a.Runs.Status(ctx, p.RunID) == run.Queued)
	}
	return out, nil
}

// RunProgress is one run's progress, or nil when it isn't in progress.
func (a *App) RunProgress(ctx context.Context, id string) *Progress {
	ps, _ := a.Progress(ctx)
	for i := range ps {
		if ps[i].RunID == id {
			return &ps[i]
		}
	}
	return nil
}

func (a *App) fillProgress(ctx context.Context, p *Progress, now time.Time) error {
	rows, err := a.DB.QueryContext(ctx, `SELECT answerer, status, COUNT(*), MAX(finished_at) FROM results
		WHERE run_id = ? GROUP BY answerer, status ORDER BY answerer`, p.RunID)
	if err != nil {
		return err
	}
	defer rows.Close()
	byAnswerer := map[string]*AnswererProgress{}
	var order []string
	var last string
	for rows.Next() {
		var answerer string
		var status run.ResultStatus
		var n int
		var finished sql.NullString
		if err := rows.Scan(&answerer, &status, &n, &finished); err != nil {
			return err
		}
		ap := byAnswerer[answerer]
		if ap == nil {
			ap = &AnswererProgress{Answerer: answerer}
			byAnswerer[answerer] = ap
			order = append(order, answerer)
		}
		ap.add(status, n)
		p.add(status, n)
		if finished.String > last {
			last = finished.String
		}
	}
	for _, name := range order {
		p.Answerers = append(p.Answerers, *byAnswerer[name])
	}
	start, err := time.Parse(time.RFC3339, p.StartedAt)
	if err != nil {
		return nil
	}
	elapsed := now.Sub(start)
	p.Elapsed = short(elapsed)
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		p.LastAnswer = short(now.Sub(t)) + " ago"
	}
	finished := p.Done + p.Errors
	if left := p.Total - finished; p.Stage == "answering" && finished >= 3 && left > 0 {
		p.Left = "~" + short(time.Duration(float64(elapsed)/float64(finished)*float64(left)))
	}
	return nil
}

// short is a duration a person reads at a glance: 45s, 12m, 1h 5m.
func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
