# Codebase Summary

## Layout

```
ghglance/
├── main.go                              # CLI entry point; parses flags, calls github.Collect → card.RenderAll, or web.Run for -serve
├── action.yml                           # GitHub Action metadata
├── entrypoint.sh                        # Action runtime; maps INPUT_* env → CLI flags
├── Dockerfile                           # Multi-stage build for the Action and web UI image
├── compose.yml                          # Runs the -serve web UI (Coolify or plain Docker)
├── .env.example                         # OAuth App settings compose.yml requires
├── go.mod                               # Module declaration; no external deps
├── internal/
│   ├── github/                          # GraphQL client + fetchers + models
│   │   ├── client.go                    # HTTP POST to /graphql, error decoding
│   │   ├── queries.go                   # profileQuery, commitHistoryQuery, contributionYearQuery
│   │   ├── model.go                     # Profile, RepoInfo, LangStat, LangEdge, DailyContribution
│   │   ├── collect.go                   # Collect (fetch sequence for CLI + web), TokenInfo, UTCOffsetLabel, ParseWeekday
│   │   ├── profile.go                   # FetchProfile — user + owned repos + stats + calendar
│   │   ├── productive.go                # FetchProductive — commit history → hour + weekday histograms + lang buckets
│   │   ├── contributions_all_time.go    # FetchContributionsAllTime — per-quarter loop → seed list + daily series
│   │   └── *_test.go                    # see "Test coverage"
│   ├── card/                            # SVG renderers; one file per card
│   │   ├── card.go                      # Card interface, RenderAll, allCards slice
│   │   ├── svg.go                       # escapeXML, formatInt, truncate, header (auto-fit title), footer
│   │   ├── axis.go                      # niceTicks (d3-style, last tick ≥ max), formatTick (1500→"1.5k")
│   │   ├── icons.go                     # Octicon path strings
│   │   ├── profile.go                   # profile-details
│   │   ├── repos_per_language.go        # repos-per-language
│   │   ├── most_commit_language.go      # most-commit-language
│   │   ├── most_commit_language_all_time.go  # most-commit-language-all-time
│   │   ├── stats.go                     # stats
│   │   ├── productive.go                # productive-time (+ all-time)
│   │   ├── productive_weekday.go        # productive-weekday (+ all-time)
│   │   ├── contributions.go             # contributions (+ all-time)
│   │   ├── contributions_heatmap.go     # contributions-heatmap (7×53 calendar grid; row 0 = Profile.WeekStart)
│   │   ├── contributions_by_year.go     # contributions-by-year bar chart
│   │   ├── streak.go                    # streak (current/longest/active days)
│   │   ├── top_starred_repos.go         # top-starred-repos bar list
│   │   ├── records.go                   # records (lifetime personal bests)
│   │   ├── donut_chart.go               # renderDonutCard — shared by language cards
│   │   └── *_test.go                    # see "Test coverage"
│   ├── theme/
│   │   ├── theme.go                     # 65-palette map ported from github-profile-summary-cards
│   │   └── theme_test.go                # theme data tests
│   └── web/                             # Optional -serve web UI (stdlib net/http + html/template + embed)
│       ├── server.go                    # Config, Run, routes, page rendering, security headers
│       ├── oauth.go                     # "Sign in with GitHub" OAuth App flow, PKCE, token revocation
│       ├── jobs.go                      # In-process job queue and workers
│       ├── store.go                     # On-disk card sets, atomic publish, expiry
│       ├── ratelimit.go                 # Per-client token bucket
│       ├── validate.go                  # Login/theme/card checks, form parsing
│       ├── templates/                   # HTML pages (embedded)
│       ├── static/                      # CSS, JS, favicon (embedded)
│       └── *_test.go                    # see "Test coverage"
├── .github/workflows/
│   ├── ci.yml                           # go vet + go test on push/PR
│   ├── release.yml                      # GHCR image + cross-platform binaries on tag
│   └── demo.yml                         # Renders every theme for the repo owner on push to main
├── docs/                                # This directory
├── plans/                               # Research reports + implementation plans
└── demo/                                # Auto-generated gallery
    ├── README.md                         # Lightweight index (links only, zero images)
    └── <theme>/                          # Per-theme page: 16 SVGs + README pairing LY / AT variants
                                          # (`output/` is entirely gitignored; see demo/ for reference renders)
```

## Module responsibilities

### `internal/github`

All network I/O. Exposes a `*Client` with three fetchers; every call takes a `context.Context` so pagination aborts cleanly on timeout or Ctrl-C:

| Fetcher | Input | Populates |
| --- | --- | --- |
| `FetchProfile(ctx, login, opts)` | username, visibility flags | Profile basics, totals, owned-repos aggregation, last-year daily calendar, `TopRepos` |
| `FetchContributionsAllTime(ctx, p, opts)` | Profile | `SeedRepos`, `DailyContributionsAllTime`, `TotalCommitsAllTime` |
| `FetchProductive(ctx, p, repos, loc, cap)` | Profile + seed + tz + cap | `Productive`, `Weekday`, `CommitsByLanguage`, `ProductiveAllTime`, `WeekdayAllTime`, `CommitsByLanguageAllTime` |

