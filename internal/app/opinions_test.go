package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/answer"
	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/grade"
	"github.com/cheapseatsdev/cheapseats/internal/typesafe"
)

// opinionsApp is providersApp with its four results re-graded for the second-opinion pass: two
// the fake Jev agrees with, one it reads differently, one it is unsure about.
func opinionsApp(t *testing.T) (*App, string, []int64) {
	t.Helper()
	a, id := providersApp(t)
	ctx := context.Background()
	res, err := a.Runs.Results(ctx, id)
	if err != nil || len(res) != 4 {
		t.Fatalf("results %d %v", len(res), err)
	}
	type fixture struct {
		text, evidence string
		verdict        grade.Verdict
	}
	fixtures := []fixture{
		{"Cheap Seats is the best place to start. Profound is the enterprise option.", "Cheap Seats is the best place to start.", grade.Recommended},
		{"Start with Scrunch. **Cheap Seats** also tracks AI answers, if you want something cheaper.", "Cheap Seats also tracks AI answers", grade.Mentioned},
		{"Cheap Seats is one of several options. Peec AI measures share of voice.", "Cheap Seats is one of several options.", grade.Recommended}, // generous: Jev will say mentioned
		{"Count how often you're named.", "", grade.Absent},
	}
	var ids []int64
	for i, f := range fixtures {
		g := grade.Grade{Verdict: f.verdict, Evidence: f.evidence, Named: []string{}}
		gj, _ := json.Marshal(g)
		aj, _ := json.Marshal(answer.Answer{Text: f.text})
		if _, err := a.DB.Exec(`UPDATE results SET grade=?, answer=?, verdict=? WHERE id=?`, string(gj), string(aj), string(f.verdict), res[i].ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res[i].ID)
	}
	return a, id, ids
}

// fakeJevOpinion answers the second-opinion questions by the answer's text, and keeps every request.
func fakeJevOpinion(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var asked []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		text := req["state"].(map[string]any)["answer"].(string)
		verdict, p := "absent", 0.55
		switch {
		case strings.HasPrefix(text, "Cheap Seats is the best"):
			verdict, p = "recommended", 0.9
		case strings.HasPrefix(text, "Start with Scrunch"):
			verdict, p = "mentioned", 0.85
		case strings.HasPrefix(text, "Cheap Seats is one of"):
			verdict, p = "mentioned", 0.8
		}
		probs := map[string]float64{"recommended": 0, "mentioned": 0, "absent": 0}
		for k := range probs {
			probs[k] = (1 - p) / 2
		}
		probs[verdict] = p
		answers := map[string]any{"verdict": map[string]any{"type": "choice", "choice": verdict, "confidence": 0.5, "probabilities": probs}}
		if _, ok := req["questions"].(map[string]any)["evidence"]; ok {
			answers["evidence"] = map[string]any{"type": "choice", "choice": "s0", "confidence": 0.9, "probabilities": map[string]float64{"s0": 0.93}}
		}
		// About what a real request costs: the state is the answer plus a few hundred tokens.
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.14.0", "answers": answers, "usage": map[string]any{"input_tokens": 2000, "output_tokens": 0}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv(typesafe.KeyVar, "")
	t.Setenv(config.TypeSafeBaseURLVar, srv.URL)
	return srv, &asked
}

