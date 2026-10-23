package state

import (
	"strings"
	"testing"
)

type status string

var table = Table[status]{Start: {"a"}, "a": {"b"}, "b": nil}

// Check tells Unlisted of a move the table doesn't have, and of nothing else: the owning packages'
// tests fail on what it reports, so a false report there would be a false failure.
func TestCheckReportsOnlyUnlistedMoves(t *testing.T) {
	var reported []string
	old := Unlisted
	Unlisted = func(machine, from, to string) { reported = append(reported, machine+":"+from+">"+to) }
	defer func() { Unlisted = old }()
	table.Check("m", Start, "a")
	table.Check("m", "a", "b")
	table.Check("m", "b", "b") // staying put is not a move
	table.Check("m", "b", "a")
	table.Check("m", "zzz", "a") // a status the table never heard of
	if got := strings.Join(reported, " "); got != "m:b>a m:zzz>a" {
		t.Fatalf("reported %q", got)
	}
}

func TestMermaidDrawsEveryMoveOnce(t *testing.T) {
	want := "```mermaid\nstateDiagram-v2\n    [*] --> a\n    a --> b\n```\n"
	if got := table.Mermaid([]status{"a", "b"}); got != want {
		t.Fatalf("got\n%s", got)
	}
	if got := strings.Join(table.Edges([]status{"a", "b"}), ","); got != "start → a,a → b" {
		t.Fatalf("edges %q", got)
	}
}
