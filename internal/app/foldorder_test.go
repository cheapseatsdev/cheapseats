package app

import (
	"maps"
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
		// A cycle: each was decided to be the other. Neither is the name to show.
		{"peec", "peec ai", "Peec AI"},
		{"peec ai", "peec", "Peec"},
		// Into the cycle from outside: folds onto the name it was decided to be.
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
		"peec.ai":           "Peec AI",
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
