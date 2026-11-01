package answer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cheapseatsdev/cheapseats/internal/config"
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

func TestCodexDisablesLocalTools(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + log + "\n" +
		"printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Use Cheap Seats.\"}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CHEAPSEATS_CLI_USER", "")

	for _, track := range []Track{Recall, Search} {
		if _, err := askCodex(context.Background(), "gpt-5.6-sol", track, System(""), "a question", false); err != nil {
			t.Fatalf("%s: %v", track, err)
		}
		got, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		args := string(got)
		wantPrefix := "--disable\nview_image\n--disable\nshell_tool\n--disable\nplugins\n--disable\nremote_plugin\n--disable\napps\n--ask-for-approval\nnever\nexec\n"
		if !strings.HasPrefix(args, wantPrefix) {
			t.Errorf("%s codex arguments lack enforced tool policy:\n%s", track, args)
		}
		if _, afterExec, _ := strings.Cut(args, "\nexec\n"); !strings.Contains(afterExec, "\n-c\nanalytics.enabled=false\n") {
			t.Errorf("%s codex call should turn analytics off after exec:\n%s", track, args)
		}
		// The search mode must follow exec: before it, codex 0.163 drops --search and -c alike.
		// Search is live; recall has none, not codex's default cached search.
		_, afterExec, _ := strings.Cut(args, "\nexec\n")
		want := map[Track]string{Search: "live", Recall: "disabled"}[track]
		if !strings.Contains(afterExec, "\n-c\nweb_search=\""+want+"\"\n") || strings.Count(args, "web_search=") != 1 {
			t.Errorf("%s codex call should set web_search=%q once, after exec:\n%s", track, want, args)
		}
		for _, want := range []string{"\n--sandbox\nread-only\n", "\n-c\nshell_environment_policy.inherit=none\n", "\n-m\ngpt-5.6-sol\n"} {
			if !strings.Contains(args, want) {
				t.Errorf("%s codex call lacks %q:\n%s", track, want, args)
			}
		}
	}
}

// A fake claude on the PATH: records its arguments and environment, prints a canned answer.
func TestClaudeCodeRunsIsolatedOnTheSubscription(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo \"KEY=${ANTHROPIC_API_KEY:-unset}\"; echo \"PWD=$PWD\"; env | grep -E '^(CHEAPSEATS_TOKENS|OPENAI_API_KEY|OTHER_SECRET|HOME|PATH|CHEAPSEATS_CLI_ENV|KEPT)=' | sed 's/=.*/=present/'; } > " + log + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"result\":\"Use Cheap Seats.\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "api-key-for-grading")
	// What this process holds stays here: the CLI has a shell, and the model drives it.
	t.Setenv("CHEAPSEATS_TOKENS", "tok")
	t.Setenv("OPENAI_API_KEY", "sk-other")
	t.Setenv("OTHER_SECRET", "x")
	t.Setenv("KEPT", "y")
	t.Setenv("CHEAPSEATS_CLI_ENV", " KEPT ,ANTHROPIC_API_KEY") // named: passed; the provider's key: still not
	t.Setenv("CHEAPSEATS_CLI_USER", "")
	if !config.Available(config.ClaudeCode) {
		t.Fatal("claude on the PATH should make claude-code available")
	}
	a, err := Ask(context.Background(), config.Answerer{Provider: config.ClaudeCode, Model: "opus"}, Recall, System(""), "a question")
	if err != nil || a.Text != "Use Cheap Seats." {
		t.Fatalf("%+v %v", a, err)
	}
	got, _ := os.ReadFile(log)
	args := string(got)
	for _, want := range []string{"\n--safe-mode\n", "\n--strict-mcp-config\n", "\n--no-session-persistence\n", "\n--tools\n\n", "KEY=unset", "cheapseats-claude-code-", "\nHOME=present\n", "\nPATH=present\n", "\nKEPT=present\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("claude call lacks %q:\n%s", want, args)
		}
	}
	for _, leak := range []string{"CHEAPSEATS_TOKENS=", "OPENAI_API_KEY=", "OTHER_SECRET=", "CHEAPSEATS_CLI_ENV=", "--allowedTools"} {
		if strings.Contains(args, leak) {
			t.Errorf("claude call has %q (recall allows no tools; the environment is the CLI's own):\n%s", leak, args)
		}
	}
}

