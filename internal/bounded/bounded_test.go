package bounded

import (
	"errors"
	"strings"
	"testing"
)

// boundedEndless is a reader that never ends, so a Read that forgot its limit never returns.
type boundedEndless struct{ n int }

func (e *boundedEndless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	e.n += len(p)
	return len(p), nil
}

func TestReadStopsAtTheLimit(t *testing.T) {
	if got, err := Read(strings.NewReader("12345"), 5); err != nil || string(got) != "12345" {
		t.Fatalf("a body exactly at the limit is read whole: %q %v", got, err)
	}
	r := &boundedEndless{}
	got, err := Read(r, 1000)
	if !errors.Is(err, ErrTooLarge) || got != nil {
		t.Fatalf("an endless body is an error: %d bytes, %v", len(got), err)
	}
	if r.n > 1001 {
		t.Errorf("read %d bytes of an endless body; the limit is 1000", r.n)
	}
}

func TestPrefixReadsOnlyTheStart(t *testing.T) {
	r := &boundedEndless{}
	if got := Prefix(r); len(got) != ErrorPrefix {
		t.Fatalf("prefix is %d bytes", len(got))
	}
	if r.n > ErrorPrefix {
		t.Errorf("read %d bytes for a %d-byte prefix", r.n, ErrorPrefix)
	}
}
