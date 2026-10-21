package answer

// Answerers that run through a locally installed, logged-in CLI, so answers come out of a
// Claude or ChatGPT subscription rather than API credit:
//
//   - claude-code: `claude -p` in safe mode (none of your CLAUDE.md, hooks, skills or MCP
//     servers), with WebSearch and WebFetch on the search track and no tools on recall.
//   - codex: `codex exec` with your config ignored, a read-only sandbox, and live web search
//     on the search track.
//
// Each call runs in an empty temp directory, keeps no session, and has the provider's API key
// removed from its environment, so it can't fall back to billing the API.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/metcalfc/popcorn/internal/config"
	"github.com/metcalfc/popcorn/internal/llm"
)

// cliSlots caps concurrent calls per CLI: subscriptions are rate limited, and the limit is
// shared with your own use.
var cliSlots = map[string]chan struct{}{
	config.ClaudeCode: make(chan struct{}, 3),
	config.Codex:      make(chan struct{}, 3),
}

const cliTimeout = 10 * time.Minute

// runCLI runs one command in a throwaway directory and returns its stdout.
func runCLI(ctx context.Context, provider, unsetKey string, name string, args ...string) ([]byte, error) {
	select {
	case cliSlots[provider] <- struct{}{}:
		defer func() { <-cliSlots[provider] }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "popcorn-"+provider+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = nil // /dev/null: codex otherwise waits for more input
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, unsetKey+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return stdout.Bytes(), nil
}

func askClaudeCode(ctx context.Context, model string, track Track, sys, prompt string, insist bool) (Answer, error) {
	tools := ""
	if track == Search {
		sys += " " + SearchPolicy
		if insist {
			sys += " You must use the WebSearch tool at least once before you answer."
		}
		tools = "WebSearch,WebFetch"
	}
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--no-session-persistence",
		"--safe-mode", "--strict-mcp-config", "--disable-slash-commands",
		"--model", model, "--tools", tools, "--max-turns", "10", "--system-prompt", sys}
	if tools != "" {
		args = append(args, "--allowedTools", tools)
	}
	out, err := runCLI(ctx, config.ClaudeCode, "ANTHROPIC_API_KEY", "claude", args...)
	ans := ParseClaudeCode(out)
	if err != nil {
		return ans, err
	}
	if ans.Text == "" {
		return ans, fmt.Errorf("claude: no result in output")
	}
	return ans, nil
}

// ParseClaudeCode reads `claude -p --output-format stream-json` output.
func ParseClaudeCode(out []byte) Answer {
	var a Answer
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch llm.Str(e["type"]) {
		case "system":
			if llm.Str(e["subtype"]) == "init" && a.Model == "" {
				a.Model = llm.Str(e["model"])
			}
		case "assistant":
			for _, c := range llm.List(llm.Map(e["message"])["content"]) {
				cm := llm.Map(c)
				if llm.Str(cm["type"]) == "tool_use" && llm.Str(cm["name"]) == "WebSearch" {
					a.Searches = append(a.Searches, Query{Query: llm.Str(llm.Map(cm["input"])["query"])})
				}
			}
		case "user":
			r, ok := e["tool_use_result"].(map[string]any)
			if !ok {
				continue
			}
			if q, ok := r["query"].(string); ok {
				i := len(a.Searches) - 1
				for j := range a.Searches {
					if a.Searches[j].Query == q && !a.Searches[j].Completed {
						i = j
						break
					}
				}
				if i < 0 {
					a.Searches = append(a.Searches, Query{Query: q})
					i = 0
				}
				a.Searches[i].Completed = true
				for _, res := range llm.List(r["results"]) {
					for _, l := range llm.List(llm.Map(res)["content"]) {
						lm := llm.Map(l)
						if u := llm.Str(lm["url"]); u != "" {
							a.Searches[i].Results = append(a.Searches[i].Results, Link{URL: u, Title: llm.Str(lm["title"])})
						}
					}
				}
			} else if u := llm.Str(r["url"]); u != "" {
				a.Fetches = append(a.Fetches, u)
			}
		case "result":
			a.Text = strings.TrimSpace(llm.Str(e["result"]))
			u := llm.Map(e["usage"])
			a.Usage = config.Usage{Input: llm.Int(u["input_tokens"]), Output: llm.Int(u["output_tokens"]),
				CacheRead: llm.Int(u["cache_read_input_tokens"]), CacheWrite: llm.Int(u["cache_creation_input_tokens"]),
				Searches: int64(len(a.Searches))}
		}
	}
	a.Citations = linksIn(a.Text)
	return a
}

