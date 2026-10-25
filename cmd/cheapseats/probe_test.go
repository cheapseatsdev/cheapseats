package main

import "testing"

// A search result's title or URL cannot reach the terminal as an escape sequence or a line break.
func TestProbePrintable(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b]0;owned\x07Docs":       "�]0;owned�Docs", // OSC: retitles the window
		"\x1b[2J\x1b[HCheap Seats":   "�[2J�[HCheap Seats",
		"title\n  2. https://forged": "title�  2. https://forged",
		"\u009b31m red":              "�31m red", // C1 CSI, one rune
		"\x9b31m raw byte":           "�31m raw byte",
		"carriage\rreturn\x7f":       "carriage�return�",
		"Cheap Seats: café, 日本":      "Cheap Seats: café, 日本",
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}
