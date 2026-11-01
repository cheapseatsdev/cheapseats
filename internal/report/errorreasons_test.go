package report

import (
	"reflect"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/run"
)

func TestSliceErrorKinds(t *testing.T) {
	failed := func(answerer string, track answer.Track, msg string) run.Result {
		return run.Result{Prompt: "p", Answerer: answerer, Track: track, Status: run.ResultError, Error: &msg}
	}
	res := []run.Result{
		// The text runs stored before Ask returned answer.ErrNoSearch, so old rows still match.
		failed("codex", answer.Search, "answered without searching, twice"),
		failed("codex", answer.Search, "HTTP 529: overloaded"),
		failed("codex", answer.Search, answer.ErrNoSearch.Error()),
		failed("claude", answer.Search, "HTTP 529: overloaded"),
		{Prompt: "p", Answerer: "clean", Track: answer.Search, Status: run.ResultDone},
	}
	s := Build(config.Brand{Name: "Cheap Seats"}, res)
	by := map[string]Slice{}
	for _, x := range s.Slices {
		by[x.Answerer] = x
	}
	want := []ErrorKind{{"answered without searching", 2}, {OtherError, 1}}
	if got := by["codex"]; got.Errors != 3 || !reflect.DeepEqual(got.ErrorKinds, want) {
		t.Fatalf("codex: %d errors, kinds %+v, want 3 and %+v", got.Errors, got.ErrorKinds, want)
	}
	if note := by["codex"].ErrorNote(); note != "(2 answered without searching, 1 other)" {
		t.Errorf("codex note = %q", note)
	}
	// Only provider errors: nothing to say beyond the count.
	if got := by["claude"]; got.ErrorNote() != "" || len(got.ErrorKinds) != 1 {
		t.Errorf("claude: kinds %+v, note %q", got.ErrorKinds, got.ErrorNote())
	}
	if got := by["clean"]; got.ErrorKinds != nil || got.ErrorNote() != "" {
		t.Errorf("clean slice has kinds %+v", got.ErrorKinds)
	}
}

func TestErrorReason(t *testing.T) {
	for msg, want := range map[string]string{
		"answered without searching, twice": "answered without searching",
		"empty provider answer":             "empty answer",
		run.GradingErrorPrefix + "HTTP 500": "grading failed",
		"codex: answered without searching": OtherError, // not the app's exact wording
		"context deadline exceeded":         OtherError,
		"":                                  OtherError,
	} {
		if got := ErrorReason(msg); got != want {
			t.Errorf("ErrorReason(%q) = %q, want %q", msg, got, want)
		}
	}
}