// codexBrief stands in for a system prompt: Codex keeps its own, so ours leads the message.
const codexBrief = "Answer the question below as a chat assistant would. Do not run shell commands or read or write files."

func askCodex(ctx context.Context, model string, track Track, sys, prompt string, insist bool) (Answer, error) {
	brief := codexBrief + " " + sys
	var args []string
	if track == Search {
		brief += " " + SearchPolicy
		if insist {
			brief += " You must use web search at least once before you answer."
		}
		args = append(args, "--search")
	} else {
		brief += " Answer from what you already know; do not look anything up."
	}
	args = append(args, "exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--skip-git-repo-check", "--sandbox", "read-only", "-m", model, brief+"\n\nQuestion: "+prompt)
	out, err := runCLI(ctx, config.Codex, "OPENAI_API_KEY", "codex", args...)
	ans := ParseCodex(out)
	if ans.Model == "" {
		ans.Model = model
	}
	if err != nil {
		return ans, err
	}
	if ans.Text == "" {
		return ans, fmt.Errorf("codex: no answer in output")
	}
	return ans, nil
}

// ParseCodex reads `codex exec --json` events.
func ParseCodex(out []byte) Answer {
	var a Answer
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch llm.Str(e["type"]) {
		case "item.completed":
			item := llm.Map(e["item"])
			switch llm.Str(item["type"]) {
			case "agent_message":
				// The last message is the answer; earlier ones narrate ("I'll check...").
				a.Text = strings.TrimSpace(llm.Str(item["text"]))
			case "web_search":
				var links []Link
				for _, r := range llm.List(item["results"]) {
					rm := llm.Map(r)
					if u := llm.Str(rm["url"]); u != "" {
						links = append(links, Link{URL: u, Title: llm.Str(rm["title"])})
					}
				}
				act := llm.Map(item["action"])
				if llm.Str(act["type"]) == "open_page" {
					a.Fetches = append(a.Fetches, llm.Str(act["url"]))
					continue
				}
				q := llm.Str(item["query"])
				if qs := llm.List(act["queries"]); len(qs) > 0 {
					parts := make([]string, len(qs))
					for i, x := range qs {
						parts[i] = llm.Str(x)
					}
					q = strings.Join(parts, " | ")
				}
				a.Searches = append(a.Searches, Query{Query: q, Completed: true, Results: links})
			}
		case "turn.completed":
			u := llm.Map(e["usage"])
			cached := llm.Int(u["cached_input_tokens"])
			a.Usage = config.Usage{Input: llm.Int(u["input_tokens"]) - cached, CacheRead: cached,
				Output: llm.Int(u["output_tokens"]), Searches: int64(len(a.Searches))}
		}
	}
	a.Citations = linksIn(a.Text)
	return a
}

var urlRe = regexp.MustCompile(`https?://[^\s)\]>"'` + "`" + `]+`)

// linksIn is every URL the answer text links to, in order, once each: CLI answers cite inline.
func linksIn(text string) []Link {
	var out []Link
	seen := map[string]bool{}
	for _, u := range urlRe.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:")
		if !seen[u] {
			seen[u] = true
			out = append(out, Link{URL: u})
		}
	}
	return out
}
