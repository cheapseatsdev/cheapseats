Read [CLAUDE.md](CLAUDE.md) for repository rules. For user-facing prose and explanatory visuals, follow the [Cheap Seats editor skill](../cheapseats-site/skills/cheapseats-editor/SKILL.md) in the website checkout; its canonical source is `cheapseatsdev/cheapseats-site`, under `skills/cheapseats-editor/`.

Dashboard equations use static MathML in Go templates; the editor skill's LaTeX delimiters apply to the Hugo site. Keep model and config text autoescaped rather than treating it as trusted math or HTML.

Repository Markdown uses GitHub's fenced `math` blocks for equations and Mermaid for workflows.
Keep variable definitions and meaningful diagram titles/descriptions. Edit generated docs at
their source; `docs/states.md` is generated from the transition tables.