func TestSecondOpinionsStoreJevsVerdictAndCompareWithTheCurrentGrade(t *testing.T) {
	a, id, ids := opinionsApp(t)
	_, asked := fakeJevOpinion(t)
	ctx := context.Background()

	n, err := a.SecondOpinions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || len(*asked) != 4 {
		t.Fatalf("asked %d answers in %d requests", n, len(*asked))
	}
	// Each request: the brand as the grader knew it, the question and the answer, one answer
	// per request, under the grader's own definitions.
	for i, req := range *asked {
		st := req["state"].(map[string]any)
		if st["brand"] != "Cheap Seats" || st["about"] == "" || st["question"] == "" || st["answer"] == "" || st["aliases"] == nil {
			t.Errorf("request %d state should carry brand, aliases, about, question and answer: %v", i, st)
		}
		if !strings.HasPrefix(st["category"].(string), "An AI visibility tracker.") {
			t.Errorf("request %d category: %v", i, st["category"])
		}
		qs := req["questions"].(map[string]any)
		crit := qs["verdict"].(map[string]any)["criteria"].(map[string]any)
		for _, v := range grade.Verdicts {
			if crit[string(v)] != grade.Definitions[v] || !strings.Contains(grade.System, grade.Definitions[v]) {
				t.Errorf("request %d: the criteria for %s should be the grader's own definition: %v", i, v, crit[string(v)])
			}
		}
		ev, hasEvidence := qs["evidence"].(map[string]any)
		switch i {
		case 3:
			if hasEvidence {
				t.Error("an absent grade gets no evidence question")
			}
		case 1:
			// Only the sentences naming the brand are offered, with the answer's own words.
			sents := st["sentences"].([]any)
			if !hasEvidence || len(sents) != 1 || sents[0] != "**Cheap Seats** also tracks AI answers, if you want something cheaper." {
				t.Errorf("request %d evidence options: %v", i, sents)
			}
			if c := ev["criteria"].(map[string]any); len(c) != 1 || !strings.Contains(c["s0"].(string), "`sentences[0]`") {
				t.Errorf("request %d evidence criteria: %v", i, c)
			}
		default:
			if !hasEvidence {
				t.Errorf("request %d should ask for the evidence", i)
			}
		}
	}

	// What's stored, read back against the current grades: agreed 2, disagreed 1, unsure 1.
	res, _ := a.Runs.Results(ctx, id)
	ops := a.Opinions(ctx, id, res)
	if len(ops) != 4 {
		t.Fatalf("opinions %d", len(ops))
	}
	want := []struct {
		verdict   grade.Verdict
		p         float64
		agreement string
		evidence  string
		match     string
	}{
		{grade.Recommended, 0.9, Agrees, "Cheap Seats is the best place to start.", "same"},
		{grade.Mentioned, 0.85, Agrees, "**Cheap Seats** also tracks AI answers, if you want something cheaper.", "same"},
		{grade.Mentioned, 0.8, Disagrees, "Cheap Seats is one of several options.", "same"},
		{grade.Absent, 0.55, Unsure, "", "none"},
	}
	for i, w := range want {
		o := ops[ids[i]]
		if o == nil || o.Verdict != w.verdict || o.Confidence != w.p || o.Agreement != w.agreement || o.Evidence != w.evidence || o.EvidenceMatch != w.match || o.Model != "jev-1.14.0" || o.Regraded {
			t.Errorf("result %d: %+v", i, o)
		}
		if i < 3 && o.EvidenceConfidence != 0.93 {
			t.Errorf("result %d: the evidence's probability is the chosen option's: %v", i, o.EvidenceConfidence)
		}
	}
	ag := Agree(ops, res)
	if ag.Agreed != 2 || ag.Disagreed != 1 || ag.Unsure != 1 || ag.EvidenceDiffers != 0 || ag.Rate != 2.0/3 || len(ag.Disagreements) != 1 {
		t.Fatalf("agreement %+v", ag)
	}
	if d := ag.Disagreements[0]; d.ResultID != ids[2] || d.Grader != grade.Recommended || d.Jev != grade.Mentioned || d.P != 0.8 || d.N != 2 || d.Answerer != "claude" {
		t.Errorf("disagreement %+v", d)
	}
	hist, err := a.AgreementHistory(ctx, "s")
	if err != nil || len(hist) != 1 || hist[id].Agreed != 2 || hist[id].Disagreed != 1 || hist[id].Unsure != 1 || hist[id].Rate != 2.0/3 {
		t.Errorf("history %+v %v", hist, err)
	}
	// Cost: a 60-answer run is logged under a cent.
	var spent float64
	a.DB.QueryRow(`SELECT COALESCE(SUM(amount_usd),0) FROM spend_events WHERE kind LIKE 'provider-attempt/second-opinions/%'`).Scan(&spent)
	if spent <= 0 || spent/4*60 >= 0.01 {
		t.Errorf("four answers cost %f in the ledger; sixty would be %f", spent, spent/4*60)
	}

	// Asking again asks nothing: every answer has an opinion.
	if n, err := a.SecondOpinions(ctx, id); err != nil || n != 0 || len(*asked) != 4 {
		t.Errorf("second pass asked %d (%v), %d requests", n, err, len(*asked))
	}
	// A re-grade changes the comparison, not the row, and asks nothing: the grader now agrees
	// with Jev on the third answer.
	g := grade.Grade{Verdict: grade.Mentioned, Evidence: "Peec AI measures share of voice.", Named: []string{}}
	gj, _ := json.Marshal(g)
	if _, err := a.DB.Exec(`UPDATE results SET grade=?, verdict=? WHERE id=?`, string(gj), "mentioned", ids[2]); err != nil {
		t.Fatal(err)
	}
	res, _ = a.Runs.Results(ctx, id)
	ops = a.Opinions(ctx, id, res)
	if o := ops[ids[2]]; o.Agreement != Agrees || !o.Regraded || o.EvidenceMatch != "differs" {
		t.Errorf("after the re-grade: %+v", o)
	}
	if ag := Agree(ops, res); ag.Agreed != 3 || ag.Disagreed != 0 || ag.Unsure != 1 || ag.EvidenceDiffers != 1 || len(*asked) != 4 {
		t.Errorf("after the re-grade: %+v, %d requests", ag, len(*asked))
	}
	if hist, _ := a.AgreementHistory(ctx, "s"); hist[id].Agreed != 3 || hist[id].Disagreed != 0 {
		t.Errorf("history after the re-grade: %+v", hist[id])
	}
	// A stricter threshold re-reads the same rows: everything under it is unsure.
	a.Cfg.TypeSafe.MinConfidence = 0.95
	if ag := Agree(a.Opinions(ctx, id, res), res); ag.Agreed != 0 || ag.Disagreed != 0 || ag.Unsure != 4 || len(*asked) != 4 {
		t.Errorf("at 0.95: %+v", ag)
	}
	if hist, _ := a.AgreementHistory(ctx, "s"); hist[id].Unsure != 4 {
		t.Errorf("history at 0.95: %+v", hist[id])
	}
}

