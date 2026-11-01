package app

import (
	"context"
	"maps"
	"strings"
	"testing"
)

// foldOrderBuilds rebuilds a folding map n times, so a map that depends on Go's randomised map
// iteration order shows up as two different builds.
func foldOrderBuilds(t *testing.T, n int, build func() map[string]string) map[string]string {
	t.Helper()
	first := build()
	for i := 1; i < n; i++ {
		if got := build(); !maps.Equal(got, first) {
			t.Fatalf("build %d differs from the first:\n first %v\n   now %v", i, first, got)
		}
	}
	return first
}

// Production: across runs of one binary on one database, a grader-written "Copilot" landed on
// "copilot chat" about half the time and on a row of its own the rest. The board's folding map
// resolved chains of decisions by ranging over the map while rewriting it, so a chain of three
// folded to whichever link the iteration order reached first.
func TestAliasesResolveChainsAndCyclesTheSameEveryTime(t *testing.T) {
	a, _ := providersApp(t)
	for _, d := range [][3]string{
		// A chain: microsoft copilot -> copilot -> github copilot -> copilot chat.
		{"microsoft copilot", "copilot", "Copilot"},
		{"copilot", "github copilot", "GitHub Copilot"},
		{"github copilot", "copilot chat", "copilot chat"},
		// A cycle: each was decided to be the other, at the same confidence. One row, under the
		// display that sorts first.
		{"peec", "peec ai", "Peec AI"},
		{"peec ai", "peec", "Peec"},
		// Into the cycle from outside: folds onto the cycle's row.
		{"peec.ai", "peec ai", "Peec AI"},
		// A configured competitor's own name is never folded away by a decision.
		{"profound", "profound ai", "Profound AI"},
	} {
		if _, err := a.DB.Exec(`INSERT INTO name_decisions(variant, candidate, display, confidence, source, decided_at) VALUES (?,?,?,0.95,'jev','2026-10-27T00:00:00Z')`, d[0], d[1], d[2]); err != nil {
			t.Fatal(err)
		}
	}
	got := foldOrderBuilds(t, 200, func() map[string]string {
		a.invalidateAliases()
		return maps.Clone(a.Aliases())
	})
	want := map[string]string{
		"microsoft copilot": "copilot chat",
		"copilot":           "copilot chat",
		"github copilot":    "copilot chat",
		"peec":              "Peec",
		"peec ai":           "Peec",
		"peec.ai":           "Peec",
		"tryprofound":       "Profound",
	}
	if !maps.Equal(got, want) {
		t.Errorf("aliases\n got %v\nwant %v", got, want)
	}
}

// Phrase clusters were resolved the same way, so a chain of clusters counted an objection under
// a wording that depended on iteration order.
func TestPhraseClustersResolveChainsTheSameEveryTime(t *testing.T) {
	a, _ := providersApp(t)
	for _, d := range [][4]string{
		{"pricey", "expensive", "Expensive"},
		{"expensive", "costly", "Costly"},
		{"costly", "high price", "High price"},
		{"high price", "", "High price"}, // its own cluster, under its first wording
	} {
		if _, err := a.DB.Exec(`INSERT INTO phrase_clusters(kind, phrase, cluster, display, confidence, source, decided_at) VALUES (?,?,?,?,0.95,'jev','2026-10-27T00:00:00Z')`, objectionKind, d[0], d[1], d[2]); err != nil {
			t.Fatal(err)
		}
	}
	got := foldOrderBuilds(t, 200, func() map[string]string {
		a.invalidatePhrases()
		return maps.Clone(a.PhraseClusters(objectionKind))
	})
	want := map[string]string{"pricey": "High price", "expensive": "High price", "costly": "High price", "high price": "High price"}
	if !maps.Equal(got, want) {
		t.Errorf("clusters\n got %v\nwant %v", got, want)
	}
}

