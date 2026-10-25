package answer

// Answerers that run through a locally installed, logged-in CLI, so answers come out of a
// Claude or ChatGPT subscription rather than API credit:
//
//   - claude-code: `claude -p` in safe mode (none of your CLAUDE.md, hooks, skills or MCP
//     servers), with WebSearch and WebFetch on the search track and no tools on recall.
//   - codex: `codex exec` with your config ignored, local read and shell tools disabled, a
//     read-only sandbox, and live web search on the search track.
//
// Each call runs in an empty temp directory, keeps no session, and gets only the environment it
// needs (cliEnv): not the provider's API key, so it can't fall back to billing the API, and not
// the tokens and keys this process holds, since each CLI is an agent with tools the model
// drives, and what's switched off today may not be tomorrow. CHEAPSEATS_CLI_USER runs the CLIs
// as another user through sudo, which keeps this user's files out of reach as well
// (deploy/exe-dev.sh sets it up).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cheapseatsdev/cheapseats/internal/config"
	"github.com/cheapseatsdev/cheapseats/internal/env"
	"github.com/cheapseatsdev/cheapseats/internal/llm"
)

// cliEnv is what a CLI gets of this process's environment: where it is and who it runs as, the
// locale, proxies and certificates, and its own settings directory. CHEAPSEATS_CLI_ENV names more
// to pass, comma-separated.
var cliEnv = []string{"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR", "TERM", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"}

// sudoSets is what sudo sets for the target user itself; passing them would override that.
var sudoSets = []string{"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR"}

// cliVars is the environment for a CLI call: cliEnv and CHEAPSEATS_CLI_ENV, as set here, less the
// provider's API key and anything that would load code into the CLI as it starts (LD_PRELOAD,
// NODE_OPTIONS and the like), whoever named it.
func cliVars(unsetKey string) []string {
	names := append(slices.Clone(cliEnv), strings.Split(env.Get("CLI_ENV"), ",")...)
	var out []string
	for _, n := range names {
		if n = strings.TrimSpace(n); n == "" || n == unsetKey || env.LoadsCode(n) {
			continue
		}
		if v, ok := os.LookupEnv(n); ok {
			out = append(out, n+"="+v)
		}
	}
	return out
}

// cliSlots caps concurrent calls per CLI: subscriptions are rate limited, and the limit is
// shared with your own use.
var cliSlots = map[string]chan struct{}{
	config.ClaudeCode: make(chan struct{}, 3),
	config.Codex:      make(chan struct{}, 3),
}

const cliTimeout = 10 * time.Minute

// cliMaxOutput is the most of a CLI's stdout kept. A search answer's stream-json, with the pages
// it fetched, runs to a few MB; a CLI that writes past this is stopped, and the call fails.
var cliMaxOutput = 32 << 20

// cliStderrTail is the most of a CLI's stderr kept: the end, where the reason it failed is.
const cliStderrTail = 4 << 10

// errCLIOutput is a CLI that wrote more than cliMaxOutput to stdout.
var errCLIOutput = errors.New("output over the limit")

// cliCapped keeps the first max bytes written and calls stop once, the first time a write would
// pass max; what comes after is discarded, so the CLI never blocks on a full pipe while it stops.
type cliCapped struct {
	buf  bytes.Buffer
	max  int
	over bool
	stop func()
}

func (w *cliCapped) Write(p []byte) (int, error) {
	if w.over {
		return len(p), nil
	}
	if room := w.max - w.buf.Len(); len(p) > room {
		w.buf.Write(p[:room])
		w.over = true
		w.stop()
		return len(p), nil
	}
	return w.buf.Write(p)
}

// cliTail keeps the last max bytes written.
type cliTail struct {
	buf []byte
	max int
}

func (w *cliTail) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = append(w.buf[:0], w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

// runCLI runs one command in a throwaway directory and returns its stdout.
func runCLI(ctx context.Context, provider, unsetKey string, name string, args ...string) ([]byte, error) {
	return runCLIInput(ctx, provider, unsetKey, nil, name, args...)
}

// runCLIInput is runCLI with stdin: a prompt too long for one argument (Linux caps one at 128 KiB).
func runCLIInput(ctx context.Context, provider, unsetKey string, stdin []byte, name string, args ...string) ([]byte, error) {
	select {
	case cliSlots[provider] <- struct{}{}:
		defer func() { <-cliSlots[provider] }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "cheapseats-"+provider+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	vars := cliVars(unsetKey)
	var cmd *exec.Cmd
	if user := strings.TrimSpace(env.Get("CLI_USER")); user == "" {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		// As that user, through sudo, which starts the environment over: it sets HOME, PATH and
		// the names itself, and takes the rest as VAR=value, which the rule permits (SETENV). The
		// command is its full path, which the rule names; the directory must let that user in.
		path, err := exec.LookPath(name)
		if err != nil {
			return nil, err
		}
		os.Chmod(dir, 0o755)
		sargs := []string{"-n", "-H", "-u", user}
		for _, kv := range vars {
			if k, _, _ := strings.Cut(kv, "="); !slices.Contains(sudoSets, k) {
				sargs = append(sargs, kv)
			}
		}
		cmd = exec.CommandContext(ctx, "sudo", append(append(sargs, path), args...)...)
	}
	cmd.Dir = dir
	cmd.Env = vars
	cmd.Stdin = nil // /dev/null: codex otherwise waits for more input
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// On timeout, a TERM the CLI (or sudo, which passes it on) can act on; a KILL soon after.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	// Past cliMaxOutput the CLI is stopped as on a timeout: TERM, then KILL.
	stdout := &cliCapped{max: cliMaxOutput, stop: cancel}
	stderr := &cliTail{max: cliStderrTail}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if stdout.over {
		return stdout.buf.Bytes(), fmt.Errorf("%s: %w of %d bytes", name, errCLIOutput, cliMaxOutput)
	}
	if err != nil {
		msg := strings.TrimSpace(string(stderr.buf))
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return stdout.buf.Bytes(), fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return stdout.buf.Bytes(), nil
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

// ClaudeStructured asks the claude CLI, on its subscription and with no tools, for output that
// matches schema, and decodes it into out: the grader and analyst through claude-code. The prompt
// goes on stdin, since the analyst's holds a whole run. It has no cost to record.
func ClaudeStructured(ctx context.Context, model, system, prompt string, schema map[string]any, out any) error {
	js, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	// --json-schema answers through a tool call, so it needs a turn beyond the first.
	args := []string{"-p", "--output-format", "json", "--no-session-persistence",
		"--safe-mode", "--strict-mcp-config", "--disable-slash-commands",
		"--model", model, "--tools", "", "--max-turns", "3", "--system-prompt", system, "--json-schema", string(js)}
	stdout, err := runCLIInput(ctx, config.ClaudeCode, "ANTHROPIC_API_KEY", []byte(prompt), "claude", args...)
	perr := parseClaudeStructured(stdout, out)
	if err == nil {
		return perr
	}
	// A failed run usually still writes its result: say why, not only the exit status.
	if perr != nil && len(bytes.TrimSpace(stdout)) > 0 {
		return fmt.Errorf("%w (%v)", err, perr)
	}
	return err
}

// parseClaudeStructured reads `claude -p --output-format json --json-schema` output.
func parseClaudeStructured(stdout []byte, out any) error {
	var res struct {
		Subtype          string          `json:"subtype"`
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(stdout, &res); err != nil {
		return fmt.Errorf("claude: reading output: %w", err)
	}
	if res.IsError || res.Subtype != "success" {
		return fmt.Errorf("claude: %s: %s", res.Subtype, res.Result)
	}
	if len(res.StructuredOutput) == 0 || string(res.StructuredOutput) == "null" {
		return fmt.Errorf("claude: no structured output")
	}
	return json.Unmarshal(res.StructuredOutput, out)
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
	// These are global flags and must precede `exec`. The read-only sandbox prevents apply_patch
	// writes; disabling view_image and shell_tool also keeps prompt-controlled tool calls from
	// reading local files. Approval is never interactive in this unattended answerer. The
	// sub-agent and JavaScript orchestration tools stay: no switch in codex 0.163 removes them
	// (not --disable multi_agent, not agents.max_depth), and neither reads files on its own.
	args := []string{"--disable", "view_image", "--disable", "shell_tool", "--ask-for-approval", "never"}
	if track == Search {
		brief += " " + SearchPolicy
		if insist {
			brief += " You must use web search at least once before you answer."
		}
		args = append(args, "--search")
	} else {
		brief += " Answer from what you already know; do not look anything up."
	}
	// A defense-in-depth empty environment also applies if a future CLI adds another command tool.
	args = append(args, "exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--skip-git-repo-check", "--sandbox", "read-only", "-c", "shell_environment_policy.inherit=none",
		"-m", model, brief+"\n\nQuestion: "+prompt)
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
