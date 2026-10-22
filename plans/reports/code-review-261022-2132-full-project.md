# Code review: ghglance full project (2026-10-22)

## Verdict

The project is in good shape. Nothing found here crashes, corrupts stored data or leaks credentials. Most findings are correctness and wording gaps at the edges. The most important ones are these:

- A login typed with different capitals silently zeroes every repo-derived number on the cards.
- Rate-limit handling rarely retries, and when it does it wastes five minutes.
- Two themes render a heatmap with no shading.
- Sparse accounts get an 18-character y-axis label.
- The pasted-token path can publish private contribution totals under a "public" label.
- The `commit_branch` input fails on every run after the first.

The `internal/github` package has 13% test coverage. Its HTTP client can be faked today by setting `Client.http` to an `http.Client` with a custom `RoundTripper`, so the missing fetch tests need no production change.

Severities below are the majority of the three verifier ratings. Several findings were filed as high or medium and were lowered by the verifiers.

## Confirmed findings

| ID | Severity | Location | Title |
| --- | --- | --- | --- |
| 1 | medium | internal/github/profile.go:95 | Owned-repo filter compares the owner login case-sensitively against user input |
| 2 | medium | internal/github/contributions_all_time.go:83 | The saturated-quarter month re-query has no test |
| 3 | medium | internal/github/client.go:139 | Rate-limit retry never fires for GraphQL RATE_LIMITED or 403 secondary limits |
| 4 | medium | internal/github/client.go:96 | The "reset too far away" guard is dead code because the wait is already clamped |
| 5 | medium | internal/github/productive.go:94 | A per-repo error drops both commits-by-language cards while hour and weekday stay partial |
| 8 | medium | internal/card/contributions_heatmap.go:211 | mixHex rejects 8-digit hex, so the transparent and panda heatmaps have no shading |
| 9 | medium | internal/card/axis.go:46 | niceTicks accumulates floats and allows sub-1 steps, giving a "0.6000000000000001" label |
| 11 | medium | internal/web/ratelimit.go:57 | A full rate-limiter table refuses every new client |
| 12 | medium | entrypoint.sh:56 | `commit_branch` resets the branch to HEAD, so the push to an existing branch is rejected |
| 14 | medium | docs/deployment-guide.md:170 | The rollback guide says `@v1` users are safe after deleting a bad tag |
| 21 | medium | internal/web/templates/form.html:70 | "Create a token" always requests `repo`, which is then refused for other usernames |
| 40 | medium | internal/web/jobs.go:349 | An own pasted token with private access publishes private totals labelled "public" |
| 7 | low | internal/github/collect.go:67 | `-top-repos` keeps the oldest repos, not the most active ones |
| 10 | low | internal/web/oauth.go:300 | The global 1000-entry pending sign-in cap can be held full by one attacker |
| 13 | low | action.yml:76 | `-timeout` has no Action input and no `INPUT_TIMEOUT` translation |
| 15 | low | docs/project-roadmap.md:44 | The roadmap lists server mode as out of scope, but `-serve` ships |
| 16 | low | docs/project-overview-pdr.md:16 | The PDR non-goal contradicts the shipped web UI |
| 17 | low | docs/codebase-summary.md:12 | The codebase layout omits internal/web and several newer files |
| 18 | low | internal/github/productive.go:106 | FetchProductive and attributeCommit have no tests |
| 19 | low | internal/github/profile.go:104 | FetchProfile parsing, pagination and filtering are untested |
| 20 | low | internal/github/contributions_all_time.go:144 | The github package writes warnings to os.Stderr, bypassing Warnf and Strict |
| 22 | low | internal/card/contributions_heatmap.go:162 | Integer-division thresholds paint light contributors' days at full intensity |
| 23 | low | internal/card/contributions.go:179 | The x-axis stride guard never fires for stride 2 or 3, so the last labels overlap |
| 24 | low | internal/web/jobs.go:313 | A refused job shows the submitter's login on the target's public page |
| 26 | low | internal/web/jobs.go:148 | One submitter can fill the whole 64-slot queue |
| 27 | low | internal/web/server.go:162 | The shutdown budget exceeds Docker's 10s grace, so token revocation can be killed |
| 28 | low | docs/deployment-guide.md:146 | The prebuilt-image switch uses tag `:v1`, which is never published |
| 29 | low | .github/workflows/release.yml:113 | A prerelease tag would force-move `v1` and `latest` |
| 30 | low | docs/project-overview-pdr.md:5 | The PDR says 15 cards; the registry renders 16 |
| 31 | low | docs/codebase-summary.md:66 | The doc says main.go sequences the fetchers; github.Collect does |
| 32 | low | docs/codebase-summary.md:115 | Test coverage cites a main_test.go that does not exist |
| 33 | low | docs/code-standards.md:23 | The package-structure rule omits internal/web |
| 34 | low | internal/web/oauth.go:227 | OAuth refusal and start-time error paths are untested |
| 35 | low | internal/card/productive.go:67 | Y-scale and y-axis rendering is copy-pasted across four chart cards |
| 36 | low | internal/card/svg.go:130 | Shared font-size constants are mostly unused |
| 37 | low | internal/web/static/app.js:13 | Timezone auto-detect overwrites an explicit UTC choice after an error |
| 38 | low | internal/web/static/app.js:109 | Status polling hides failures behind a frozen progress line |
| 39 | low | internal/web/templates/user.html:21 | Progress copy promises an auto-refresh that needs JavaScript |
| 41 | low | README.md:399 | `include_private=false` is documented as hiding private work, but only repo lists are filtered |
| 42 | low | internal/github/profile.go:153 | "Total PR Reviews" counts only the last year, unlike the rows beside it |
| 43 | low | internal/card/top_starred_repos.go:33 | Top Starred Repos prints private repo names under a "public repos" empty state |

## Findings

### 1. Owned-repo filter compares the owner login case-sensitively (medium)

**Scenario.** A visitor types `TienNM99`, or a CLI user passes `-user TienNM99`. GitHub's `user(login:)` lookup ignores case, so the query succeeds. `FetchProfile` keeps the raw input as `p.Login` (profile.go:109), and `ownedByUser` compares `r.Owner.Login == login` exactly (profile.go:95). Every owned repo fails the check, so stars, forks, repo count, repos-per-language and top-starred all come out empty with no warning. The web layer is case-insensitive only for storage keys and the token-owner check, not for the login passed to `Collect`.

