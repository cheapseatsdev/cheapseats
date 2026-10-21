// Package redact masks known API keys and tokens in text, including their encoded forms.
package redact

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"sort"
	"strings"
)

func Values(extra ...string) []string {
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "POPCORN_TOKENS"} {
		for _, v := range strings.Split(os.Getenv(k), ",") {
			extra = append(extra, v)
		}
	}
	return extra
}
func Text(s string, values ...string) string {
	var forms []string
	for _, v := range Values(values...) {
		if v == "" {
			continue
		}
		forms = append(forms, v, url.QueryEscape(v), url.PathEscape(v), base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)), base64.URLEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)), strings.ReplaceAll(v, "'", `'\''`))
		j, _ := json.Marshal(v)
		escaped := string(j[1 : len(j)-1])
		forms = append(forms, escaped)
		j, _ = json.Marshal(escaped)
		forms = append(forms, string(j[1:len(j)-1]))
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	for _, v := range forms {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	return s
}