// foldCirclesDecide stores name decisions: variant, candidate, display and confidence.
func foldCirclesDecide(t *testing.T, a *App, ds []foldCirclesDecision) {
	t.Helper()
	for _, d := range ds {
		if _, err := a.DB.Exec(`INSERT INTO name_decisions(variant, candidate, display, confidence, source, decided_at) VALUES (?,?,?,?,'jev','2026-10-27T00:00:00Z')`, d.variant, d.candidate, d.display, d.conf); err != nil {
			t.Fatal(err)
		}
	}
}

type foldCirclesDecision struct {
	variant, candidate, display string
	conf                        float64
}

// Shapes from production name_decisions: two names decided onto each other agree they are one
// product and differ only on its spelling. Left apart, an answer set using both spellings put the
// product on two rows and split its share of voice. Every name on a circle, and every name
// decided onto one, now lands on one row under the circle's highest-confidence display.
var foldCirclesFixtures = []foldCirclesDecision{
	{"goodie", "goodie ai", "Goodie AI", 0.84},
	{"goodie ai", "goodie", "Goodie", 0.89},
	{"postgres", "postgresql", "PostgreSQL", 0.75},
	{"postgresql", "postgres", "Postgres", 0.80},
	{"gracker", "gracker.ai", "gracker.ai", 0.92},
	{"gracker.ai", "gracker", "Gracker", 0.85},
	{"grackerai", "gracker", "Gracker", 0.83},
	{"se visible", "se ranking's se visible", "SE Ranking's SE Visible", 0.86},
	{"se ranking's se visible", "se visible", "SE Visible", 0.73},
	{"sevisible", "se visible", "SE Visible", 0.90},
	{"se visible ai", "se visible", "SE Visible", 0.81},
	// A circle of three. "Otterly AI" and "Otterly" tie at 0.9; "Otterly" sorts first.
	{"otterly", "otterly.ai", "Otterly.AI", 0.80},
	{"otterly.ai", "otterly ai", "Otterly AI", 0.90},
	{"otterly ai", "otterly", "Otterly", 0.90},
	// A chain that runs into a circle.
	{"otterly tool", "otterly hq", "Otterly HQ", 0.80},
	{"otterly hq", "otterly.ai", "Otterly.AI", 0.80},
	// Touching a configured competitor (Profound, alias tryprofound): configuration wins over a
	// more confident decision. The decision about the competitor's own name is not read, so
	// "profound ai" folds onto Profound rather than the circle choosing "Profound AI".
	{"profound", "profound ai", "Profound AI", 0.95},
	{"profound ai", "profound", "Profound", 0.80},
	{"tryprofound", "try profound", "Try Profound", 0.99},
	{"try profound", "tryprofound", "tryprofound", 0.90},
}

func TestAliasesFoldCirclesOntoOneRow(t *testing.T) {
	a, _ := providersApp(t)
	foldCirclesDecide(t, a, foldCirclesFixtures)
	got := foldOrderBuilds(t, 200, func() map[string]string {
		a.invalidateAliases()
		return maps.Clone(a.Aliases())
	})
	want := map[string]string{
		"goodie":                  "Goodie",
		"goodie ai":               "Goodie",
		"postgres":                "Postgres",
		"postgresql":              "Postgres",
		"gracker":                 "gracker.ai",
		"gracker.ai":              "gracker.ai",
		"grackerai":               "gracker.ai",
		"se visible":              "SE Ranking's SE Visible",
		"se ranking's se visible": "SE Ranking's SE Visible",
		"sevisible":               "SE Ranking's SE Visible",
		"se visible ai":           "SE Ranking's SE Visible",
		"otterly":                 "Otterly",
		"otterly.ai":              "Otterly",
		"otterly ai":              "Otterly",
		"otterly tool":            "Otterly",
		"otterly hq":              "Otterly",
		"profound ai":             "Profound",
		"tryprofound":             "Profound",
		"try profound":            "Profound",
	}
	if !maps.Equal(got, want) {
		t.Errorf("aliases\n got %v\nwant %v", got, want)
	}
}