**Evidence.** `p := &Profile{Login: login}` at profile.go:109; `if r.Owner == nil || r.Owner.Login == login {` at profile.go:95. `contributions_all_time.go:136` and `:188` also reuse the raw login.

**Fix.** On page 0, set `p.Login = u.Login` (the canonical login, already decoded at profile.go:14) and pass `p.Login` to `ownedByUser` and `toRepoInfo`. This also fixes the displayed name and the later contribution pass. `strings.EqualFold` alone would fix the filter but leave the user's casing on the card.

**Verifiers.** All three confirmed it from the code and lowered it to medium: it needs mixed-case input and has an easy workaround, but it silently publishes zeroed aggregates, including on the public web gallery.

### 2. The saturated-quarter month re-query has no test (medium)

**Scenario.** `FetchContributionsAllTime` and `fetchContributionWindow` are at 0% coverage. If a refactor flipped `foldCalendar` on the month re-queries or dropped the `saturated` check, users with 100 or more repos in a quarter would get doubled all-time commits and duplicate calendar days, and `@v1` callers would publish them with no failing test. The current code is correct.

**Evidence.** contributions_all_time.go:83 passes `foldCalendar=true` for quarters and :96 passes `false` for months; :154 folds totals only under `if foldCalendar`; :170 sets `saturated`.

**Fix.** Test only, with no production change. In a package `github` test, set `c.http = &http.Client{Transport: roundTripFunc(...)}` and answer canned GraphQL JSON by switching on the `from`/`to` variables. Return 100 repos for one quarter and distinct repos for each month. Assert that `TotalCommitsAllTime` and the length of `DailyContributionsAllTime` match the quarter alone, that month-only repos land in `SeedRepos` once, and that fork and private repos are filtered according to `opts`. Making the endpoint a field is unnecessary.

**Verifiers.** Two confirmed the gap at medium; one refuted it as a live defect and rated it low. All three agreed the current code is correct and that the "endpoint is a constant" claim does not block a test.

### 3. Rate-limit retry never fires for GraphQL or secondary limits (medium)

**Scenario.** `rateLimited()` returns true only for a 429, or a 403 whose `X-RateLimit-Remaining` is `"0"`. A secondary-limit 403 carries `Retry-After` with Remaining above 0, so it fails at once with `http 403`. A `RATE_LIMITED` error inside a 200 body is returned as `graphql: ...` without any rate-limit check. The Retry-After branch in `rateLimitWait` therefore almost never runs, and long all-time fetches abort instead of waiting. `rateLimited`, `rateLimitWait`, `clampDuration`, `truncate` and `queryHeader` have no tests.

**Evidence.** client.go:138-141 checks only `remaining == "0"` on a 403; the `errors[]` array is turned straight into an error without reading `gqlError.Type`.

**Fix.** Treat a 403 that carries `Retry-After`, or whose body mentions a secondary rate limit, as rate-limited. After decoding a 200, treat `errors[].type == "RATE_LIMITED"` (or `Remaining == "0"`) the same way. Do this only on attempt 0, so the loop still ends after one retry. A primary-limit reset is usually beyond the 5-minute cap, so for that case the fix mostly turns the error into the clearer "resets in ..." message; the real gain is for secondary limits. Add tests through a fake `RoundTripper`: 429 then 200; a 403 with Retry-After and Remaining above 0; a reset beyond the cap (error, no sleep); a GraphQL errors array; ctx cancelled during the sleep; and `truncate` on a multi-byte boundary. Also fix the `rateLimited` doc comment, which misdescribes GitHub's secondary-limit signal.

**Verifiers.** Two kept medium; one rated it low because the primary-limit case still fails and the fetch pipeline is sequential, so secondary limits are uncommon.

### 4. The "reset too far away" guard is dead code (medium)

**Scenario.** `rateLimitWait` clamps both the Retry-After path and the reset path to `maxRateLimitSleep` (5m), so `wait > maxRateLimitSleep` at client.go:96 can never be true. With a reset 45 minutes away, the client sleeps 5 minutes, retries once while still limited and fails with a generic `http 403`. docs/deployment-guide.md:183 and :191 and docs/system-architecture.md:141 promise the fail-fast error instead.

**Evidence.** client.go:152 and :159 both return `clampDuration(...)`; `clampDuration` caps at the maximum.

**Fix.** Make `rateLimitWait` return the unclamped duration, keep the 60s fallback, and delete `clampDuration`. Update the `rateLimitWait` doc comment ("capped at maxRateLimitSleep"). Add tests with a reset an hour away and with `Retry-After: 3600`, both expecting the fail-fast error and no sleep.

**Verifiers.** Two kept medium, citing the documented contract and the wasted 5 minutes of a 30-minute timeout; one rated it low because the run fails either way.

### 5. A per-repo error drops both commits-by-language cards (medium)

**Scenario.** `FetchProductive` fills the hour and weekday buckets inside the loop but builds `CommitsByLanguage` and `CommitsByLanguageAllTime` only after it. If one repo query fails, or the 30-minute deadline expires, the early `return err` at productive.go:94 leaves both language slices nil. In non-strict mode `Collect` only warns, so the CLI and Action render partial hour and weekday cards beside two empty language donuts. This contradicts the `Collect` doc comment, which promises partially filled aggregates. The web path sets `Strict` and is not affected.

**Evidence.** productive.go:94 `return err`; productive.go:126-127 assign the language slices after the loop.

**Fix.** Assign the two `sortLangStats` results on every exit path, for example with a deferred assignment. Do not switch to warn-and-continue on per-repo errors: Strict mode would then silently publish incomplete data.

**Verifiers.** All three confirmed it at medium. The third noted that warn-and-continue would defeat Strict mode, so only the deferred assignment should be used.

### 8. mixHex rejects 8-digit hex colors (medium)

