package run

import (
	"context"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/sets"
)

// Preview is what Create would make of a set now, recorded nowhere: the snapshot (with adaptive
// sampling's extras, decided as Create would decide them) and the answers each answerer would
// give on each track. For a dry run.
type Preview struct {
	Snapshot Snapshot
	Cells    []PreviewCell
}

// PreviewCell is one answerer on one track: its planned answers over the whole set, and the
// extras adaptive sampling would add.
type PreviewCell struct {
	Answerer string
	Track    answer.Track
	Planned  int
	Extra    int
}

// Preview plans a run over the set without creating it.
func (e *Engine) Preview(ctx context.Context, set sets.Set) (Preview, error) {
	snap := NewSnapshot(e.Cfg, set)
	if e.Cfg.Sampling.Adaptive && set.Tier != sets.PLG && len(snap.Answerers) > 0 {
		sp, err := e.plan(ctx, set, snap)
		if err != nil {
			return Preview{}, err
		}
		snap.Sampling = sp
	}
	p := Preview{Snapshot: snap}
	for _, a := range snap.Answerers {
		for _, t := range []answer.Track{answer.Search, answer.Recall} {
			c := PreviewCell{Answerer: a.ID, Track: t}
			for _, q := range set.Prompts {
				c.Planned += e.samples(set, a, t)
				c.Extra += snap.Sampling.Extra(q.ID, a.ID, t)
			}
			p.Cells = append(p.Cells, c)
		}
	}
	return p, nil
}
