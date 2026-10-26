// Package bots names the crawlers that matter to AI visibility, and what each one is for: fetching
// a page because someone asked an assistant (assistant), building an assistant's search index
// (search), collecting training data (training), or a web search engine some assistants search
// through (engine). The site audit reads robots.txt rules for them; the crawler log import counts
// their fetches, and people's visits beside them.
package bots

import "strings"

type Class string

const (
	Assistant Class = "assistant" // user-triggered: fetched because someone asked
	Search    Class = "search"    // an assistant's search index
	Training  Class = "training"  // training data
	Engine    Class = "engine"    // a web search engine (Bing feeds ChatGPT and Copilot, Google feeds Gemini)
	Referral  Class = "referral"  // not a bot: a person who came from an assistant's answer
	Visit     Class = "visit"     // not a bot: a person's browser, come from anywhere else
)

// Classes in the order pages show them.
var Classes = []Class{Assistant, Search, Training, Engine}

type Bot struct {
	// The robots.txt product token, which the user agent also contains unless RobotsOnly.
	Token    string
	Operator string
	Class    Class
	// A control token for robots.txt only (Google-Extended): no crawler sends it.
	RobotsOnly bool
	// A user agent string the bot sends, for the audit's firewall check.
	UA string
}

// All is every bot known, most specific token first where one contains another.
var All = []Bot{
	{Token: "ChatGPT-User", Operator: "OpenAI", Class: Assistant, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot"},
	{Token: "Claude-User", Operator: "Anthropic", Class: Assistant, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-User/1.0; +Claude-User@anthropic.com)"},
	{Token: "Perplexity-User", Operator: "Perplexity", Class: Assistant, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Perplexity-User/1.0; +https://perplexity.ai/perplexity-user)"},
	{Token: "MistralAI-User", Operator: "Mistral", Class: Assistant},
	{Token: "OAI-SearchBot", Operator: "OpenAI", Class: Search, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; OAI-SearchBot/1.0; +https://openai.com/searchbot"},
	{Token: "Claude-SearchBot", Operator: "Anthropic", Class: Search, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-SearchBot/1.0; +Claude-SearchBot@anthropic.com)"},
	{Token: "PerplexityBot", Operator: "Perplexity", Class: Search, UA: "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; PerplexityBot/1.0; +https://perplexity.ai/perplexitybot)"},
	{Token: "DuckAssistBot", Operator: "DuckDuckGo", Class: Search},
	{Token: "GPTBot", Operator: "OpenAI", Class: Training},
	{Token: "ClaudeBot", Operator: "Anthropic", Class: Training},
	{Token: "anthropic-ai", Operator: "Anthropic", Class: Training},
	{Token: "Google-Extended", Operator: "Google", Class: Training, RobotsOnly: true},
	{Token: "Applebot-Extended", Operator: "Apple", Class: Training, RobotsOnly: true},
	{Token: "CCBot", Operator: "Common Crawl", Class: Training},
	{Token: "Meta-ExternalAgent", Operator: "Meta", Class: Training},
	{Token: "Bytespider", Operator: "ByteDance", Class: Training},
	{Token: "Amazonbot", Operator: "Amazon", Class: Training},
	{Token: "cohere-ai", Operator: "Cohere", Class: Training},
	{Token: "Googlebot", Operator: "Google", Class: Engine},
	{Token: "Bingbot", Operator: "Microsoft", Class: Engine},
}

// Audited is the bots whose robots.txt rules the audit reports, in report order: the assistants'
// search and user fetchers, the training crawlers, then the search engines.
func Audited() []Bot {
	var out []Bot
	for _, b := range All {
		switch b.Token {
		case "MistralAI-User", "DuckAssistBot", "Amazonbot", "cohere-ai":
			continue // counted in logs, not worth a robots.txt line of their own
		}
		out = append(out, b)
	}
	return out
}

// Match is the bot a user agent string belongs to, by its token, ignoring case. A token only in
// robots.txt never matches. It's the user agent's word only: anyone can send it.
func Match(ua string) (Bot, bool) {
	l := strings.ToLower(ua)
	for _, b := range All {
		if !b.RobotsOnly && strings.Contains(l, strings.ToLower(b.Token)) {
			return b, true
		}
	}
	return Bot{}, false
}

// notBrowser are words in a user agent that only tools and bots send: a crawler's "+https://" URL,
// "bot", "spider", a headless browser, an HTTP library.
var notBrowser = []string{"bot", "crawl", "spider", "slurp", "http", "compatible;", "headless", "lighthouse",
	"preview", "python", "curl", "wget", "java/", "okhttp", "go-http", "fetch", "scan", "monitor"}

// Browser says whether a user agent reads as a person's web browser: it starts as browsers' do and
// has none of the words tools and bots send. It's a guess from the user agent alone, so counts it
// keeps are approximate: a bot dressed as a browser counts.
func Browser(ua string) bool {
	l := strings.ToLower(ua)
	if !strings.HasPrefix(l, "mozilla/") {
		return false
	}
	for _, w := range notBrowser {
		if strings.Contains(l, w) {
			return false
		}
	}
	return true
}

// Referrers are the hosts of assistants whose answers link people to pages, by assistant.
var Referrers = map[string]string{
	"chatgpt.com":           "ChatGPT",
	"chat.openai.com":       "ChatGPT",
	"claude.ai":             "Claude",
	"perplexity.ai":         "Perplexity",
	"copilot.microsoft.com": "Copilot",
	"gemini.google.com":     "Gemini",
}

// ReferrerOf is the assistant a host (from a Referer, or a utm_source value) belongs to, if any.
func ReferrerOf(host string) (string, bool) {
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
	if n, ok := Referrers[h]; ok {
		return n, true
	}
	return "", false
}