**Scenario.** With `theme=transparent` (Background `#00000000`) or `theme=panda` (Accent `#19f9d899`), `parseHex` fails because it requires length 7, and `mixHex` returns Accent for every ramp level. Every heatmap cell, including zero-count days, gets the same color. The "dim" fills in productive_weekday.go and contributions_by_year.go collapse to Accent too. The committed demos show it: demo/transparent/contributions-heatmap.svg has 369 cells all `#006AFF`, and demo/panda has 388 cells all `#19f9d899`. `internal/theme` has no tests and every card test uses dracula.

**Evidence.** `parseHex` at contributions_heatmap.go:211 returns false unless `len(s) == 7`; the ramp is built at :54-60; theme.go:72 and :84 define the two 8-digit colors.

**Fix.** Do not just accept `#rrggbbaa` and drop the alpha: transparent would then blend from opaque black and panda would lose its 60% alpha. Instead, when either color has alpha, render the cells as Accent with `fill-opacity` stepping through 0, 0.25, 0.5, 0.75 and 1 (the repo already uses `fill-opacity` in contributions.go and top_starred_repos.go), or blend RGBA with the alpha carried through. Apply the same treatment to the dim fills. Add a table test over `theme.IDs()` asserting the five ramp colors differ for every theme, and a theme test that every ID round-trips through `Lookup` and every color is valid `#rrggbb` or `#rrggbbaa`.

**Verifiers.** All three confirmed it at medium from the code and the demo SVGs. The third corrected the fix: ignoring alpha would regress both themes, so use fill-opacity steps or an RGBA blend.

### 9. niceTicks produces a "0.6000000000000001" label (medium)

**Scenario.** All four chart cards replace a max of 0 with 1 and call `niceTicks(yMax, 5)`. For max 1, the step is 0.2, and repeated `v += step` gives `[0 0.2 0.4 0.6000000000000001 0.8 1]`. `formatTick` prints it with `FormatFloat(v, 'f', -1)`, so every empty or near-empty account gets an 18-character label in a gutter sized for 4. Max 2 and 3 also give 0.5, 1.5 and 2.5 ticks on integer count axes.

**Evidence.** The accumulating loop is at axis.go:46 (`for v := 0.0; v <= last+step/1e9; v += step`); `formatTick` is at axis.go:65. Callers are productive.go:67, productive_weekday.go:75, contributions_by_year.go:54 and contributions.go:92.

**Fix.** After choosing the 1/2/5 step, clamp it to at least 1, which gives `[0 1]`, `[0 1 2]` and `[0 1 2 3]` and keeps the last tick at or above max. Also build each tick as `float64(i)*step` instead of accumulating. Add max 1, 2 and 3 cases to card_test.go asserting every tick is whole and every label is at most 4 characters.

**Verifiers.** All three reproduced it by running the function and kept medium. They noted it is more common than reported because empty charts are forced to max 1.

### 11. A full rate-limiter table refuses every new client (medium)

**Scenario.** Once the limiter holds 10,000 keys, `Allow` returns false for every key not already present. A bucket is swept only after it has fully refilled, which takes 10 minutes. `clientKey` groups IPv6 by /64, so an attacker with a routed /48 sends 10,000 cheap POSTs (Allow runs before validation, and requests without Origin or Sec-Fetch-Site pass `CrossOriginProtection`) and keeps every new visitor on 429. About 17 requests per second sustains it.

**Evidence.** ratelimit.go:54-59 refuses when `len(l.buckets) >= maxBuckets`; the sweep at :77-85 drops only fully refilled buckets; readSubmission calls Allow in server.go:246 before checking the parse error.

**Fix.** When the table is full, evict instead of refusing. Sample a few entries from map iteration, or keep an LRU list, and drop the stalest or fullest bucket; avoid a full scan on every insert. If a coarser IPv6 limit is added, it must be generous or apply only when the table is full, because ISPs hand out /56s and /60s from shared /48s.

**Verifiers.** Two kept medium; one rated it low because it needs an IPv6 allocation and sustained effort, and only affects the web UI. One noted there is a single readSubmission caller, not two.

### 12. `commit_branch` push is rejected on every run after the first (medium)

**Scenario.** `git fetch origin "$commit_branch"` only writes FETCH_HEAD, and `git checkout -B "$commit_branch"` with no start point recreates the branch at the workflow's HEAD. The first run creates the branch. Every later scheduled run builds a commit that is not a descendant of the remote branch, the plain `git push origin HEAD` is rejected as non-fast-forward, and `set -eu` fails the step. A verifier reproduced this in a scratch repo.

**Evidence.** entrypoint.sh:54-64: `git fetch origin "$commit_branch" || true`, `git checkout -B "$commit_branch"`, then `git push origin HEAD`.

**Fix.** Do not use `git checkout -B "$commit_branch" FETCH_HEAD`: when the target branch tracks the card paths, the new untracked SVGs make checkout abort. Move the branch and index without touching the working tree instead:

```sh
if git fetch origin "$commit_branch"; then
  git checkout -B "$commit_branch"
  git reset -q FETCH_HEAD
else
  git checkout -B "$commit_branch"
fi
```

After the mixed reset, `git add "$out"` stages only the card files and the push is a fast-forward. This works with the default shallow `actions/checkout`. Documenting that the branch must be new is not acceptable, because the feature would still fail on its second run.

**Verifiers.** Two confirmed it at medium. The third agreed the bug is real but refuted the original fix for the reason above and supplied the reset-based fix.

### 14. The rollback guide misleads about `@v1` (medium)

**Scenario.** A maintainer ships a broken release and follows the rollback steps: delete the patch tag, its release and its GHCR tag. `update-major-tag` has already force-moved `v1` to the broken commit, and nothing moves it back. The guide says `@v1` users "keep working", so nobody re-points `v1`, and every Marketplace caller keeps running the broken code.

**Evidence.** docs/deployment-guide.md:168-170; release.yml:116-117 runs `git tag -f "$major" "$TAG"` and `git push -f origin "$major"`.

**Fix.** Add a step to re-point the floating tag: `git tag -f v1 <previous good tag> && git push -f origin v1`. This restores Action callers completely, because action.yml builds from the Dockerfile at the tagged commit. For GHCR, release.yml publishes `{{version}}`, `{{major}}.{{minor}}`, `{{major}}` and `latest`; re-point `1`, `1.<minor>` and `latest` to the last good digest with `docker buildx imagetools create`, or delete `1.<minor>` if the bad release was the first in its minor line.

