// Package state is the shape of a status column: the statuses a row moves through and the moves
// the code makes between them, written down once so the code, its tests and docs/states.md agree.
// Each column's owner (run, budget, app) declares its statuses and its Table; this package only
// knows how to check a move against a table and draw it.
package state

import (
	"log/slog"
	"strings"
)

// Start is a row before it exists. A table's entry for it lists the statuses a row is born in.
const Start = ""

// Table lists, for each status, the statuses a row may move to next.
type Table[S ~string] map[S][]S

// CanBecome reports whether the table lists the move from one status to another. Staying put is
// not a move, so it's always allowed: a bulk update that sets a status some rows already have is
// fine.
func (t Table[S]) CanBecome(from, to S) bool {
	if from == to {
		return true
	}
	for _, s := range t[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Unlisted is told of a move the table doesn't list: a bug in the table or in the code. It logs;
// the tests of each owning package swap in a function that fails the test. The move itself is
// the caller's to make or not: the SQL guards the data as it always did.
var Unlisted = func(machine, from, to string) {
	slog.Warn("status move the table doesn't list", "machine", machine, "from", from, "to", to)
}

// Check reports the move to Unlisted if the table doesn't list it.
func (t Table[S]) Check(machine string, from, to S) {
	if !t.CanBecome(from, to) {
		Unlisted(machine, string(from), string(to))
	}
}

// Mermaid draws the table as a stateDiagram-v2 block, statuses in the order given (every status
// in the table must be in order, or the diagram would drop its moves).
func (t Table[S]) Mermaid(order []S) string {
	var b strings.Builder
	b.WriteString("```mermaid\nstateDiagram-v2\n")
	for _, to := range t[Start] {
		b.WriteString("    [*] --> " + string(to) + "\n")
	}
	for _, from := range order {
		for _, to := range t[from] {
			b.WriteString("    " + string(from) + " --> " + string(to) + "\n")
		}
	}
	b.WriteString("```\n")
	return b.String()
}

// Edges lists the table's moves as "from → to", Start's first, then in the order given, for
// tests that pair each move with the code that makes it.
func (t Table[S]) Edges(order []S) []string {
	var out []string
	for _, to := range t[Start] {
		out = append(out, "start → "+string(to))
	}
	for _, from := range order {
		for _, to := range t[from] {
			out = append(out, string(from)+" → "+string(to))
		}
	}
	return out
}