// With CHEAPSEATS_CLI_USER, the CLI runs as that user through sudo: non-interactive, with that
// user's home, the command by its full path, and the CLI's environment as VAR=value arguments.
func TestCLIRunsAsAnotherUser(t *testing.T) {
	dir := t.TempDir()
	log, sudoLog := filepath.Join(dir, "log"), filepath.Join(dir, "sudo")
	// A fake sudo: records its arguments, then does what the real one would with them.
	sudo := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + sudoLog + "\n" +
		"while [ $# -gt 0 ]; do case $1 in -n|-H) shift ;; -u) shift 2 ;; *=*) export \"$1\"; shift ;; *) break ;; esac; done\n" +
		"exec \"$@\"\n"
	claude := "#!/bin/sh\n{ echo \"PWD=$PWD\"; echo \"LANG=${LANG:-unset}\"; stat -f %Lp . 2>/dev/null || stat -c %a .; } > " + log + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"result\":\"Use Cheap Seats.\"}'\n"
	for name, body := range map[string]string{"sudo": sudo, "claude": claude} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CHEAPSEATS_CLI_USER", "cheapseats-cli")
	t.Setenv("LANG", "C.UTF-8")
	a, err := Ask(context.Background(), config.Answerer{Provider: config.ClaudeCode, Model: "opus"}, Recall, System(""), "a question")
	if err != nil || a.Text != "Use Cheap Seats." {
		t.Fatalf("%+v %v", a, err)
	}
	got, _ := os.ReadFile(sudoLog)
	sargs := string(got)
	if !strings.HasPrefix(sargs, "-n\n-H\n-u\ncheapseats-cli\n") || !strings.Contains(sargs, "\nLANG=C.UTF-8\n") || !strings.Contains(sargs, "\n"+filepath.Join(dir, "claude")+"\n-p\n") || strings.Contains(sargs, "\nHOME=") {
		t.Errorf("sudo arguments:\n%s", sargs)
	}
	got, _ = os.ReadFile(log)
	if out := string(got); !strings.Contains(out, "LANG=C.UTF-8") || !strings.HasSuffix(strings.TrimSpace(out), "755") {
		t.Errorf("the CLI as the other user:\n%s", out)
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
	if err := ClaudeStructured(context.Background(), "claude-haiku-5-5", "You grade.", prompt, map[string]any{"type": "object", "additionalProperties": false}, &out); err != nil || out.Verdict != "mentioned" {
		t.Fatalf("%+v %v", out, err)
	}
	got, _ := os.ReadFile(log)
	args := string(got)
	for _, want := range []string{"-p\n", "\n--json-schema\n{\"additionalProperties\":false,\"type\":\"object\"}\n", "\n--tools\n\n", "\n--max-turns\n3\n", "\n--safe-mode\n", "\n--model\nclaude-haiku-5-5\n", "KEY=unset"} {
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

// cliCapFake puts a fake claude that runs script on the PATH.
func cliCapFake(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CHEAPSEATS_CLI_USER", "")
}

// A CLI that never stops writing is stopped at the cap, and the call fails rather than holding
// the whole stream in memory.
func TestCLIOutputPastTheCapStopsTheCLI(t *testing.T) {
	old := cliMaxOutput
	cliMaxOutput = 1 << 20
	t.Cleanup(func() { cliMaxOutput = old })
	cliCapFake(t, "exec yes cheapseats\n")
	out, err := runCLI(context.Background(), config.ClaudeCode, "ANTHROPIC_API_KEY", "claude")
	if !errors.Is(err, errCLIOutput) {
		t.Fatalf("err %v", err)
	}
	if len(out) != cliMaxOutput {
		t.Errorf("kept %d bytes of stdout; want the first %d", len(out), cliMaxOutput)
	}
}

// Of a long stderr the end is kept, since that is where a CLI says why it failed.
func TestCLIErrorKeepsTheEndOfStderr(t *testing.T) {
	cliCapFake(t, "i=0; while [ $i -lt 2000 ]; do echo noise >&2; i=$((i+1)); done; echo 'the real reason' >&2; exit 3\n")
	_, err := runCLI(context.Background(), config.ClaudeCode, "ANTHROPIC_API_KEY", "claude")
	if err == nil || !strings.HasSuffix(err.Error(), "the real reason") {
		t.Fatalf("err %v", err)
	}
}

// Claude's telemetry is off on every call, even when this process turns it back on and passes it.
func TestClaudeCLISendsNoTelemetry(t *testing.T) {
	cliCapFake(t, "echo \"$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC\"\n")
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "0")
	t.Setenv("CHEAPSEATS_CLI_ENV", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
	out, err := runCLI(context.Background(), config.ClaudeCode, "ANTHROPIC_API_KEY", "claude")
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=%q, err %v", out, err)
	}
}

// The CLI would take maxItems; the API answers it with an HTTP 400. Both paths refuse it.
func TestClaudeStructuredRefusesSchemaTheAPIRefuses(t *testing.T) {
	bad := map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"type": "string"}}
	var out []string
	if err := ClaudeStructured(context.Background(), "m", "s", "p", bad, &out); err == nil || !strings.Contains(err.Error(), "maxItems") {
		t.Fatalf("schema the API refuses: %v", err)
	}
}
