# ghglance — Product Development Requirements

## One-liner

Single-binary Go CLI + GitHub Action that renders 16 themed SVG cards summarising a GitHub user's public (and optionally private) profile, for embedding in a profile README.

## Users

- **Primary**: GitHub users maintaining a profile README who want auto-updating stat cards without running a server: the Action and CLI need nothing hosted. The optional `-serve` web UI is a self-hosted way to preview cards.
- **Secondary**: Tools integrating profile summaries (dashboards, portfolio sites).

## Non-goals

- WakaTime-style editor telemetry.
- Cloning repos or running linguist locally (lowlighter/metrics territory). A future `-accurate-languages` mode may add per-commit REST classification; clone-mode is out of scope for v1.
- Hosting cards at stable URLs or an embeddable image API. The `-serve` web UI renders on request for viewing, but inlines cards in its pages and serves none by URL.

## Value proposition vs alternatives

| Tool | Language | Runs as | Solves per-commit attribution? |
| --- | --- | --- | --- |
| anuraghazra/github-readme-stats | JS | hosted service | No (byte-size only) |
| vn7n24fzkq/github-profile-summary-cards | TS | Action + hosted | No (primary-language-per-repo) |
| lowlighter/metrics (indepth) | JS | Action | Yes (clones + linguist-js) |
| **ghglance** | Go | Action + CLI (+ optional self-hosted web UI) | Partial (byte-weighted today; REST-per-commit planned) |

Distinguishing traits:
- **Single binary**: no Node, no Ruby, no Docker needed for CLI usage.
- **Seed-list sampling**: commit-history probes land on repos the user actually committed in (via `contributionsCollection.commitContributionsByRepository`), not top-starred or owned-only.
- **All-time variants**: for every time-bounded card (most-commit-language, productive-time, contributions), there's a lifetime counterpart.
- **Accurate defaults**: forks and private repos are **on** by default so cards reflect real activity; `-include-private` silently no-ops if the token lacks `repo` scope, so the default is safe for public runs too.

## Functional requirements

| # | Requirement |
| --- | --- |
| F1 | Render every card registered in `allCards` (`internal/card/card.go`) per selected theme (see `docs/system-architecture.md`) |
| F2 | Support 65 themes ported from github-profile-summary-cards |
| F3 | Handle the full username→profile→cards flow in a single invocation |
| F4 | Package as GitHub Action with `commit_changes` auto-commit of output |
| F5 | Expose `-include-forks` / `-include-private` toggles |
| F6 | Apply byte-weighted commit-to-language attribution |
| F7 | Render smooth area charts for time-series (Catmull-Rom) |
| F8 | Support IANA timezones for productive-time (display `UTC±N.NN`) |
| F9 | Offer an optional `-serve` web UI to generate and view cards in the browser. It requires a GitHub OAuth App (`-oauth-client-id`, `-oauth-client-secret`, `-public-url`) and runs every job on the visitor's own token, from sign-in or pasted; the server holds no token of its own (see `docs/system-architecture.md#web-ui-mode`) |

## Non-functional requirements

| Axis | Target |
| --- | --- |
| Runtime (scheduled Action) | < 60 s for typical user |
| GraphQL calls per run | < 100 |
| REST calls per run | 0 (may grow with future modes) |
| Dependencies | stdlib only (no Go module deps required) |
| Binary size | < 15 MB stripped |
| SVG output correctness | XML-escaped, no script injection from user data |

## Success metrics

- Cards render identically across `dracula`, `github`, `github_dark`, `nord_bright`, `tokyonight`.
- Test suite covers rendering, XML escaping, number formatting, language sort.
- `go vet ./...` and `go test ./...` clean on every commit.

## Open questions

Planned follow-on work lives in [`project-roadmap.md`](./project-roadmap.md).
