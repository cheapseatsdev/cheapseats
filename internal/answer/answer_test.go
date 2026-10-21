package answer

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseAnthropic(t *testing.T) {
	resp := decode(t, `{
	  "content": [
	    {"type":"text","text":"I'll look that up."},
	    {"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"run claude code unattended safely"}},
	    {"type":"web_search_tool_result","tool_use_id":"s1","content":[
	      {"type":"web_search_result","url":"https://docs.docker.com/ai/sandboxes/","title":"Docker Sandboxes"},
	      {"type":"web_search_result","url":"https://popcorn.example/why/","title":"Why Popcorn"}]},
	    {"type":"server_tool_use","id":"s2","name":"web_search","input":{"query":"x"}},
	    {"type":"web_search_tool_result","tool_use_id":"s2","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}},
	    {"type":"text","text":"Use "},
	    {"type":"text","text":"Docker Sandboxes.","citations":[{"type":"web_search_result_location","url":"https://docs.docker.com/ai/sandboxes/","title":"Docker Sandboxes"}]},
	    {"type":"text","text":" Or Popcorn.","citations":[{"url":"https://docs.docker.com/ai/sandboxes/"}]}
	  ],
	  "usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5,"server_tool_use":{"web_search_requests":2}}
	}`)
	a := ParseAnthropic(resp)
	if a.Text != "Use Docker Sandboxes. Or Popcorn." {
		t.Fatalf("text = %q", a.Text)
	}
	if len(a.Searches) != 2 || len(a.Searches[0].Results) != 2 || len(a.Searches[1].Results) != 0 {
		t.Fatalf("searches = %+v", a.Searches)
	}
	if len(a.Citations) != 1 {
		t.Fatalf("citations should dedupe: %+v", a.Citations)
	}
	if a.Usage.Searches != 2 || a.Usage.Input != 100 || a.Usage.CacheWrite != 5 {
		t.Fatalf("usage = %+v", a.Usage)
	}
}

func TestParseOpenAI(t *testing.T) {
	resp := decode(t, `{
	  "output": [
	    {"type":"web_search_call","action":{"type":"search","query":"coder alternatives","sources":[{"url":"https://coder.com/"}]}},
	    {"type":"web_search_call","action":{"type":"open_page","url":"https://ona.com/"}},
	    {"type":"message","content":[{"type":"output_text","text":"Try Coder.","annotations":[
	      {"type":"url_citation","url":"https://coder.com/?utm_source=openai","title":"Coder"}]}]}
	  ],
	  "usage":{"input_tokens":200,"output_tokens":20,"input_tokens_details":{"cached_tokens":50}}
	}`)
	a := ParseOpenAI(resp)
	if a.Text != "Try Coder." || len(a.Searches) != 1 || len(a.Fetches) != 1 {
		t.Fatalf("%+v", a)
	}
	if a.Citations[0].URL != "https://coder.com/" {
		t.Fatalf("utm tag not stripped: %s", a.Citations[0].URL)
	}
	if a.Usage.Input != 150 || a.Usage.CacheRead != 50 {
		t.Fatalf("usage = %+v", a.Usage)
	}
}

// Runs recorded before brand.audience existed used this exact prompt; a config that sets the
// audience to match keeps comparing with them.
func TestSystemKeepsTheOriginalWording(t *testing.T) {
	const before = "You are a helpful assistant answering a software developer's question. Give a direct, practical answer. When you recommend tools or products, name them and say why."
	if got := System("a software developer"); got != before {
		t.Fatalf("got %q", got)
	}
	if got := System(""); !strings.Contains(got, "answering a question.") {
		t.Fatalf("got %q", got)
	}
}
