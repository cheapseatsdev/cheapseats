Read [CLAUDE.md](CLAUDE.md) for repository rules. For user-facing prose and explanatory visuals, follow the [Cheap Seats editor skill](../cheapseats-site/skills/cheapseats-editor/SKILL.md) in the website checkout; its canonical source is `cheapseatsdev/cheapseats-site`, under `skills/cheapseats-editor/`.

Dashboard equations use static MathML in Go templates; the editor skill's LaTeX delimiters apply to the Hugo site. Keep model and config text autoescaped rather than treating it as trusted math or HTML.