Call order in `github.Collect` (used by the CLI and every web job): Profile → AllTime → Productive. `Collect` also applies the optional `-top-repos` cap to the seed list before `FetchProductive`; only a profile failure is fatal unless `CollectConfig.Strict` is set (the web path sets it). `Client.query` handles GitHub rate limits transparently — on 429 or 403 with `X-RateLimit-Remaining: 0`, it honors `Retry-After` / `X-RateLimit-Reset` (capped at 5 minutes) and retries once.

### `internal/card`

Pure rendering. Every card implements the `Card` interface:

```go
type Card interface {
    Filename() string
    SVG(*github.Profile, theme.Theme) ([]byte, error)
}
```

`RenderAll` iterates `allCards`, writes each to `<outDir>/<themeID>/<Filename>`.

Shared helpers:
- `renderDonutCard` — language donut + legend (used by 3 language cards)
- `renderProductiveTime` — 24h bar chart (used by both productive-time cards)
- `renderWeekday` — 7-bar day-of-week chart (used by both productive-weekday cards)
- `renderContributions` — smooth area chart (used by both contributions cards)
- `renderHeatmap` — 7×N calendar grid with `mixHex`-derived intensity ramp
- `mixHex` / `parseHex` — `#rrggbb` blending (used by heatmap, by-year, weekday for peak-vs-dim bars)
- `header`, `footer` — SVG chrome
- `niceTicks`, `formatTick` — axis math

### `internal/theme`

Static map of 65 themes. Each theme specifies title/text/background/stroke/accent/muted plus `StrokeOpacity` for correct light-theme borders.

### `internal/web`

Optional HTTP UI started by `-serve`: GitHub sign-in or a pasted visitor token, an in-process job queue that calls `github.Collect` and `card.RenderAll`, and an on-disk card store whose pages inline the cards. Routes, storage, token handling and hardening are described in [system-architecture.md#web-ui-mode](./system-architecture.md#web-ui-mode).

## Card ↔ data flow

```
profileQuery ─────► Profile.{identity, owned repos, totals, last-year calendar}
                                │
contributionYearQuery ─┬──► SeedRepos + DailyContributionsAllTime + TotalCommitsAllTime
                       │
                       └─ seed into ─►
                                │
commitHistoryQuery ──► Productive + Weekday + CommitsByLanguage (+ AllTime variants)
                                │
                                ▼
                          16 SVG files per theme
```

## Test coverage

- `internal/card/card_test.go` — `TestRenderAll` checks that every card filename it lists renders as a valid SVG; XML escape through real render pipeline; `formatInt` cases; `TestDonutSingleSlice` / `TestDonutEmpty` (donut edge cases); `TestCardsFitFrame` (renders every card against an adversarial profile and asserts text + coordinates stay in the 340×200 frame); `TestFitTitleFontSize` (pins the auto-shrink table for every real title); `TestNiceTicksCoversMax` (guards the `yMax ≥ dataMax` invariant so bars can't overflow chartH).
- `internal/card/weekday_start_test.go` — `TestPadToWeekGridRotatesByWeekStart` (leading pad matches configured start day), `TestRenderWeekdayRespectsWeekStart` (bar order rotates with `WeekStart`), `TestRenderHeatmapLabelsRespectWeekStart` (heatmap row labels rotate accordingly), `TestRenderHeatmapMonthLabelsSkipPaddedCells`.
- `internal/card/contributions_trim_test.go` — trimming the last-year series to a clean 13-month span, and empty input.
- `internal/card/records_test.go` — each records helper (peak day and month, first active day, active days, account age, languages used) and the records card on a full and an empty profile.
- `internal/github/profile_test.go` — `sortLangStats` ordering and tiebreak.
- `internal/github/collect_test.go` — `TestUTCOffsetLabel` covers UTC, Asia/Saigon, half-hour (Kolkata), quarter-hour (Kathmandu) zones. `TestParseWeekday` covers the `-start-of-week` input parsing. `TestScopesIncludeRepo` covers reading a classic token's `X-OAuth-Scopes` as private-capable.
- `internal/github/commit_cap_test.go` — `reachedCommitCap`, the per-repo commit sampling limit.
- `internal/github/contribution_scope_test.go` — quarter and month contribution windows (full year, current year clamped to now, future year, months covering a quarter exactly) and owner affiliations.
- `internal/theme/theme_test.go` — theme data.
- `internal/web/web_test.go` — validation, form parsing, store publish/sweep/expiry and path safety, queue dedup and token-owner rules, handlers, rate limiter and client keys. A fake `Fetcher` stands in for GitHub.
- `internal/web/oauth_test.go` — the sign-in flow against a fake GitHub OAuth server (`httptest.NewServer`): scopes, state and cookie checks, expiry, the pending-sign-in cap, scope downgrades and wider grants, and token revocation.
- `internal/web/pasted_token_test.go` — pasted-token precedence, foreign accounts, never echoing the token, and no revocation on shutdown.

No test calls the real GitHub API: the github fetchers are verified by real runs (`-token` + local build), and internal/web tests use `httptest` with fakes.

## Naming conventions

- Go files use snake_case for multi-word names (`repos_per_language.go`, `contributions_all_time.go`).
- Cards' `Filename()` returns a plain kebab-case name (`profile-details.svg`, `most-commit-language-all-time.svg`, …). Embedders reference by name, so no numeric prefix is needed.
- Themes in snake_case to match upstream (`github_dark`, `nord_bright`).
