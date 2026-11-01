package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/errclass"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// The live check's defaults. An accepted schema is billed for its input (the schema, which the API
// expands into a system prompt, and a one-line prompt) and at most liveMaxTokens of output: about a
// cent on the analyst's model. A refused one is an HTTP 400 before anything is generated.
const (
	defaultLiveBudget = 0.25
	liveMaxTokens     = 16
)

// liveSchemaCheck sends each schema once to the model of the role that sends it (cheapseats check
// -live), to catch what the offline llm.CheckSchema can't: the API refusing a schema the code was
// sure of, as v0.6.0's maxItems was refused. It runs only what this config sends through the API:
// a role on the claude CLI is noted and skipped. Spend is checked before each request against
// budget, so it can go over by at most one request's cost. Any refusal, error, or schema left
// unchecked is an error.
func liveSchemaCheck(ctx context.Context, cfg *config.Config, schemas []llm.NamedSchema, budget float64, w io.Writer) error {
	// flag.Float64 takes NaN and Inf. Against NaN, spent >= budget is never true, so either would
	// leave the check with no cap at all.
	if math.IsNaN(budget) || math.IsInf(budget, 0) || budget <= 0 {
		return fmt.Errorf("-live-budget must be a dollar amount more than 0, not %v", budget)
	}
	roles := map[string]config.Model{"grader": cfg.Grader, "analyst": cfg.Analyst}
	var problems []error
	spent, sent := 0.0, 0
	for i, s := range schemas {
		m, ok := roles[s.Role]
		if !ok {
			problems = append(problems, fmt.Errorf("%s: unknown role %q", s.Name, s.Role))
			continue
		}
		if m.Via() == config.ClaudeCode {
			fmt.Fprintf(w, "live: %s: skipped: the %s runs on the claude CLI, not the API\n", s.Name, s.Role)
			continue
		}
		if p, ok := cfg.Pricing[m.Model]; !ok || p.Input <= 0 || p.Output <= 0 {
			problems = append(problems, fmt.Errorf("%s: model %q has no [pricing] entry, so the live check can't keep to its budget", s.Name, m.Model))
			continue
		}
		if spent >= budget {
			problems = append(problems, fmt.Errorf("live check stopped at its budget ($%.4f of $%.2f): %d schemas not sent; raise -live-budget", spent, budget, len(schemas)-i))
			break
		}
		u, err := llm.ProbeSchema(ctx, m.Model, s.Schema, liveMaxTokens)
		sent++
		cost := cfg.Cost(m.Model, u)
		spent += cost
		var api *errclass.Error
		switch {
		case err == nil:
			fmt.Fprintf(w, "live: %s (%s, %s): accepted ($%.4f)\n", s.Name, s.Role, m.Model, cost)
		case errors.As(err, &api) && api.Category == "http_400":
			fmt.Fprintf(w, "live: %s (%s, %s): REFUSED: %v\n", s.Name, s.Role, m.Model, err)
			problems = append(problems, fmt.Errorf("%s: the API refused the schema: %w", s.Name, err))
		default:
			fmt.Fprintf(w, "live: %s (%s, %s): not checked: %v\n", s.Name, s.Role, m.Model, err)
			problems = append(problems, fmt.Errorf("%s: %w", s.Name, err))
		}
	}
	if sent == 0 && len(problems) == 0 {
		return errors.New("live check: no schema goes through the API with this config, so nothing was checked")
	}
	fmt.Fprintf(w, "live: %d of %d schemas sent, $%.4f of a $%.2f budget\n", sent, len(schemas), spent, budget)
	return errors.Join(problems...)
}