// A circle of configured entries (one competitor's alias is another's name, in either order) is
// named by configuration before any decision, and two configured entries by sort order.
func TestResolveFoldsConfiguredOutranksDecisions(t *testing.T) {
	got := resolveFolds(map[string]fold{
		"a": {display: "B", confidence: 0.99},
		"b": {display: "C", configured: true},
		"c": {display: "a", confidence: 0.99},
	}, strings.ToLower)
	want := map[string]string{"a": "C", "b": "C", "c": "C"}
	if !maps.Equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestPhraseClustersFoldCirclesOntoOneWording(t *testing.T) {
	a, _ := providersApp(t)
	for _, d := range []struct {
		phrase, cluster, display string
		conf                     float64
	}{
		{"pricey", "expensive", "Expensive", 0.80},
		{"expensive", "too costly", "Too costly", 0.90},
		{"too costly", "pricey", "Pricey", 0.85},
		{"steep price", "pricey", "Pricey", 0.95},
	} {
		if _, err := a.DB.Exec(`INSERT INTO phrase_clusters(kind, phrase, cluster, display, confidence, source, decided_at) VALUES (?,?,?,?,?,'jev','2026-10-27T00:00:00Z')`, objectionKind, d.phrase, d.cluster, d.display, d.conf); err != nil {
			t.Fatal(err)
		}
	}
	got := foldOrderBuilds(t, 200, func() map[string]string {
		a.invalidatePhrases()
		return maps.Clone(a.PhraseClusters(objectionKind))
	})
	want := map[string]string{"pricey": "Too costly", "expensive": "Too costly", "too costly": "Too costly", "steep price": "Too costly"}
	if !maps.Equal(got, want) {
		t.Errorf("clusters\n got %v\nwant %v", got, want)
	}
}

// The fold pass's listing said a decision folded whenever it cleared the threshold, though the
// board ignores a decision about a configured name and follows chains and circles past the
// decision's own display.
func TestNameDecisionsListWhatTheBoardDoes(t *testing.T) {
	a, _ := providersApp(t)
	foldCirclesDecide(t, a, append(foldCirclesFixtures,
		foldCirclesDecision{"goodie", "goodie inc", "Goodie Inc", 0.50},
		foldCirclesDecision{"gracker", "gracker.io", "Gracker.io", 0.92},
	))
	ds, err := a.NameDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		folds         bool
		foldsTo, note string
	}
	got := map[[2]string]row{}
	for _, d := range ds {
		got[[2]string{d.Variant, d.Candidate}] = row{d.Folds, d.FoldsTo, d.note()}
	}
	for k, want := range map[[2]string]row{
		{"goodie", "goodie ai"}:        {true, "Goodie", "  (on the board as Goodie)"},
		{"goodie ai", "goodie"}:        {true, "Goodie", ""},
		{"gracker.ai", "gracker"}:      {true, "gracker.ai", "  (on the board as gracker.ai)"},
		{"otterly tool", "otterly hq"}: {true, "Otterly", "  (on the board as Otterly)"},
		{"goodie", "goodie inc"}:       {false, "Goodie", "  (not read: below the threshold; on the board as Goodie)"},
		// Two decisions at one confidence: the candidate that sorts first is read.
		{"gracker", "gracker.io"}:       {false, "gracker.ai", "  (not read: another decision for this name is read; on the board as gracker.ai)"},
		{"profound", "profound ai"}:     {false, "", "  (not read: a configured competitor's name; keeps its own row)"},
		{"tryprofound", "try profound"}: {false, "Profound", "  (not read: a configured alias of Profound; on the board as Profound)"},
	} {
		if got[k] != want {
			t.Errorf("%v: got %+v, want %+v", k, got[k], want)
		}
	}
}