**Verifiers.** All three confirmed it at medium; the third added the missing GHCR tags.

### 21. "Create a token" always requests `repo` (medium)

**Scenario.** A visitor who wants public cards for another user clicks "Create a token on GitHub", which pre-ticks `repo`. The job's `TokenInfo` sees `CanReadPrivate` on a token that is not the target's, and the job fails with "your token belongs to X and can read private repositories". The one-click path leads straight into the refusal and gets the visitor to create a write-capable token they did not need.

**Evidence.** form.html:70 links to `settings/tokens/new?scopes=repo,read:user`; jobs.go:311-313 refuses the token; web_test.go:471 asserts the fixed href.

**Fix.** Default the link to `scopes=read:user`. In app.js, reuse `oauthScopes()` and the change listeners from `wireOAuthScopes` to switch the href to `repo,read:user` (plus `read:org` when org repos are ticked) only while private repos are ticked. Rewrite the adjacent hint, which says the `repo` scope is pre-ticked, with the same sync. Update the assertion in web_test.go:471.

**Verifiers.** Two kept medium; one rated it low because the error message is clear and the path is behind a collapsed section. The third added the test and hint updates.

### 40. Own pasted token publishes private totals labelled "public" (medium)

**Scenario.** A visitor pastes their own token that can read private repos and leaves "Include private repos" unticked. `run()` treats the token as their own, so the `CanReadPrivate` check is skipped. GitHub folds every private contribution the token can see into the totals, calendar, streak and records, while `Meta.Scope` is set to `"public"` and the public page shows "public data". The reverse case is mislabelled too: an own `read:user` token with the box ticked is labelled "private data". The sign-in path refuses this mismatch (oauth.go:440).

**Evidence.** jobs.go:309 `own := strings.EqualFold(info.Login, j.login)`; lines 310-324 act only when `!own`; line 349 sets the scope from the tick alone. The comment at jobs.go:284-286 already explains why filtering repo lists is not enough.

**Fix.** In `run()`, when `own && info.CanReadPrivate && !opts.IncludePrivate`, refuse the token. Write a message for pasted tokens, such as "your token can read private repositories: tick Include private repos, or paste a token with only read:user"; do not reuse the sign-in message, which refers to revoking the OAuth app. Refusing is better than silently treating the run as private, which would override the untick. Set `Scope` from `own && info.CanReadPrivate && opts.IncludePrivate`. Add pasted-token tests for both cases and update the form hint (form.html:77) to match.

**Verifiers.** Two rated it medium, noting that the default token link (finding 21) steers users straight into this case; one rated it low because only the owner's own aggregate counts are exposed.

### 7. `-top-repos` keeps the oldest repos (low)

**Scenario.** `FetchContributionsAllTime` walks years in ascending order and appends seed repos in first-seen order, and `Collect` keeps the first `TopRepos` entries. A user active since 2015 with `-top-repos 10` gets only 2015-era repos probed, so the last-year productive and language cards can come out nearly empty. `Collect` has no tests, so neither this ordering nor the Strict and non-Strict branches are covered.

**Evidence.** contributions_all_time.go:76 `sort.Ints(years)`; :193-194 append in first-seen order; collect.go:65-68 slices `SeedRepos[:cfg.TopRepos]`. The per-window `contributions { totalCount }` is queried (queries.go:113) and decoded but never read.

**Fix.** Rank `SeedRepos` before the cap. Ranking by most recent activity is simplest. If ranking by contribution count, add only quarter-level (`foldCalendar=true`) counts, and for a saturated quarter use month sums only for repos missing from the quarter list, to avoid double counting. Default runs are unaffected because `top_repos` defaults to 0 and the web path never sets it. Update the knob wording in the README CLI and Action tables, the action.yml input description and docs/system-architecture.md:56. Add a `Collect` test with a fake transport covering which repos survive the cap, Strict turning a stage failure into an error, and non-Strict calling `Warnf` and returning the profile.

**Verifiers.** One kept medium; two rated it low because the cap is opt-in and off by default. The third added the double-counting caution and the doc updates.

### 10. Pending sign-in cap can be held full (low)

**Scenario.** `POST /auth/start` needs no token. Each start adds a pending login that lives 10 minutes, and the global map refuses new entries at 1000. The only per-client limit is the per-IPv4 or per-IPv6-/64 bucket (burst 5, then one every 2 minutes), so 200 /64s or 200 IPv4 addresses keep the map full and every real visitor gets 503. Pasted-token submissions bypass this path and still work.

**Evidence.** `pendingLogins.add` (oauth.go:296-312) sweeps only expired entries, then returns `errTooManyLogins`; `maxPendingLogins = 1000`, `loginTTL = 10 * time.Minute`.

**Fix.** Make `add()` evict the earliest-expiring entry when full, keeping the 1000 cap purely as a memory bound. At the attacker's sustained rate, a real user's entry survives long enough to finish the GitHub redirect. A per-client cap alone does not help against many /64s; a /48 limiter is an optional extra. Correct the comment at oauth.go:35-36, which says the submission rate limit bounds one client.

**Verifiers.** Two rated it low because it affects one optional sign-in path and needs sustained effort; one kept medium and corrected the fix to favour eviction.

### 13. `-timeout` is not exposed to the Action (low)

**Scenario.** An Action caller with a large account sets `commits_per_repo: "0"`, runs past the fixed 30-minute deadline, and the job fails with no way to raise the limit. The README marks only `-serve` through `-public-url` as server-only, so `-timeout` falls under the five-place rule in CLAUDE.md.

**Evidence.** main.go:33 defines the flag; README.md:176 lists it; action.yml has no `timeout` input and entrypoint.sh passes no `-timeout`.

