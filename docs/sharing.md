# Sharing a run

A share link is a read-only page of one run's report for someone without a sign-in: a manager,
a colleague in marketing, a client. It shows:

- the headline rates (named, recommended, top pick) with their 95% intervals, without the prompts
  that name the brand, and with them when any do;
- share of voice, the top 10 products and the brand;
- the two strongest prompts (highest recommended rate over at least two answers) and the two
  weakest (a competitor was the top pick and the brand was left out of some answers, lowest named
  rate first; at most one where the brand was never named). Info and branded prompts are left out;
- the run's top themes, without their quotes;
- the top five fixes, by title and target only.

Never the config, the snapshot, the fixes' Claude Code prompts, result ids, other runs or links to
anything behind the sign-in. The answers' text (two per highlighted prompt, cut at 1,500
characters) only when the link is made with **include answers**.

## Making and revoking

- **Run page:** the Share section (finished runs only) makes a link, lists the run's links with
  who made them and when they expire, and revokes one. A new link is shown once: only its hash is
  stored.
- **API:** `POST /api/runs/{id}/shares` with `{"days": 30, "include_answers": false}` (both
  optional) returns the link as `url`; `GET /api/runs/{id}/shares` lists them; `DELETE
  /api/shares/{id}` revokes one.
- **MCP:** `share_run` with `run`, or `set` for its newest finished run, and `days`,
  `include_answers`.

Links last 30 days unless told otherwise, at most 365. A revoked or expired link answers 404, the
same as one that never existed.

## Where links point

`CHEAPSEATS_PUBLIC_URL` when set; else `https://` and the first of `CHEAPSEATS_HOSTS`; else the host and
scheme the request that made the link came in on (a proxy's `X-Forwarded-Host` and
`X-Forwarded-Proto` count). Behind a proxy, set `CHEAPSEATS_PUBLIC_URL`: a link that would point at a
loopback address gets a warning on the run page and in `share_run`'s result.

## Security

- The token is 128 random bits (22 characters, base64url), stored as its SHA-256. A copy of the
  database opens no link.
- `/share/<token>` is the only page open without a sign-in besides `/login`, `/healthz` and
  static files, and only for `GET` with a well-formed token.
- The page is sent with `X-Robots-Tag: noindex, nofollow`, `Referrer-Policy: no-referrer` (so the
  token doesn't leak to sites it links to), `Cache-Control: no-store` and `X-Frame-Options: DENY`.
- A client asking for 20 unknown tokens in a minute gets 429 for unknown tokens until the minute is
  up. A token is looked up before the limit is checked, so a link that works always opens, even
  behind a proxy where every client shares the proxy's address.
