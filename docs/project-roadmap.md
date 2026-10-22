# Roadmap

What's planned next and what's intentionally out of scope. Completed work lives in the git log and GitHub Releases; this file doesn't rehash it.

## Planned

### Per-commit file classification (`-accurate-languages`)

Fix the Markdown-blog misattribution case (and any repo where linguist's byte view disagrees with the files the user actually edited).

- **Approach**: `GET /repos/{owner}/{repo}/commits/{sha}` per commit → classify each file with `go-enry`. Weight by `additions + deletions`.
- **Cost**: ~1 REST call per commit. At current defaults (30 seed repos × 500 commits = 15 000 commits worst case) this is heavy; opt-in flag, schedule weekly not daily.
- **Research**: `plans/reports/researcher-260418-2001-accurate-language-stats.md`.
- **Status**: designed, not implemented.

### Partial bare clone for lifetime language stats (`-deep`)

Lifetime language stats across every repo a user has committed in, without the 500-commits-per-repo cap.

- **Approach**: `git clone --filter=blob:none --bare` per seed repo + `git log --author --numstat` → go-enry.
- **Cost**: ~5 % of full-clone disk (trees only); 3–5 min runtime for 100 repos; zero REST calls.
- **Trade-off**: needs disk + git binary on runner.
- **Status**: researched only; would land behind `-deep`.

### User-configurable repo exclusion (`-exclude-repo`)

Drop throwaway repos (experiments, stashed forks) from stats without turning off `include_forks` globally.

- **Approach**: `-exclude-repo owner1/name1,owner2/name2` flag. Client-side filter on the seed list before probing.
- **Status**: pending user demand.

### Expanded `ownerAffiliations`

Catch work in org repos where the user is a collaborator rather than owner.

- **Approach**: expose `-affiliations OWNER,COLLABORATOR,ORGANIZATION_MEMBER`.
- **Blocker**: decide whether to display private org work on a public profile card by default.
- **Status**: blocked on that privacy call.

## Out of scope (by design)

| Limitation | Reason |
| --- | --- |
| Markdown/prose excluded from byte counts | Linguist's default — we defer to it |
| No hosted service, no card-by-URL or embeddable image API | Cards are committed by the Action or written by the CLI; the optional `-serve` web UI is for viewing only and inlines cards as `data:` URIs, so no route serves a card to link or embed |
| No WakaTime integration | Other tools already cover this (`athul/waka-readme`, `anmol098/waka-readme-stats`) |
| No 7×24 heatmap variant of productive time | 24-hour bar chart matches the reference project |
| Hard 340 px card width | Matches github-profile-summary-cards; customising would cascade through every chart's geometry |
