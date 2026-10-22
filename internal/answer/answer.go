// Package answer asks an assistant a question the way a developer would, and records what it said
// and every page it searched, opened or cited on the way.
//
// Two tracks:
//   - search: the assistant must search the web before answering (one retry, then the answer
//     fails). This measures what the web says about the brand today.
//   - recall: no tools. This measures what the model already believes.
package answer

import (
	"context"
	"fmt"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

type Track string

const (
	Search Track = "search"
	Recall Track = "recall"
)

type Link struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

type Query struct {
	Query     string `json:"query"`
	Error     string `json:"error,omitempty"`
	Completed bool   `json:"completed"`
	Results   []Link `json:"results,omitempty"`
}

type Answer struct {
	Model     string       `json:"model,omitempty"`
	Text      string       `json:"text"`
	Searches  []Query      `json:"searches,omitempty"`
	Fetches   []string     `json:"fetches,omitempty"`
	Citations []Link       `json:"citations,omitempty"`
	Usage     config.Usage `json:"usage"`
	Retried   bool         `json:"retried,omitempty"`
}

// System is the system prompt an assistant answers under: a plain chat assistant, nothing about
// the brand. audience is brand.audience ("a software developer"), or empty for anyone.
func System(audience string) string {
	who := "a question"
	if audience != "" {
		who = audience + "'s question"
	}
	return "You are a helpful assistant answering " + who + ". Give a direct, practical answer. When you recommend tools or products, name them and say why."
}

const SearchPolicy = "Search the web before answering so your answer reflects what is current."

// Ask runs one prompt on one answerer and track, under the system prompt sys (from System).
func Ask(ctx context.Context, a config.Answerer, track Track, sys, prompt string) (Answer, error) {
	ask := askAnthropic
	switch a.Provider {
	case config.OpenAI:
		ask = askOpenAI
	case config.ClaudeCode:
		ask = askClaudeCode
	case config.Codex:
		ask = askCodex
	}
	ans, err := ask(ctx, a.Model, track, sys, prompt, false)
	if err != nil {
		return ans, err
	}
	if track == Search && !ans.Searched() {
		first := ans.Usage
		ans, err = ask(ctx, a.Model, track, sys, prompt, true)
		ans.Usage.Add(first)
		ans.Retried = true
		if err != nil {
			return ans, err
		}
		if !ans.Searched() {
			return ans, fmt.Errorf("answered without searching, twice")
		}
	}
	if strings.TrimSpace(ans.Text) == "" {
		return ans, fmt.Errorf("empty provider answer")
	}
	return ans, nil
}

func askAnthropic(ctx context.Context, model string, track Track, sys, prompt string, insist bool) (Answer, error) {
	body := map[string]any{
		"model":      model,
		"max_tokens": 4096,
		"messages":   []any{map[string]any{"role": "user", "content": prompt}},
	}
	if track == Search {
		sys += " " + SearchPolicy
		if insist {
			sys += " You must use the web search tool at least once before you answer."
		}
		body["tools"] = []any{map[string]any{"type": "web_search_20260318", "name": "web_search", "max_uses": 6}}
	}
	body["system"] = sys
	resp, err := llm.Anthropic(ctx, body)
	if err != nil {
		return Answer{}, err
	}
	return ParseAnthropic(resp), nil
}

// ParseAnthropic reads the answer text, searches and citations out of a Messages response.
func ParseAnthropic(resp map[string]any) Answer {
	var a Answer
	a.Model = llm.Str(resp["model"])
	blocks := llm.List(resp["content"])
	queries := map[string]int{} // tool_use id -> index in a.Searches
	var text strings.Builder
	seenCite := map[string]bool{}
	for _, raw := range blocks {
		b := llm.Map(raw)
		switch llm.Str(b["type"]) {
		case "server_tool_use":
			in := llm.Map(b["input"])
			switch llm.Str(b["name"]) {
			case "web_search":
				queries[llm.Str(b["id"])] = len(a.Searches)
				a.Searches = append(a.Searches, Query{Query: llm.Str(in["query"])})
			case "web_fetch":
				a.Fetches = append(a.Fetches, llm.Str(in["url"]))
			}
		case "web_search_tool_result":
			i, ok := queries[llm.Str(b["tool_use_id"])]
			if !ok {
				continue
			}
			if m, ok := b["content"].(map[string]any); ok {
				a.Searches[i].Error = llm.Str(m["error_code"])
			} else if _, ok := b["content"].([]any); ok {
				a.Searches[i].Completed = true
			}
			for _, r := range llm.List(b["content"]) { // an error result is an object, not a list
				rm := llm.Map(r)
				a.Searches[i].Results = append(a.Searches[i].Results, Link{URL: llm.Str(rm["url"]), Title: llm.Str(rm["title"])})
			}
		case "text":
			text.WriteString(llm.Str(b["text"]))
			for _, c := range llm.List(b["citations"]) {
				cm := llm.Map(c)
				u := llm.Str(cm["url"])
				if u != "" && !seenCite[u] {
					seenCite[u] = true
					a.Citations = append(a.Citations, Link{URL: u, Title: llm.Str(cm["title"])})
				}
			}
		}
	}
	// Claude often narrates ("I'll search for that.") before the first search; the answer is what
	// follows the last tool result.
	a.Text = strings.TrimSpace(finalText(blocks, text.String()))
	a.Usage = llm.Usage(resp, false)
	return a
}

func finalText(blocks []any, all string) string {
	last := -1
	for i, raw := range blocks {
		t := llm.Str(llm.Map(raw)["type"])
		if strings.HasSuffix(t, "_tool_result") {
			last = i
		}
	}
	if last < 0 {
		return all
	}
	var b strings.Builder
	for _, raw := range blocks[last+1:] {
		m := llm.Map(raw)
		if llm.Str(m["type"]) == "text" {
			b.WriteString(llm.Str(m["text"]))
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return all
	}
	return b.String()
}

func askOpenAI(ctx context.Context, model string, track Track, sys, prompt string, insist bool) (Answer, error) {
	body := map[string]any{"model": model, "input": prompt, "max_output_tokens": 4096}
	if track == Search {
		sys += " " + SearchPolicy
		if insist {
			sys += " You must use web search at least once before you answer."
		}
		body["tools"] = []any{map[string]any{"type": "web_search"}}
		body["include"] = []any{"web_search_call.action.sources"}
	}
	body["instructions"] = sys
	resp, err := llm.OpenAI(ctx, body)
	if err != nil {
		return Answer{}, err
	}
	return ParseOpenAI(resp), nil
}

// ParseOpenAI reads a Responses API response.
func ParseOpenAI(resp map[string]any) Answer {
	var a Answer
	a.Model = llm.Str(resp["model"])
	seenCite := map[string]bool{}
	var text strings.Builder
	for _, raw := range llm.List(resp["output"]) {
		o := llm.Map(raw)
		switch llm.Str(o["type"]) {
		case "web_search_call":
			act := llm.Map(o["action"])
			switch llm.Str(act["type"]) {
			case "search":
				q := Query{Query: llm.Str(act["query"]), Completed: llm.Str(o["status"]) == "completed"}
				for _, v := range llm.List(act["queries"]) {
					if q.Query != "" {
						q.Query += " | "
					}
					q.Query += llm.Str(v)
				}
				if llm.Str(o["status"]) == "failed" {
					q.Error = "failed"
				}
				for _, s := range llm.List(act["sources"]) {
					q.Results = append(q.Results, Link{URL: llm.Str(llm.Map(s)["url"])})
				}
				a.Searches = append(a.Searches, q)
			case "open_page":
				a.Fetches = append(a.Fetches, llm.Str(act["url"]))
			}
		case "message":
			for _, c := range llm.List(o["content"]) {
				cm := llm.Map(c)
				if llm.Str(cm["type"]) != "output_text" {
					continue
				}
				text.WriteString(llm.Str(cm["text"]))
				for _, an := range llm.List(cm["annotations"]) {
					am := llm.Map(an)
					u := cleanURL(llm.Str(am["url"]))
					if llm.Str(am["type"]) == "url_citation" && u != "" && !seenCite[u] {
						seenCite[u] = true
						a.Citations = append(a.Citations, Link{URL: u, Title: llm.Str(am["title"])})
					}
				}
			}
		}
	}
	a.Text = strings.TrimSpace(text.String())
	a.Usage = llm.Usage(resp, true)
	return a
}

// cleanURL drops the utm_source=openai tag ChatGPT adds, so the same page counts once.
func cleanURL(u string) string {
	for _, tag := range []string{"?utm_source=openai", "&utm_source=openai"} {
		u = strings.Replace(u, tag, "", 1)
	}
	return u
}

func (a Answer) Searched() bool {
	for _, s := range a.Searches {
		if s.Completed && s.Error == "" {
			return true
		}
	}
	return false
}