func TestSecondOpinionsPassIsRegistered(t *testing.T) {
	p, ok := PassByCommand("verdicts")
	if !ok || p.Name != "second-opinions" || p.Order != 35 || p.Provider != config.TypeSafeProvider || p.Print == nil {
		t.Fatalf("%+v %v", p, ok)
	}
	if !p.On(&config.Config{TypeSafe: &config.TypeSafe{}}) || p.On(&config.Config{}) {
		t.Error("on with [typesafe], off without")
	}
}

func TestSentencesAndTheAnswerCap(t *testing.T) {
	text := "- **Cheap Seats** is free. Try it!\nProfound costs more... Is cheapseats.dev any good? Yes.\n1. CheapSeats, again"
	got := sentences(text)
	want := []string{"**Cheap Seats** is free.", "Try it!", "Profound costs more...", "Is cheapseats.dev any good?", "Yes.", "CheapSeats, again"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("sentences %q", got)
	}
	if s := sentencesNaming(text, []string{"Cheap Seats", "cheapseats"}, 2); len(s) != 2 || s[0] != want[0] || s[1] != want[3] {
		t.Errorf("naming the brand, capped at two: %q", s)
	}
	long := strings.Repeat("Cheap Seats is good. ", 1000) // 21,000 runes
	cut, was := capAnswer(long, 12000)
	if !was || len(cut) > 12000 || !strings.HasSuffix(cut, "good.") {
		t.Errorf("cut %d runes, at a sentence end: %v %q", len(cut), was, cut[len(cut)-20:])
	}
	if s, was := capAnswer("short", 12000); was || s != "short" {
		t.Error("a short answer is left alone")
	}
	for _, c := range []struct{ jev, grader, want string }{
		{"**Cheap Seats** is the  free place to start.", "cheap seats is the free place to start", "same"},
		{"Cheap Seats is the free place to start, and more.", "Cheap Seats is the free place", "same"},
		{"Cheap Seats is free.", "Profound is pricey.", "differs"},
		{"", "Profound is pricey.", "none"},
	} {
		if got := evidenceMatch(c.jev, c.grader); got != c.want {
			t.Errorf("evidenceMatch(%q, %q) = %s", c.jev, c.grader, got)
		}
	}
}
