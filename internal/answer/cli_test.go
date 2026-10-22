package answer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metcalfc/popcorn/internal/config"
)

// Trimmed from real `claude -p --output-format stream-json --verbose` output (Claude Code 2.1).
const claudeCodeStream = `{"type":"system","subtype":"init","model":"claude-opus-5-5","tools":["WebFetch","WebSearch"],"mcp_servers":[],"apiKeySource":"none"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"WebSearch","input":{"query":"sandbox coding agent macOS"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"WebSearch","input":{"query":"Docker sandboxes microVM"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2"}]},"tool_use_result":{"query":"Docker sandboxes microVM","results":[{"tool_use_id":"s2","content":[{"title":"Docker Sandboxes","url":"https://docs.docker.com/ai/sandboxes/"}]},"summary text"]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]},"tool_use_result":{"query":"sandbox coding agent macOS","results":[{"tool_use_id":"s1","content":[{"title":"Sandboxing","url":"https://docs.claude.com/en/docs/claude-code/sandboxing"},{"title":"Post","url":"https://dev.to/x"}]}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"WebFetch","input":{"url":"https://go.dev/doc"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3"}]},"tool_use_result":{"bytes":10,"code":200,"result":"...","url":"https://go.dev/doc"}}
{"type":"result","subtype":"success","result":"Use Docker Sandboxes ([docs](https://docs.docker.com/ai/sandboxes/)). See https://dev.to/x.","total_cost_usd":0.1,"usage":{"input_tokens":4,"cache_creation_input_tokens":2260,"cache_read_input_tokens":1996,"output_tokens":289}}
`

func TestParseClaudeCode(t *testing.T) {
	a := ParseClaudeCode([]byte(claudeCodeStream))
	if a.Model != "claude-opus-5-5" || !strings.HasPrefix(a.Text, "Use Docker Sandboxes") {
		t.Fatalf("%+v", a)
	}
	if len(a.Searches) != 2 || a.Searches[0].Query != "sandbox coding agent macOS" || len(a.Searches[0].Results) != 2 || len(a.Searches[1].Results) != 1 || !a.Searched() {
		t.Fatalf("searches matched to the wrong query: %+v", a.Searches)
	}
	if len(a.Fetches) != 1 || a.Fetches[0] != "https://go.dev/doc" {
		t.Fatalf("fetches: %v", a.Fetches)
	}
	if len(a.Citations) != 2 || a.Citations[0].URL != "https://docs.docker.com/ai/sandboxes/" || a.Citations[1].URL != "https://dev.to/x" {
		t.Fatalf("citations: %+v", a.Citations)
	}
	if a.Usage.CacheWrite != 2260 || a.Usage.Searches != 2 {
		t.Fatalf("usage: %+v", a.Usage)
	}
}

// Trimmed from real `codex exec --json` output (codex-cli 0.159).
const codexStream = `{"type":"thread.started","thread_id":"x"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I'll check current guidance."}}
{"type":"item.started","item":{"type":"web_search","query":"","action":{"type":"other"}}}
{"type":"item.completed","item":{"type":"web_search","query":"Docker bind mount ...","action":{"type":"search","queries":["Docker bind mount security","sandbox coding agents"]},"results":[{"type":"text_result","title":"Self-hosted sandboxes","url":"https://developers.openai.com/x"}]}}
{"type":"item.completed","item":{"type":"web_search","query":"https://docs.docker.com/run/","action":{"type":"open_page","url":"https://docs.docker.com/run/"},"results":[{"url":"https://docs.docker.com/run/"}]}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Start with **Docker Sandboxes** ([docs](https://docs.docker.com/ai/sandboxes/))."}}
{"type":"turn.completed","usage":{"input_tokens":66554,"cached_input_tokens":50176,"output_tokens":549}}
`

func TestParseCodex(t *testing.T) {
	a := ParseCodex([]byte(codexStream))
	if !strings.HasPrefix(a.Text, "Start with") {
		t.Fatalf("the last message is the answer, not the narration: %q", a.Text)
	}
	if len(a.Searches) != 1 || a.Searches[0].Query != "Docker bind mount security | sandbox coding agents" || len(a.Searches[0].Results) != 1 || !a.Searched() {
		t.Fatalf("searches: %+v", a.Searches)
	}
	if len(a.Fetches) != 1 || len(a.Citations) != 1 || a.Citations[0].URL != "https://docs.docker.com/ai/sandboxes/" {
		t.Fatalf("fetches %v citations %+v", a.Fetches, a.Citations)
	}
	if a.Usage.Input != 66554-50176 || a.Usage.CacheRead != 50176 {
		t.Fatalf("usage: %+v", a.Usage)
	}
}

// A fake claude on the PATH: records its arguments and environment, prints a canned answer.
func TestClaudeCodeRunsIsolatedOnTheSubscription(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo \"KEY=${ANTHROPIC_API_KEY:-unset}\"; echo \"PWD=$PWD\"; } > " + log + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"result\":\"Use Popcorn.\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "api-key-for-grading")
	if !config.Available(config.ClaudeCode) {
		t.Fatal("claude on the PATH should make claude-code available")
	}
	a, err := Ask(context.Background(), config.Answerer{Provider: config.ClaudeCode, Model: "opus"}, Recall, System(""), "a question")
	if err != nil || a.Text != "Use Popcorn." {
		t.Fatalf("%+v %v", a, err)
	}
	got, _ := os.ReadFile(log)
	args := string(got)
	for _, want := range []string{"\n--safe-mode\n", "\n--strict-mcp-config\n", "\n--no-session-persistence\n", "\n--tools\n\n", "KEY=unset", "popcorn-claude-code-"} {
		if !strings.Contains(args, want) {
			t.Errorf("claude call lacks %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--allowedTools") {
		t.Error("recall should allow no tools")
	}
}

// The grader and analyst on the subscription: the prompt on stdin, a JSON schema, no tools, no
// API key, and the structured output decoded.
func TestClaudeStructuredRunsOnTheSubscription(t *testing.T) {
	dir := t.TempDir()
	log, in := filepath.Join(dir, "log"), filepath.Join(dir, "stdin")
	script := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo \"KEY=${ANTHROPIC_API_KEY:-unset}\"; } > " + log + "\ncat > " + in + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"{}\",\"structured_output\":{\"verdict\":\"mentioned\"}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "api-key")
	var out struct{ Verdict string }
	prompt := strings.Repeat("a long prompt ", 20000) // past Linux's 128 KiB cap on one argument
	if err := ClaudeStructured(context.Background(), "claude-haiku-5-5", "You grade.", prompt, map[string]any{"type": "object"}, &out); err != nil || out.Verdict != "mentioned" {
		t.Fatalf("%+v %v", out, err)
	}
	got, _ := os.ReadFile(log)
	args := string(got)
	for _, want := range []string{"-p\n", "\n--json-schema\n{\"type\":\"object\"}\n", "\n--tools\n\n", "\n--max-turns\n3\n", "\n--safe-mode\n", "\n--model\nclaude-haiku-5-5\n", "KEY=unset"} {
		if !strings.Contains(args, want) {
			t.Errorf("claude call lacks %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "a long prompt") {
		t.Error("the prompt should go on stdin, not in an argument")
	}
	if b, _ := os.ReadFile(in); string(b) != prompt {
		t.Errorf("stdin got %d bytes, want %d", len(b), len(prompt))
	}

	for name, body := range map[string]string{
		"error":     `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"too many turns"}`,
		"no output": `{"type":"result","subtype":"success","is_error":false,"result":"text only"}`,
	} {
		if err := parseClaudeStructured([]byte(body), &out); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
