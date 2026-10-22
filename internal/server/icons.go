package server

import (
	"embed"
	"fmt"
	"html/template"
	"path"
	"strings"

	"github.com/cheapseatsdev/cheapseats/internal/grade"
)

// Lucide icons (lucide-static 0.460.0, ISC: icons/LICENSE), copied in rather than loaded from a
// CDN so the app works offline and behind CHEAPSEATS_TOKENS.
//
//go:embed icons/*.svg icons/LICENSE
var iconFiles embed.FS

// iconPaths is each icon's inner markup, by name.
var iconPaths = func() map[string]string {
	files, _ := iconFiles.ReadDir("icons")
	out := map[string]string{}
	for _, f := range files {
		name, ok := strings.CutSuffix(f.Name(), ".svg")
		if !ok {
			continue
		}
		b, _ := iconFiles.ReadFile(path.Join("icons", f.Name()))
		s := string(b)
		start := strings.Index(s, "<svg")
		start += strings.Index(s[start:], ">") + 1
		out[name] = strings.TrimSpace(s[start:strings.LastIndex(s, "</svg>")])
	}
	return out
}()

// statusIcon is the icon that goes with a verdict or a run or result status, so none of them
// is told apart by color alone; nothing for one it doesn't know.
func statusIcon(s any) template.HTML {
	name := map[string]string{
		"recommended": "circle-check", "mentioned": "circle-dot", "absent": "circle-x",
		"done": "circle-check", "running": "loader-circle", "queued": "loader-circle",
		"partial": "circle-dot", "budget": "circle-dot", "failed": "circle-x", "error": "circle-x",
		"cancelled": "x", "pending": "clock", "skipped": "circle-dashed",
	}[fmt.Sprint(s)]
	if name == "" {
		return ""
	}
	h, _ := icon(name, 14)
	return h
}

// icon renders a decorative icon (hidden from screen readers: it always sits beside a label) at
// 16px, or the size given.
func icon(name string, size ...int) (template.HTML, error) {
	inner, ok := iconPaths[name]
	if !ok {
		return "", fmt.Errorf("no icon %q", name)
	}
	px := 16
	if len(size) > 0 {
		px = size[0]
	}
	return template.HTML(fmt.Sprintf(`<svg class="icon icon-%s" width="%d" height="%d" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">%s</svg>`, name, px, px, inner)), nil
}

// markPath is the Cheap Seats mark ("Back Row" in the brand kit): three rising seat backs on one
// baseline, in a 192x112 box.
const markPath = "M0 112V108Q0 100 8 100V74Q8 58 24 58H44Q60 58 60 74V100H68V54Q68 38 84 38H104Q120 38 120 54V100H128V34Q128 18 144 18H164Q180 18 180 34V100H184Q192 100 192 108V112Z"

// logo is the brand mark h pixels high, in the text color: the header's wordmark names it.
func logo(h int) template.HTML {
	return template.HTML(fmt.Sprintf(`<svg class="mark" width="%d" height="%d" viewBox="0 0 192 112" aria-hidden="true"><path fill="currentColor" d="%s"/></svg>`, h*192/112, h, markPath))
}

// The seat meter (brand kit): an answer's grade as filled seats, none for absent to all three for
// top pick. Each file's paths, by grade; colors come from --seat-* in the stylesheet.
//
//go:embed meters/*.svg
var meterFiles embed.FS

var meterPaths = func() map[string]string {
	out := map[string]string{}
	for _, g := range []string{"absent", "mentioned", "recommended", "top-pick"} {
		b, _ := meterFiles.ReadFile("meters/" + g + ".svg")
		s := string(b)
		start := strings.Index(s, "</title>") + len("</title>")
		out[g] = s[start:strings.LastIndex(s, "</svg>")]
	}
	return out
}()

// seats is the seat meter for a grade key (absent, mentioned, recommended or top-pick), or the
// status icon for anything else: an ungraded answer isn't absent. Text beside it names the grade.
func seats(key string) template.HTML {
	inner, ok := meterPaths[key]
	if !ok {
		return statusIcon(key)
	}
	return template.HTML(`<svg class="seat-meter" width="24" height="16" viewBox="0 0 24 16" aria-hidden="true" focusable="false">` + inner + `</svg>`)
}

// gradeKey is the seat meter's key for a grade: its verdict, or top-pick when the brand is the
// answer's top pick.
func gradeKey(g *grade.Grade, brand string) string {
	if g == nil {
		return ""
	}
	if g.IsTopPick(brand) {
		return "top-pick"
	}
	return string(g.Verdict)
}
