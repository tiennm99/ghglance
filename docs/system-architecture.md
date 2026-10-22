# System Architecture

## Runtime shape

One process, three phases: **flag parsing → data fetch → SVG render**.

```
┌───────────┐   ┌──────────────────┐   ┌─────────────────┐   ┌──────────────┐
│ flag / env│──►│  internal/github │──►│  internal/card  │──►│  output/*.svg│
│  parsing  │   │  (GraphQL only)  │   │  (pure render)  │   │   per theme  │
└───────────┘   └──────────────────┘   └─────────────────┘   └──────────────┘
                         ▲                       ▲
                         │                       │
                    api.github.com        internal/theme
```

No database, no cache, no background workers in CLI mode. Stateless CLI; Action runtime just sets environment variables + runs the binary. `-serve` switches the same binary into the long-running web UI described under [Web UI mode](#web-ui-mode).

A root `context.Context` is built in `main.go` with an overall deadline (`-timeout`, default 30m) and cancelled on `SIGINT`/`SIGTERM`. Every fetcher and HTTP request inherits it so a slow run aborts cleanly instead of draining the 6h Action budget.

## Data-fetch sequence

`github.Collect` owns this sequence; the CLI and every web UI job call it, so
the two paths cannot drift. Only the profile fetch is fatal; later stages
report through `CollectConfig.Warnf` and leave partial data.

```
github.Collect(ctx, client, login, cfg)
  │
  ▼
FetchProfile(ctx, login, opts)
  │  profileQuery × N pages (owned repos, STARGAZERS desc, 100/page)
  │  ownerAffiliations = [OWNER] (+ ORGANIZATION_MEMBER when
  │    opts.IncludeOrgRepos; non-ADMIN org repos dropped client-side)
  │  yields: Profile.{identity, stars, forks, PRs, issues,
  │                   TopRepos, ReposByLanguage,
  │                   ContributionYears,
  │                   DailyContributions (last year),
  │                   TotalCommits (last year)}
  │
  ▼
FetchContributionsAllTime(ctx, profile, opts)
  │  contributionYearQuery × 4 quarters × len(ContributionYears)
  │  per quarter: totalCommitContributions +
  │            contributionCalendar.weeks +
  │            commitContributionsByRepository(maxRepositories: 100)
  │  quarters keep most windows under the 100-repo ceiling, which a
  │    year-wide window silently truncates at; a quarter that still
  │    saturates is re-queried per month for repos only (its days and
  │    totals are already folded in)
  │  yields: SeedRepos (deduped),
  │          DailyContributionsAllTime,
  │          TotalCommitsAllTime
  │
  ▼
FetchProductive(ctx, profile, seeds, loc, commitsPerRepo)  // commitsPerRepo 0 = no cap
  │  seeds = SeedRepos, or with -top-repos N (cfg.TopRepos; 0 = no cap)
  │    the N most recently active seed repos
  │  commitHistoryQuery × (#seeds × pages)
  │  per commit: t = committedDate in loc
  │              ProductiveAllTime[t.Hour]++
  │              WeekdayAllTime[t.Weekday]++  + language votes
  │              if t.After(yearAgo): Productive[t.Hour]++
  │                                   Weekday[t.Weekday]++ + language votes
  │  yields: Productive, Weekday, ProductiveAllTime, WeekdayAllTime,
  │          CommitsByLanguage, CommitsByLanguageAllTime
  │
  ▼
card.RenderAll(profile, theme, outDir)  ×  len(themes)   // caller's step
```

## GraphQL queries

All three queries live in `internal/github/queries.go`.

| Query | Purpose | Cost estimate |
| --- | --- | --- |
| `profileQuery` | Profile identity + totals + owned repos + last-year calendar | 1–10 calls (100 repos/page × ≤10 pages safety cap) |
| `contributionYearQuery` | Per-quarter calendar + seed list | 4 calls per active year, +3 per saturated quarter |
| `commitHistoryQuery` | Authored commits on default branch | 1 call per 100 commits per seed repo |

Typical run (8 active years, 30 seed repos, avg 50 commits each):
- profile: 1 call
- quarter loop: 8 × 4 = 32 calls
- commit history: 30 × 1 = 30 calls
- **≈ 63 GraphQL calls, 0 REST calls**

## Attribution model

Language attribution for the "most commit language" card is **byte-weighted**:

```
for each repo R:
    total_bytes = Σ R.languages[*].bytes   // precomputed once per repo
    for each commit C in R:
        for each (lang, bytes) in R.languages:
            commits_by_lang[lang] += scaleFactor × bytes / total_bytes
```

Implementation in `internal/github/productive.go:attributeCommit`. The per-repo byte total is hoisted out of the commit loop so the hot path doesn't re-sum language edges for every commit. `scaleFactor = 10_000` preserves fractional precision in int64 storage — percentages rendered in the card are unaffected by magnitude.

Known distortion: linguist excludes prose types (Markdown, AsciiDoc, reST) from byte counts. Blog-style repos with 95% Markdown and 5% JS still attribute all commits to JS. Future fix: per-commit REST file classification via `-accurate-languages` (see roadmap).

## SVG generation

Each card produces a self-contained SVG with:
- Card frame (rounded rect, theme background, theme stroke + opacity)
- Title (top-left, theme title color)
- Content layer (chart elements, text, legend)

Shared primitives:
- `renderDonutCard(title, stats, theme)` — pie slices via polar arc math + legend with color swatches (top 7 entries, rest collapse into "Other"). Single-slice case (one language at 100%) renders as two concentric `<circle>` elements instead of an arc, since SVG's `A` command from point P back to P draws nothing.
- `renderProductiveTime(title, hours, theme)` — 24 bars + both axes + tick math from `niceTicks`
- `renderWeekday(title, data, theme)` — 7-bar day-of-week chart mirroring the productive-time layout; peak bar uses `theme.Accent`, others `mixHex(Background, Accent, 0.55)`
- `renderHeatmap(title, days, theme)` — 7×53 calendar grid with a 5-bucket intensity ramp synthesised from `theme.Background → theme.Accent`
- `renderContributions(title, days, theme)` — monthly aggregation, Catmull-Rom → cubic Bezier area path, two-sided Y axis

Chart-geometry invariants:
- `niceTicks(max, 5)` rounds the top tick up to the next step (`last = ceil(max/step) × step`), guaranteeing `yMax ≥ dataMax` — bar heights can never exceed `chartH` and collide with the title row.
- `formatTick` abbreviates ≥ 1000 to `k` / `M` / `B` so y-axis labels never exceed 4 characters (`10000 → "10k"`, `1234567 → "1.2M"`); keeps the left gutter ≤ 28 px for every profile.
- `header()` picks the largest title font in [11, 15] px at which the string fits in `width − 24` at a 0.6 char-width estimate, so long titles like `Commits by Weekday (last year, UTC+7.00)` still fit the frame.

Catmull-Rom control-point math: for each segment `P_i → P_{i+1}`,
```
C1 = P_i + (P_{i+1} - P_{i-1}) / 6
C2 = P_{i+1} - (P_{i+2} - P_i) / 6
```
Tension = 0.5 (d3's default).

## Theme model

`theme.Theme` is a pure-data struct — no methods. Cards pull `t.Background`, `t.Text`, `t.Title`, `t.Accent`, `t.Muted`, `t.Stroke`, `t.StrokeOpacity`. The 65 palettes live in a map keyed by snake_case ID.

Light themes (`default`, `github`, `nord_bright`, etc.) use `StrokeOpacity: 1` with a visible stroke color; dark themes often use `StrokeOpacity: 0` or a stroke that blends into the background.

## Failure modes

| Fault | Behavior |
| --- | --- |
| Empty `-user` | Exit 2, usage printed |
| Unknown theme | Exit 2, suggests `-list-themes` |
| GraphQL 4xx/5xx | Error wrapped with HTTP status and truncated (UTF-8-safe) body |
| Rate limit (429, primary or secondary 403, GraphQL `RATE_LIMITED`) | Wait once for `Retry-After`, else `X-RateLimit-Reset` when remaining=0, else 60 s, then retry; a wait over 5 min fails at once |
| Contribution window (quarter or month) returns nil user | Skipped; other windows still contribute, and after the loop an "all-time totals are partial" error names the missing windows: a warning in the CLI and Action, fatal for web jobs (`Strict`) |
| `FetchProductive` network error | Warn to stderr; partial data rendered |
| Unknown timezone | Warn to stderr; fall back to UTC |
| Overall timeout (`-timeout`) or Ctrl-C | `ctx` cancels in-flight requests; partial data may render |
| User with 0 commits | Card renders "No data available" |

## Web UI mode

`ghglance -serve :8080` runs `internal/web` (stdlib `net/http`,
`html/template`, `embed`; vanilla JS, no build step) instead of the one-shot
CLI path. It is for quickly viewing cards, not hosting them: the user page
inlines each card as a `data:` URI and no route serves a card by URL, so
cards cannot be linked to or embedded in Markdown.

```
POST /auth/start ─► validate ─► rate limit ─┬─ pasted token ─► Queue (below), never revoked
                                            └─ pending sign-in (state, PKCE verifier; memory, 10 min)
                 ─► 302 github.com/login/oauth/authorize (scope from ticks, state, S256 challenge)
GET /auth/callback ─► state == cookie, single use ─► POST /login/oauth/access_token
                   ─► narrow options to granted scopes (wider than ticked: revoke, refuse)
                   ─► Queue (dedup per user) ─► -workers goroutines
                                                   │  token owner check, cooldown
                                                   ▼
                       github.Collect ─► Store.Publish (every theme)
                                                   │
GET /u/{user} ◄── meta.json + os.Root card reads, inlined as data: URIs
job ends (sign-in token only) ─► DELETE api.github.com/applications/{client_id}/token
```

- **Storage.** `<data>/<user>` (lowercased login) is a symlink into
  `<data>/.gen/<user>-<nanos>/`, which holds `<theme>/<card>.svg` plus
  `meta.json` (generated time, options, `public`/`private` scope; never the
  token). Publish renders into a fresh generation directory, then renames a
  new symlink over the old one, so a reader sees the old set or the new set,
  never a partial one. Startup sweeps generations no link points at.
  Sets older than `-retention` (default `24h`) are deleted at startup and
  hourly; a store mutex keeps that removal from racing a republish.
- **Path safety.** Logins are checked against GitHub's rule (alphanumerics
  and single hyphens, 1–39 chars) and themes and card names against the
  registered lists before any path is built; reads go through `os.Root`.
- **Jobs.** In-process queue, at most one queued or running job per target
  user and at most 3 queued or running jobs per submitter, `-workers`
  concurrent, `-timeout` each, capacity 64. `SIGTERM` stops the
  HTTP server, cancels running jobs and drops queued ones; nothing is
  published mid-render.
- **Tokens.** The server has no GitHub token of its own: every job runs on
  the visitor's token, from their sign-in or pasted into the form's
  collapsed "Or use your own token" section. A non-empty pasted token
  (charset-checked like a sign-in token, which rules out header injection)
  takes precedence in `/auth/start` whichever button sent the form, and is
  queued straight away instead of starting a sign-in. GitHub folds every private
  contribution a token can see into totals and calendars, so repo filters
  alone cannot keep cards public. Each job first identifies its token
  (`viewer` query: login, a one-repo `privacy: PRIVATE` probe, and a
  classic token's `X-OAuth-Scopes`). Signed in as the target login, the job
  keeps the ticked scope and skips the cooldown; as anyone else it is
  refused if private-capable, otherwise forced to public scope under the
  cooldown. The rules are the same for both kinds of token. The token
  lives only on the job and is cleared when it ends; it is never logged,
  written to disk or echoed into the form. Only sign-in tokens are
  revoked; a pasted token belongs to the visitor.
- **Sign in with GitHub** (`internal/web/oauth.go`). OAuth App web flow,
  configuration required even though visitors may paste a token instead: `-serve` exits at startup unless `-oauth-client-id`,
  `-oauth-client-secret` and `-public-url` are all set. Scopes come from
  the ticked options (`read:user`; `repo` for private; `read:org` on top
  for org repos). `/auth/start` parks the validated submission under a
  256-bit random `state` (10-minute TTL, 1,000 entries max, at most 3
  per client key: a further start is refused, while a browser starting
  again replaces its own earlier sign-in) and binds it to
  the browser with an `HttpOnly`, `SameSite=Lax` cookie on `/`, named
  `__Host-ghglance_oauth` and `Secure` when the public URL is https so a
  sibling subdomain cannot plant it (plain `ghglance_oauth` over http, a
  weaker binding). The callback compares state and cookie in constant time,
  consumes the entry, exchanges the code with the PKCE verifier (15 s
  timeout), drops options whose scope GitHub did not grant, revokes and
  refuses a token carrying scopes the ticks did not ask for (GitHub folds
  earlier grants into new tokens), and hands the token to the queue. The
  worker revokes it before it reports the job finished; `Stop` revokes the
  tokens of queued jobs it drops, and a callback whose job is not queued
  revokes at once. Revocation is best effort and logs only the status.
  GitHub endpoints come from `OAuthConfig.WebURL`/`APIURL`, so tests run
  against `httptest`. The page CSP adds GitHub's origin to `form-action`,
  since browsers check a form submission's redirect against it.
- **Partial fetches.** The web path runs `github.Collect` with `Strict`, so
  a failed all-time or commit-history stage fails the job, and a job whose
  deadline passed is failed even if the fetch returned. The CLI keeps
  rendering partial data with warnings.
- **HTTP hardening.** Strict CSP on pages, with `img-src 'self' data:` for
  the inlined cards (an SVG loaded through `<img>` runs no scripts and
  fetches nothing, and stays isolated from the page and the other cards),
  `nosniff`, 16 KiB form limit, `http.CrossOriginProtection` on the
  POSTs, per-client token bucket (burst 5, +1 per 2 min) keyed by IPv4
  address or IPv6 /64, capped at 10,000 tracked clients.

## Extension points

- **New card**: implement `Card` interface, add to `allCards` in `card.go`.
- **New theme**: add entry to `themes` map in `theme.go`.
- **New fetcher mode** (e.g., REST per-commit): add a new method on `*Client`, call from `github.Collect`, wire to new `Profile` fields.
