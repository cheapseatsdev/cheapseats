package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/app"
)

// Competitor discovery (docs/optional-providers.md). The API reads the stored suggestions and
// never calls a provider; the discovery itself runs from cheapseats discover or the
// suggest_competitors tool.

// competitorSuggestions is GET /api/competitors/suggestions and the tool's answer: the
// suggestions in order, and the same as TOML to paste.
type competitorSuggestions struct {
	Suggestions []app.Suggestion `json:"suggestions"`
	TOML        string           `json:"toml"`
}

func (s *Server) apiCompetitorSuggestions(w http.ResponseWriter, r *http.Request) {
	ss, err := s.App.CompetitorSuggestions(r.Context())
	if err != nil {
		httpErr(w, err, 500)
		return
	}
	writeJSON(w, competitorSuggestions{Suggestions: ss, TOML: app.SuggestionsTOML(ss)})
}

// apiDismissSuggestion is POST /api/competitors/suggestions/{host}/dismiss: the host leaves the
// suggestions and stays out through later refreshes.
func (s *Server) apiDismissSuggestion(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if err := s.App.DismissSuggestion(r.Context(), host); err != nil {
		code := 500
		if strings.HasPrefix(err.Error(), "no suggestion") {
			code = 404
		}
		httpErr(w, err, code)
		return
	}
	writeJSON(w, map[string]any{"host": host, "dismissed": true})
}

// suggestCompetitorsTop is how many suggestions suggest_competitors returns.
const suggestCompetitorsTop = 10

// suggestCompetitors runs the discovery and returns the top suggestions with their reasons.
func (s *Server) suggestCompetitors(ctx context.Context) (any, error) {
	d, err := s.App.DiscoverCompetitors(ctx)
	if err != nil {
		return nil, err
	}
	ss, err := s.App.CompetitorSuggestions(ctx)
	if err != nil {
		return nil, err
	}
	total := len(ss)
	if len(ss) > suggestCompetitorsTop {
		ss = ss[:suggestCompetitorsTop]
	}
	return struct {
		app.Discovered
		Total int `json:"total"`
		competitorSuggestions
	}{d, total, competitorSuggestions{Suggestions: ss, TOML: app.SuggestionsTOML(ss)}}, nil
}