**Fix.** Add a `timeout` input defaulting to `"30m"` to action.yml (say that `0` means no limit and that the job's `timeout-minutes` still caps the run), add `timeout="${INPUT_TIMEOUT:-30m}"` and `-timeout "$timeout"` to entrypoint.sh, and add a row to the README Action inputs table and the flag to the workflow YAML example. The default keeps current behaviour for `@v1` callers.

**Verifiers.** All three rated it low: only very large accounts hit it, callers can lower `commits_per_repo` or `top_repos`, and the failure is loud.

### 15. The roadmap lists server mode as out of scope (low)

**Scenario.** docs/project-roadmap.md:44 lists "No real-time API / server mode" as out of scope by design, while `-serve` ships a web UI documented in the README and deployment guide.

**Evidence.** The roadmap row versus main.go:36 and main.go:56-69 and internal/web/server.go.

**Fix.** Reword the row to the real limit: no hosted service and no card-by-URL or embeddable image API (the web UI inlines cards as data URIs, as docs/system-architecture.md:153 says). Deleting the row would lose that limit.

**Verifiers.** All three rated it low: a stale planning row, while the user-facing docs are correct.

### 16. The PDR non-goal contradicts the web UI (low)

**Scenario.** docs/project-overview-pdr.md:16 says ghglance is not a per-request server, and functional requirements F1-F8 never mention the web UI or its OAuth settings.

**Evidence.** The PDR line versus main.go:56-82, which runs `web.Run`.

**Fix.** Narrow the non-goal to "no hosting of cards at stable URLs and no embeddable image API", and add a requirement for the optional `-serve` web UI (OAuth App required, visitor tokens only). Also update the PDR "Users" line ("without a self-hosted service") and the "Runs as" column of the value-proposition table.

**Verifiers.** All three rated it low as planning-doc drift; the third listed the extra PDR lines to change.

### 17. The codebase layout omits internal/web and newer files (low)

**Scenario.** The codebase map shows only `github/`, `card/` and `theme/` under `internal/`, so the web UI package and its deployment files are invisible to newcomers, and the module table omits `github.Collect`.

**Evidence.** docs/codebase-summary.md:12-41 versus internal/web/, internal/github/collect.go, internal/card/records.go, compose.yml and .env.example on disk.

**Fix.** Add internal/web (listing `templates/` and `static/` as directories rather than file by file), collect.go, records.go, the newer test files, compose.yml and .env.example to the tree. Add an `internal/web` subsection under "Module responsibilities" that links to system-architecture.md#web-ui-mode.

**Verifiers.** All three rated it low; the third suggested listing directories rather than every template file.

### 18. FetchProductive and attributeCommit have no tests (low)

**Scenario.** Both functions are at 0% coverage but feed four cards. Untested behaviours include time-zone bucketing (23:30Z in Asia/Saigon lands in hour 6 of the next day), the last-year versus all-time split, dropping a commit from a repo with no languages, skipping a share that rounds to 0, and pagination stopping at the cap and following `EndCursor`.

**Evidence.** The bucketing is at productive.go:106-115 and `attributeCommit` at :136-155. The file has 156 lines, so the originally reported line 333 was wrong.

**Fix.** `attributeCommit` is pure, so add a table test now covering a byte split, the primary-language fallback, an empty repo and first-seen color. Test `FetchProductive` through a fake `RoundTripper` on `c.http` (no endpoint change needed) over two pages, checking the buckets, the split, the cursor and the stop at `maxPerRepo`. Build commit dates relative to `time.Now()`, because `yearAgo` is computed inside the function.

**Verifiers.** All three rated it low as a coverage gap with no current defect, and corrected the line numbers.

### 19. FetchProfile is untested (low)

**Scenario.** Only the helpers are tested. Untested are: profile fields read only on page 0, `TotalContributionsLastYear` adding the restricted count, the 10-page cap, the "user not found" error (whose text internal/web/jobs.go:367 matches), and the fork, private and org-admin filters feeding the totals.

**Evidence.** profile.go:129-131, :134, :154 and :172-199.

**Fix.** Add a fake-transport test (set `c.http`; no endpoint change) with two pages: a fork, a private repo, org repos with ADMIN and WRITE, restricted contributions above 0, and `hasNextPage` true then false. Assert the totals including `TotalForks`, `RepoCount`, language order, that the `after` cursor is sent on page 2, that page-2 user fields do not overwrite page-0 fields, and the error text for `user: null`.

**Verifiers.** One kept medium; two rated it low because no defect exists today and the ownership and sorting helpers are already tested.

### 20. Warnings bypass Warnf and Strict (low)

**Scenario.** When a contribution window returns a nil user, `fetchContributionWindow` prints to stderr and returns `(false, nil)`, so Strict never sees the gap and the web job can publish a partial all-time result. The month-ceiling notice (line 175) and the rate-limit sleep notice (client.go:99) also go to stderr, without the `job <key>:` prefix the web server adds through `Warnf`.

**Evidence.** contributions_all_time.go:144-148 and :175 call `fmt.Fprintf(os.Stderr, ...)`.

**Fix.** Route these messages through a warn callback on `Client` or the fetch options. For nil-user windows, finish the loop and return the skipped windows together at the end as one partial-data error (for example with `errors.Join`); `Collect` already turns that into an error under Strict and a warning otherwise, so Strict need not be threaded into `Client`. Keep the month-ceiling notice a warning, never a Strict error: it is a hard API limit, and making it fatal would fail heavy contributors on every run.

**Verifiers.** Two confirmed it at low; one refuted the data-loss part because a nil user after a successful profile fetch is nearly unreachable. All three agreed the ceiling case must stay a warning.

### 22. Heatmap thresholds paint light contributors at full intensity (low)

**Scenario.** Thresholds are `{1, max/4, max/2, 3*max/4}`. Bucket 1 is reachable only when max is 8 or more. With max 2 the thresholds are `{1,0,1,1}`, so 1- and 2-contribution days both get full Accent; with max 3, a 1-contribution day lands in bucket 3. `thresholds[0]` is never read, and no test covers `bucketFor` or `intensityThresholds`.

**Evidence.** contributions_heatmap.go:162-195 (the reported line 426 does not exist).

**Fix.** Drop the thresholds array and compute `bucket = (4*count + max - 1) / max` for `count > 0`. That maps the busiest day to 4 and keeps lower counts lower (1 of 2 gives 2; 1 of 100 gives 1). The originally suggested `1 + min(3, (count-1)*4/max)` stops the busiest day from reaching full Accent when max is below 4. Add a table test for max 0, 1, 2, 3, 4, 8 and 100. Mention the recolouring in the release notes.

**Verifiers.** All three confirmed it at low; the third corrected the formula.

### 23. The last two x-axis labels overlap (low)

**Scenario.** `stride = (n-1)/5`, so for 11 to 20 months the stride is 2 or 3 and `stride/2` is 1. The guard `n-1-i < stride/2` then excludes only the pinned last index, so the label before it is kept. For 12, 14, 17 and 20 months, labels end up 26 to 15 px apart while each "mm/yy" label is about 30 px wide. The usual 13-month last-year card is unaffected. contributions_by_year.go:93 always pins the last year label with no closeness guard, so 16 or 17 years of history overlap too.

**Evidence.** `xAxisLabelVisible` in contributions.go:166-182; contributions_by_year.go:93 `if i%labelStride == 0 || i == len(buckets)-1`.

**Fix.** Skip index `i` when `(n-1-i)*chartW/(n-1)` is below a minimum label pitch (about 34 px for "mm/yy"). In contributions_by_year.go, add a similar guard with a pitch of about 26-28 px (four-digit years), measured in that file's bar pitch. The fix belongs in `xAxisLabelVisible`, not `trimToLastYear`.

**Verifiers.** All three confirmed it at low and corrected the contributions_by_year.go line and pitch.

### 24. A refused job exposes the submitter's login (low)

**Scenario.** Alice submits a private-capable token for `bob`. The refusal "your token belongs to alice ..." is stored in the job and, for an hour, shown on the public `/u/bob` page and returned by `/u/bob/status`.

**Evidence.** jobs.go:313 and :315 put `info.Login` in the error; jobs.go:267 stores it; `Queue.Status` returns it with no ownership check; user.html:25 renders it.

**Fix.** Use a neutral message, for example "the token can read private repositories, so it can only generate cards for its own account", matching the existing `errFresh` messages that say "another account". Update the assertions in internal/web/pasted_token_test.go:107 and internal/web/web_test.go:703. Skip the one-time-notice idea: the error arrives asynchronously and would need new plumbing.

**Verifiers.** All three confirmed it at low: only a public login leaks, for one hour.

### 26. One submitter can fill the job queue (low)

**Scenario.** Jobs are deduplicated by target login only. With one throwaway token and 13 /64s to get past the per-key burst, an attacker queues 64 jobs for large accounts, and real visitors get 503 until the queue drains.

**Evidence.** `Queue.Submit` (jobs.go:145-153) checks only `userKey(sub.Login)` and `queueCapacity`; `TokenInfo` runs only inside `run()`.

**Fix.** Call `TokenInfo` at submit time and allow at most one or two queued or running jobs per token owner, sharing the key between pasted and sign-in tokens and keeping the per-target check. A per-client-key cap adds nothing over the existing limiter. Update the README "Limits" line and the Jobs bullet in docs/system-architecture.md afterwards. The attacker's own GitHub rate limit usually drains the queue within tens of minutes, so "many hours" is an upper bound.

**Verifiers.** All three confirmed it at low; the third rejected the per-client-key option.

### 27. Shutdown can be killed before token revocation (low)

**Scenario.** compose.yml sets no `stop_grace_period`, so Docker kills the process 10 seconds after SIGTERM. `Run` waits up to 15 seconds in `srv.Shutdown`, and only then does `queue.Stop` revoke dropped sign-in tokens, each call allowed another 15 seconds. With a request in flight or a slow GitHub, revocation never runs.

**Evidence.** server.go:162-164; jobs.go `Stop`; oauth.go `oauthTimeout = 15s`.

**Fix.** Add `stop_grace_period: 45s` to compose.yml, which covers the HTTP drain plus the revoke and worker windows. A single deadline under 10 seconds does not work unless it also shortens each revoke call below the 15-second timeout.

**Verifiers.** Two confirmed it at low; one refuted the harm because the tokens exist only in memory and die with the process.

### 28. The prebuilt-image switch uses an unpublished tag (low)

**Scenario.** A maintainer follows the guide and sets `image: docker://ghcr.io/tiennm99/ghglance:v1`. metadata-action's semver patterns strip the `v`, so the published tags are `1.x.y`, `1.x`, `1` and `latest`, and the pull fails. Nothing breaks today because action.yml still uses the Dockerfile.

**Evidence.** docs/deployment-guide.md:146; release.yml:41-44.

**Fix.** Change the doc to `:1`, and note that pinning the exact version is safer because older `@v1.x.y` Action pins would otherwise pull the newest image.

**Verifiers.** All three confirmed it at low.

### 29. A prerelease tag would move `v1` and `latest` (low)

**Scenario.** Pushing `v1.9.0-rc.1` matches `v*`; `${TAG%%.*}` gives `v1`, which is force-moved to the release candidate. The raw `latest` image tag and a non-prerelease GitHub release are published too. No prerelease tag has ever been pushed.

**Evidence.** release.yml:6 trigger `v*`; :113-117 force-push; :44 unconditional `type=raw,value=latest`; action-gh-release gets no `prerelease` input.

**Fix.** Narrow the trigger to `v[0-9]+.[0-9]+.[0-9]+`, which matches the documented release flow. If prereleases are wanted later, gate `update-major-tag` and the raw `latest` tag on `!contains(github.ref_name, '-')` and pass `prerelease` to action-gh-release; the semver major and minor patterns already skip prereleases.

**Verifiers.** Two confirmed it at low; one refuted it as theoretical but called the guard cheap hardening.

### 30. The PDR says 15 cards (low)

**Scenario.** docs/project-overview-pdr.md:5 and F1 at :37 promise 15 cards; `allCards` lists 16 and the README already says 16.

**Evidence.** internal/card/card.go:24-39; demo/dracula holds 16 SVGs.

**Fix.** Point F1 at "every card registered in internal/card/card.go" so the count cannot drift, and update line 5.

**Verifiers.** All three confirmed it at low.

### 31. codebase-summary says main.go sequences the fetchers (low)

**Scenario.** docs/codebase-summary.md:66 says the call order lives in main.go, but main.go:129 only calls `github.Collect`, which both the CLI and web jobs use. Lines 50 and 107 say 15 SVGs.

**Evidence.** main.go:129; docs/system-architecture.md:23 already describes `Collect` correctly; `allCards` has 16 entries.

**Fix.** Reword line 66 to "Call order in `github.Collect` (used by the CLI and every web job): Profile, then AllTime, then Productive", and change lines 50 and 107 to 16. Line 112 says `RenderAll` produces 15 valid SVGs, which is true of `TestRenderAll` today because its list omits `records.svg`. Add `records.svg` to the `want` list in internal/card/card_test.go first, then change line 112 to 16.

**Verifiers.** All three confirmed it at low; the third caught that line 112 needs the test change.

### 32. Test coverage cites a missing main_test.go (low)

**Scenario.** docs/codebase-summary.md:115 says `TestUTCOffsetLabel` and `TestParseWeekday` live in main_test.go, which does not exist. The section also omits 7 of the 11 test files.

**Evidence.** The tests are in internal/github/collect_test.go:11 and :40.

**Fix.** Point the bullet at internal/github/collect_test.go and name `TestScopesIncludeRepo` too. Add bullets for internal/card/{contributions_trim,records}_test.go, internal/github/{commit_cap,contribution_scope}_test.go and internal/web/{web,oauth,pasted_token}_test.go. Re-check the "No network-touching tests" line once the httptest-based web tests are listed.

**Verifiers.** All three confirmed it at low.

### 33. code-standards omits internal/web (low)

**Scenario.** docs/code-standards.md:23 names only github, card and theme, and the testing section (lines 68-69) says network tests are omitted and nothing mocks `http.Client`. internal/web exists and its tests use httptest, including a fake GitHub OAuth server.

**Evidence.** main.go:18 imports internal/web; internal/web/oauth_test.go:79 uses `httptest.NewServer`.

**Fix.** Add "web = optional HTTP UI (sign-in, job queue, card store)" to the list. Rewrite lines 68-69 so the no-network rule applies only to the github fetchers and say that internal/web is tested with httptest. Line 34 ("no goroutines") applies only to renderers and needs no change.

**Verifiers.** All three confirmed it at low.

### 34. OAuth error paths are untested (low)

**Scenario.** No test sends a wrong code, a non-200 exchange or a non-JSON body, or an empty `code`, and `handleAuthStart`'s active-job redirect and `errTooManyLogins` 503 never run. A regression could lose the form or mishandle these paths without a failing test. The code is short and fails closed today.

**Evidence.** Coverage shows 0 hits on oauth.go:226-233, the callback's 400 and 502 renders, and oauth.go:364-366 and :374-379.

**Fix.** Give the fake GitHub a configurable failure mode. Add callback tests with a valid state and a wrong code or a 500 or non-JSON response, asserting a 502 with the options kept, no queued job, and the pending login consumed. Add `handleAuthStart` tests for an active job (303 to `/u/<user>?notice=pending`, no cookie) and for a full pending map, filled through `s.logins.add` (503 with the form kept, no cookie).

**Verifiers.** All three confirmed it at low.

### 35. Y-axis code is duplicated across four cards (low)

**Scenario.** The max scan, zero guard, `niceTicks(yMax, 5)`, the snap to the last tick and the tick loop are repeated in productive.go, productive_weekday.go, contributions_by_year.go and contributions.go. A change to tick count, label offset or font size needs four edits.

**Evidence.** productive.go:57-82, productive_weekday.go:62-91, contributions_by_year.go:44-70 and contributions.go:81-122.

**Fix.** Add `yScale(maxVal int) (yMax float64, ticks []float64)` and a left-axis writer to axis.go. contributions.go has different geometry and writes left and right ticks in one loop, so it needs a mirrored variant parameterised by x and text-anchor. Only the width, height, `leftAxis`, `rightPad`, `topPad` and `chartH` constants are shared; `barGap` differs per card, and the 0.55 dim ratio is used only in two files. Do this together with finding 9.

**Verifiers.** All three confirmed it at low as maintainability only.

### 36. Font-size constants are mostly unused (low)

**Scenario.** svg.go says every card should use the shared sizes, but `fontLabel` and `fontBigNum` are never used, and about 20 literals (10, 11, 12 and 28) are hard-coded across the cards.

**Evidence.** svg.go:126-135; literals in contributions.go, productive.go, productive_weekday.go, contributions_by_year.go, donut_chart.go, streak.go, stats.go, top_starred_repos.go and profile.go.

**Fix.** Replace each literal with `fontAxis`, `fontLabel`, `fontBody` or `fontBigNum` through `%d`. Every literal already equals its constant, so the SVG output is unchanged. Do not delete the unused constants: their values are in use as literals.

**Verifiers.** All three confirmed it at low; the third rejected the delete option.

### 37. Timezone auto-detect overwrites an explicit UTC (low)

**Scenario.** A visitor outside UTC types UTC on purpose, and the submit fails. Every error re-render passes the submitted form with no `Meta`, so `data-autotz` is emitted again and app.js replaces "UTC" with the browser's zone. The next submit shifts the productive-hours cards, although the page said the options were kept.

**Evidence.** form.html:17 `{{if not .Meta}}data-autotz{{end}}`; app.js:13; error renders in server.go and oauth.go:369-442.

**Fix.** Add a `Submitted bool` to `formValues`, set it in `parseSubmission`, and render `{{if and (not .Meta) (not .Form.Submitted)}}data-autotz{{end}}`. Gating on `.Error` would also stop auto-detect on the default form rendered after a parse failure.

**Verifiers.** All three confirmed it at low.

### 38. Status polling hides failures (low)

**Scenario.** If the server restarts or the network drops while cards generate, every poll fails and the catch handler only grows the delay. The page keeps "Running: <stage>" with a spinning indicator and a frozen clock. After a restart the job is gone, and the page reloads to "No cards yet" with no hint that a run was lost.

**Evidence.** app.js:109-112 updates no DOM; server.go:130-131 abandons jobs on shutdown.

**Fix.** After one or two failures, write "Lost contact with the server, retrying…" to `#progress-text` once (it is a live region, so do not rewrite it on every failure). The next successful poll overwrites it. The "run was lost" case needs server-side state and is out of scope. Stopping after N failures is unnecessary because backoff is capped at 30 seconds.

**Verifiers.** Two confirmed it at low; one refuted it as negligible because polling recovers on its own.

### 39. Progress copy promises an auto-refresh without JavaScript (low)

**Scenario.** With JavaScript off, the progress box still says "This page refreshes when the cards are ready", but only app.js reloads the page.

**Evidence.** user.html:21; app.js:1-3 says every page works without JavaScript.

**Fix.** Default to "Reload this page to check progress" and show the auto-refresh line only with JavaScript, using `<noscript>` or the existing `html.js` class. Do not add an unconditional `<meta http-equiv="refresh">`: it would race the polling reload and keep `?notice=pending` in the URL. If a meta refresh is wanted, put it inside `<noscript>`.

**Verifiers.** All three confirmed it at low; the third rejected the unconditional meta refresh.

### 41. `include_private=false` does not keep private work off the cards (low)

**Scenario.** An Action user with a `repo` token sets `include_private: "false"`. Only the repo lists are filtered; total commits, PRs, issues, reviews, restricted contributions and the calendar-derived cards still include private activity. README.md:397-400 promises otherwise.

**Evidence.** profile.go:147-167 assigns totals before any privacy check; contributions_all_time.go:154-166 folds totals before the `IsPrivate` filter, which only touches `SeedRepos`.

**Fix.** Correct the README sentence. Say that the option filters repo-derived cards (repo lists, stars, languages and top repos) and that contribution totals reflect what the token can read. Tell users that a token without `repo` removes per-repo private detail, and that turning off "Private contributions" in their GitHub profile settings is also needed to hide anonymised private counts. Add a short clause to the action.yml and CLI flag descriptions; they are already accurate for repo lists. Do not filter the totals: GitHub's counters cannot be split by repo privacy, and changing them would move numbers for `@v1` callers.

**Verifiers.** Two rated it low because only aggregate counts are affected; one rated it medium and added the profile-setting caveat.

### 42. "Total PR Reviews" counts only the last year (low)

**Scenario.** `profileQuery` requests `contributionsCollection` with no dates, which GitHub limits to the last year, but the stats card labels the review count "Total PR Reviews" next to the all-time PR and issue totals. A user with 2,000 reviews over 8 years and 50 in the last year sees 50.

**Evidence.** queries.go:29; profile.go:153; stats.go:37-39.

**Fix.** Relabel the row "PR Reviews (last year)", matching the commit rows, and update README:23, which calls these values totals. Summing reviews across the all-time windows would move numbers on existing cards, so per CLAUDE.md it must be a separate default-off addition wired through all five knob surfaces, not a silent change.

**Verifiers.** All three confirmed it at low; the third ruled out an unconditional all-time sum.

### 43. Top Starred Repos prints private repo names (low)

**Scenario.** With the default `include_private=true` and a `repo` token, a private repo with stars stays in `TopRepos`, and `ownedNonForkRepos` filters only forks and zero-star repos, so the private name is drawn on a card meant for a public README. The empty state says "No public repos with stars."

**Evidence.** top_starred_repos.go:89-101 ignores `RepoInfo.IsPrivate`; top_starred_repos.go:43 holds the empty-state text.

**Fix.** The verifiers split on the intended behaviour. Two support skipping `r.IsPrivate` in `ownedNonForkRepos`. The third argues that showing private repos is the documented effect of `include_private`, and that skipping them would override the user's choice for one card and change `@v1` output. The safe minimum is to change the empty-state text to "No starred repos yet." and note in the README that this card lists private repo names when `include_private` is on. Whether to also hide private names is a product decision for the maintainer. Add a test with a starred private repo either way.

**Verifiers.** Two confirmed it at low; one refuted the leak framing and called only the empty-state text a defect.

## Refuted

- **6. Productive-time buckets use `committedDate` (productive.go:107).** This is a documented, defensible choice: switching to `authoredDate` does not fix squash merges, adds cherry-pick skew, and would move numbers for `@v1` callers, which CLAUDE.md requires to be default-off.
- **25. The web container runs as root (Dockerfile:13).** This is hardening with no exploitable path. The compose-only `user:` fix would break the existing root-owned `/data` volume; a future change should use a separate non-root `web` build stage and a one-time `chown` instead.

## Suggested fix order

The groups below own disjoint files, so they can be implemented in parallel. Within each group, fix the medium findings first.

1. **GitHub fetch** (internal/github/*.go and its tests): findings 1, 5, 4, 3, then 20 and 7, then the test additions 2, 18 and 19. Write the fake `RoundTripper` helper once and reuse it for findings 2, 3, 4, 7, 18 and 19. Finding 7's wording updates in README.md, action.yml and docs/system-architecture.md belong to the files owned by groups 4 and 6. Those groups apply them after this group lands.
2. **Card rendering** (internal/card except stats.go, plus internal/theme tests): findings 8 and 9, then 22, 23, 35, 36 and 43. Do findings 9 and 35 together, because both change the axis code. This group also adds `records.svg` to `TestRenderAll` in card_test.go, which the docs group's finding 31 depends on.
3. **Web server limits and shutdown** (internal/web/oauth.go, oauth_test.go, ratelimit.go, server.go, compose.yml): findings 11 and 10, then 27 and 34.
4. **Action, release and README** (entrypoint.sh, action.yml, main.go, README.md, .github/workflows/release.yml, internal/card/stats.go): findings 12 and 41, then 13, 29 and 42. This group also owns the README note for finding 43 and the README and action.yml wording for finding 7. It applies them once groups 1 and 2 settle those changes.
5. **Web jobs and UI** (internal/web/jobs.go, validate.go, web_test.go, pasted_token_test.go, templates/form.html, templates/user.html, static/app.js): findings 40 and 21, then 24, 26, 37, 38 and 39. The README "Limits" note for finding 26 goes to group 4.
6. **Docs** (docs/deployment-guide.md, docs/project-roadmap.md, docs/project-overview-pdr.md, docs/codebase-summary.md, docs/code-standards.md, docs/system-architecture.md): findings 14, then 28, 15, 16, 30, 17, 31, 32 and 33. Change line 112 of codebase-summary.md (finding 31) only after group 2 lands the card_test.go change. This group also applies the docs/system-architecture.md follow-ups for findings 7 and 26.
