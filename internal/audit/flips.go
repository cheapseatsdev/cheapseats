package audit

// PageFlip is a cited page whose reading changed between two citation checks: it started or
// stopped naming the brand, or a competitor. Only a page both checks read is compared, so a page
// that couldn't be fetched once never reads as a change.
type PageFlip struct {
	URL                string   `json:"url"`
	NowNamesBrand      bool     `json:"now_names_brand,omitempty"`
	StoppedNamingBrand bool     `json:"stopped_naming_brand,omitempty"`
	NowNames           []string `json:"now_names,omitempty"`      // competitors it names now and didn't
	StoppedNaming      []string `json:"stopped_naming,omitempty"` // competitors it named and doesn't
	SinceRun           string   `json:"since_run"`                // the run whose check it is compared with
}

// Chips is the flip in words, brand first: "now names you", "stopped naming Gauge".
func (f PageFlip) Chips() []string {
	var out []string
	if f.NowNamesBrand {
		out = append(out, "now names you")
	}
	if f.StoppedNamingBrand {
		out = append(out, "stopped naming you")
	}
	for _, c := range f.NowNames {
		out = append(out, "now names "+c)
	}
	for _, c := range f.StoppedNaming {
		out = append(out, "stopped naming "+c)
	}
	return out
}

// Flips is each page of cur that changed since prev, in cur's order.
func Flips(prev, cur CitationCheck) []PageFlip {
	before := map[string]CitedPage{}
	for _, p := range prev.Pages {
		if p.Skipped == "" {
			before[p.URL] = p
		}
	}
	var out []PageFlip
	for _, p := range cur.Pages {
		b, ok := before[p.URL]
		if !ok || p.Skipped != "" {
			continue
		}
		f := PageFlip{URL: p.URL, SinceRun: prev.RunID,
			NowNamesBrand: p.NamesBrand && !b.NamesBrand, StoppedNamingBrand: b.NamesBrand && !p.NamesBrand,
			NowNames: missingFrom(p.Competitors, b.Competitors), StoppedNaming: missingFrom(b.Competitors, p.Competitors)}
		if len(f.Chips()) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// missingFrom is the names in xs that aren't in ys.
func missingFrom(xs, ys []string) []string {
	in := map[string]bool{}
	for _, y := range ys {
		in[y] = true
	}
	var out []string
	for _, x := range xs {
		if !in[x] {
			out = append(out, x)
		}
	}
	return out
}
