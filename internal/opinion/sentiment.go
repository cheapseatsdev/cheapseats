// Package opinion is what a run's answers say about the brand, beyond whether they name it: the
// tone they take (Sentiment, counted from the grades) and the themes their objections, praise and
// reasons for picking something else fall into (Themes, one grader call a run).
package opinion

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

// Sentiment is the tone of the answers that name the brand.
type Sentiment struct {
	// Answers naming the brand, and how many of those have a tone (grades from before tone was
	// graded have none). The shares and Net are over Toned.
	Named int `json:"named"`
	Toned int `json:"toned"`
	// Answers per tone, in grade.Tones order.
	Tones []ToneCount `json:"tones"`
	// Net sentiment: the share positive minus the share negative, -1 to 1; nil when no answer
	// naming the brand has a tone.
	Net *float64 `json:"net"`
	// The strengths and objections the answers name most often, most frequent first.
	Praise     []Count `json:"praise"`
	Objections []Count `json:"objections"`
}

type ToneCount struct {
	Tone  grade.Tone `json:"tone"`
	N     int        `json:"n"`
	Share float64    `json:"share"`
}

// Count is how many answers said one thing, under the first wording seen.
type Count struct {
	Text string `json:"text"`
	N    int    `json:"n"`
}

// top is how many praise and objection strings Sentiment keeps.
const top = 5

// Summarize counts tone, praise and objections over graded answers; a nil grade is skipped.
func Summarize(grades []*grade.Grade) Sentiment {
	s := Sentiment{Tones: []ToneCount{}}
	per := map[grade.Tone]int{}
	var praise, objections counter
	for _, g := range grades {
		if g == nil || g.Verdict == grade.Absent || g.Verdict == "" {
			continue
		}
		s.Named++
		if g.Tone != "" {
			s.Toned++
			per[g.Tone]++
		}
		praise.add(g.Praise)
		objections.add(g.Objections)
	}
	for _, t := range grade.Tones {
		c := ToneCount{Tone: t, N: per[t]}
		if s.Toned > 0 {
			c.Share = float64(c.N) / float64(s.Toned)
		}
		s.Tones = append(s.Tones, c)
	}
	if s.Toned > 0 {
		net := float64(per[grade.Positive]-per[grade.Negative]) / float64(s.Toned)
		s.Net = &net
	}
	s.Praise, s.Objections = praise.top(top), objections.top(top)
	return s
}

// NetText is Net in percentage points, signed ("+40", "-15", "0"), or "n/a" with nothing to go on.
func (s Sentiment) NetText() string {
	if s.Net == nil {
		return "n/a"
	}
	n := int(math.Round(*s.Net * 100))
	if n > 0 {
		return fmt.Sprintf("+%d", n)
	}
	return fmt.Sprint(n)
}

// counter counts strings by their folded form, once per answer, keeping the first wording.
type counter struct {
	n     map[string]int
	text  map[string]string
	order []string
}

func (c *counter) add(items []string) {
	if c.n == nil {
		c.n, c.text = map[string]int{}, map[string]string{}
	}
	seen := map[string]bool{}
	for _, it := range items {
		k := fold(it)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if _, ok := c.n[k]; !ok {
			c.text[k] = strings.Join(strings.Fields(it), " ")
			c.order = append(c.order, k)
		}
		c.n[k]++
	}
}

func (c *counter) top(n int) []Count {
	out := []Count{}
	for _, k := range c.order {
		out = append(out, Count{c.text[k], c.n[k]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].N > out[j].N })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// fold is a phrase for counting, as the run page's objections count it.
func fold(s string) string { return grade.Fold(s) }

// Report is a run's opinion: the sentiment counted from its grades, and its theme pass, if it had
// one.
type Report struct {
	Sentiment Sentiment `json:"sentiment"`
	Themes    *Themes   `json:"themes,omitempty"`
}
