# Sharing a run

A share link opens a read-only digest of one finished run without a sign-in. Anyone with the
link can read it. It shows:

- the headline rates (named, recommended, top pick) with their 95% intervals, without the prompts
  that name the brand, and with them when any do;
- share of voice, the top 10 products and the brand;
- up to two strongest prompts (highest recommended rate, at least one recommendation over two
  or more answers) and up to two
  weakest (a competitor was the top pick and the brand was left out of some answers, lowest named
  rate first; at most one where the brand was never named). Info and branded prompts are left out;
- the run's top themes, without their quotes;
- the top five fixes, by title and target only.

It excludes config, snapshots, fixes' Claude Code prompts, result ids, other runs and links behind
sign-in. **Include answers** adds up to two answers per highlighted prompt, cut at 1,500 characters.

## Making and revoking

- **Run page:** Share makes, lists and revokes links, showing their creator and expiry. Copy a new
  link when it appears: only its hash is stored.
- **API:** `POST /api/runs/{id}/shares` with `{"days": 30, "include_answers": false}` (both
  optional) returns the link as `url`; `GET /api/runs/{id}/shares` lists them; `DELETE
  /api/shares/{id}` revokes one.
- **MCP:** `share_run` with `run`, or `set` for its newest finished run, and `days`,
  `include_answers`.

Links default to 30 days, up to 365. Revoked, expired and unknown links all return 404.

## Where links point

`CHEAPSEATS_PUBLIC_URL` when set; else `https://` and the first of `CHEAPSEATS_HOSTS`; else the host and
scheme the request that made the link came in on (a proxy's `X-Forwarded-Host` and
`X-Forwarded-Proto` count). Behind a proxy, set `CHEAPSEATS_PUBLIC_URL`: a link that would point at a
loopback address gets a warning on the run page and in `share_run`'s result.

## Security

- The token is 128 random bits (22 characters, base64url), stored as its SHA-256. A database copy
  cannot recover share tokens, but still contains private answers and config snapshots.
- `/share/<token>` is the only page open without a sign-in besides `/login`, `/healthz` and
  static files, and only for `GET` or `HEAD` with a well-formed token.
- The page is sent with `X-Robots-Tag: noindex, nofollow`, `Referrer-Policy: no-referrer` (so the
  token doesn't leak to sites it links to), `Cache-Control: no-store`, and, like every page,
  `X-Frame-Options: DENY` and a `Content-Security-Policy` that refuses frames and outside images.
- After 20 unknown tokens in a minute, further unknown tokens return 429 until earlier misses
  leave that window. Valid links bypass this limit, including behind a proxy where clients share
  its address.
