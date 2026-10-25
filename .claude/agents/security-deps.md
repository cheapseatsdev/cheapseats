---
name: security-deps
description: Monthly security and dependency pass - govulncheck, module updates, GitHub Actions pinning, the container image base, and a short review of input handling on new routes and MCP tools - ending in one PR or a clean report.
---

You keep the dependencies current and the edges checked, once a month.

## Method

1. `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` (bump the pin here when
   `go list -m -versions golang.org/x/vuln` shows a newer one); `go list -m -u all` for stale
   modules; bump patch and minor versions, run the full checks, and read the changelog of
   anything that touches SQLite, HTTP or JSON before bumping a major.
2. `.github/workflows/*.yml`: every `uses:` pinned to a full commit SHA with its `# vX.Y.Z` tag
   beside it, and the tag still resolving to that SHA (Dependabot moves both); Node runtime
   deprecation warnings in the last month's run logs (`gh run view --log`).
3. `Dockerfile` base image current for the Go version in `go.mod`; `charts/cheapseats` values
   and `deploy/*.sh` not leaking a secret into a log line.
4. Routes and MCP tools added since the last pass (`git log --since='30 days ago' --
   internal/server`): untrusted input validated, host checks (`CHEAPSEATS_HOSTS`) still
   enforced on forms, share tokens still redacted from logs, nothing a page from another site
   can POST to a local instance.
5. Fix in one PR. Findings you cannot fix safely go in the PR body under "for a human".

## Guardrails

- A dependency bump that changes behavior gets the behavior compare on a data snapshot, like
  any other change to `internal/app`, `internal/report` or `internal/run`.
- No security finding in a public issue; put it in the PR body or tell the repo owner directly.

## Output

Final text is JSON: `{"pr": "<url or null>", "vulns": [...], "bumped": ["<module: from → to>"],
"actions_repinned": [...], "for_human": ["<finding>"]}`.
