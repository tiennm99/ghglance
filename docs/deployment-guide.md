# Deployment Guide

Three consumption paths: **GitHub Action**, **prebuilt binaries**, **go install**.
The same binary also runs as a self-hosted **web UI** (section 4).

## 1. GitHub Action (recommended for README auto-updates)

### Workflow template

File: `.github/workflows/ghglance.yml` in your profile repo.

```yaml
name: ghglance

on:
  schedule:
    - cron: "0 0 * * *"        # daily at 00:00 UTC
  workflow_dispatch:

permissions:
  contents: write              # needed for commit_changes

jobs:
  cards:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: tiennm99/ghglance@v1
        with:
          user: ${{ github.repository_owner }}
          token: ${{ secrets.GHGLANCE_TOKEN }}
          themes: dracula,github_dark,tokyonight
          tz: Asia/Saigon
          # start_of_week: monday   # optional; default sunday — rotates heatmap rows + weekday bars
          include_forks: "false"
          include_private: "false"
          commit_changes: "true"
```

### Required secrets

`GHGLANCE_TOKEN`: a **classic** personal access token with at minimum:

| Scope | Needed for |
| --- | --- |
| `read:user` | Basic profile fields, contribution calendar |
| `repo` | Only if `include_private: "true"` |

Fine-grained PATs and the default `${{ github.token }}` lack the introspection scope for contribution calendars in many orgs, so a classic PAT is recommended.

Create one at <https://github.com/settings/tokens> → "Generate new token (classic)" → select `read:user` (+ `repo` if needed) → save as repo secret `GHGLANCE_TOKEN`.

### Embedding in README

```md
![profile](./output/dracula/profile-details.svg)
![repos-per-language](./output/dracula/repos-per-language.svg)
![most-commit-language](./output/dracula/most-commit-language.svg)
![stats](./output/dracula/stats.svg)
![productive-time](./output/dracula/productive-time.svg)
![productive-weekday](./output/dracula/productive-weekday.svg)
![contributions](./output/dracula/contributions.svg)
![contributions-heatmap](./output/dracula/contributions-heatmap.svg)
![top-starred-repos](./output/dracula/top-starred-repos.svg)
![streak](./output/dracula/streak.svg)
![most-commit-language-all-time](./output/dracula/most-commit-language-all-time.svg)
![productive-time-all-time](./output/dracula/productive-time-all-time.svg)
![productive-weekday-all-time](./output/dracula/productive-weekday-all-time.svg)
![contributions-all-time](./output/dracula/contributions-all-time.svg)
![contributions-by-year](./output/dracula/contributions-by-year.svg)
```

The Action commits SVGs to `output/<theme>/` on the default branch. GitHub serves them from the raw URL the README references.

## 2. Prebuilt binaries

Each tag under `v*` publishes:
- Linux `amd64`, `arm64`
- macOS `amd64`, `arm64`
- Windows `amd64`

Released via `.github/workflows/release.yml` which matrixes `GOOS` × `GOARCH`, strips symbols (`-ldflags="-s -w"`), and uploads tar.gz / zip to the GitHub Release.

Install:

```sh
# Linux x86_64 example
curl -L https://github.com/tiennm99/ghglance/releases/latest/download/ghglance_linux_amd64.tar.gz \
  | tar xz
./ghglance -user YOUR_USERNAME
```

## 3. go install

```sh
go install github.com/tiennm99/ghglance@latest
```

Requires Go 1.26+. Puts the binary in `$(go env GOPATH)/bin`.

## 4. Web UI (Docker Compose / Coolify)

`compose.yml` at the repo root builds the `Dockerfile`, overrides its
entrypoint to run `ghglance -serve :8080 -data-dir /data`, stores cards in
the `ghglance-data` volume, and health-checks `/healthz` with busybox
`wget`. It publishes no host port; Coolify routes the domain generated for
`SERVICE_FQDN_GHGLANCE_8080` to container port 8080.

| Variable | Needed for |
| --- | --- |
| `GHGLANCE_OAUTH_CLIENT_ID` | Required. Client ID of the GitHub OAuth App behind "Sign in with GitHub" (`-oauth-client-id`). |
| `GHGLANCE_OAUTH_CLIENT_SECRET` | Required. That OAuth App's client secret (`-oauth-client-secret`). Never logged or printed. |
| `GHGLANCE_PUBLIC_URL` | Required. The site's external origin, e.g. `https://ghglance.example.com` (`-public-url`). The OAuth App's callback URL must be exactly `<public-url>/auth/callback`. |

The server holds no GitHub token: every generation runs on the visitor's
token, either from Sign in with GitHub (revoked when the job ends) or one
they paste into the form (used once, never stored, not revoked). The
OAuth App is still required. No token variable exists. The site is for
quick viewing only: cards are inlined into `/u/<user>` and have no URL of
their own.
`compose.yml` refuses to start while any of the three variables is empty,
and `-serve` exits with an error naming the missing settings.

Coolify: register an OAuth App (GitHub **Settings > Developer settings >
OAuth Apps > New OAuth App**; homepage URL = the domain, authorization
callback URL = `<domain>/auth/callback`) and generate a client secret.
Then create a Docker Compose resource from this repo, compose file
`/compose.yml`, set the three variables, assign the domain, and deploy;
the startup log prints the callback URL it uses. Server flags
(`-cooldown`, `-retention`, `-workers`, `-timeout`) are changed by editing `command:` in
`compose.yml`. Steps for a plain Docker host and the request-handling rules
are in the README's "Run the web UI" section.

Rollback: redeploy the previous commit. Card sets on the volume are
format-stable, so no data migration is involved.

## Docker image

Published to `ghcr.io/tiennm99/ghglance:<tag>` on each `v*` release via `.github/workflows/release.yml` (buildx, multi-tag: exact version, major.minor, major, latest).

The Action itself uses a runner-built image by default (`image: Dockerfile` in `action.yml`). To switch to the pre-built image for faster cold starts, edit `action.yml`:

```yaml
runs:
  using: docker
  image: docker://ghcr.io/tiennm99/ghglance:1
```

Image tags drop the `v` (`1.2.0`, `1.2`, `1`, `latest`), so `:v1` does not
exist. Prefer the exact version (`:1.2.0`) over `:1`: with `:1`, callers
pinned to an older `@v1.x.y` Action tag would still pull the newest image.

## Release process

1. Tag: `git tag -a v1.2.0 -m "..." && git push origin v1.2.0`.
2. `release.yml` runs `go vet` + `go test` as a gate before the docker and
   binaries jobs. If tests fail, no artifacts ship.
3. On green, GHCR push + cross-platform binary artifacts happen automatically.
4. The `update-major-tag` job force-moves the floating major tag (e.g. `v1`)
   to this release's commit after test + docker + binaries all pass.
   Consumers pinned to `tiennm99/ghglance@v1` pick up the release on their
   next Action run without a workflow edit.
5. Docker base images and third-party actions are SHA-pinned (with version
   comments) so mutable-tag changes upstream can't rewrite a released image.
6. **Marketplace:** the action is listed on the GitHub Marketplace as
   [`ghglance`](https://github.com/marketplace/actions/ghglance); the listing
   name comes from `name:` in `action.yml`. Publishing is a web-UI step —
   edit the GitHub release and tick "Publish this Action to the GitHub
   Marketplace" — since `action-gh-release` cannot publish it.

## Rollback

By the time a release is found broken, `update-major-tag` has already
force-moved `v1` to it, and deleting the patch tag does not move `v1` back.
Every caller on `@v1` keeps running the broken release until `v1` is
re-pointed.

1. Re-point the floating tag at the last good release:
   `git tag -f v1 v1.1.0 && git push -f origin v1`. This fully restores
   Action callers, because `action.yml` builds from the Dockerfile at the
   tagged commit.
2. Delete the bad tag and its release: `git push --delete origin v1.2.0`,
   then delete the GitHub release.
3. Fix GHCR. The bad release also moved `1`, `1.<minor>` and `latest`.
   Re-point each to the last good image, for example
   `docker buildx imagetools create -t ghcr.io/tiennm99/ghglance:1 ghcr.io/tiennm99/ghglance:1.1.0`
   (repeat for `1.<minor>` and `latest`), then delete the bad `1.2.0` image
   version. If the bad release was the first in its minor line, delete its
   `1.<minor>` tag instead of re-pointing it.

## Rate limit considerations

| Scenario | GraphQL calls per run | Notes |
| --- | --- | --- |
| Typical user, defaults | 15–40 | Well under 5000 pts/hr |
| Active user (8 years, 30+ seed repos) | 40–80 | Still comfortable |
| `-include-private=true` with 100+ work repos | 80–200 | Fine for daily cron |
| Adversarial user with 500+ committed repos/year | Capped by `maxRepositories: 100` per year query | Long tail drops silently |

No REST calls today. Future `-accurate-languages` mode will push toward 1000+ REST per run; schedule that mode less frequently (weekly, not daily).

The client auto-handles rate-limit responses: on 429 or 403 with `X-RateLimit-Remaining: 0`, it sleeps up to 5 minutes (honoring `Retry-After` / `X-RateLimit-Reset`) and retries once. A reset window longer than 5 min surfaces as an error so CI can reschedule instead of burning runner time. Use the `-timeout` flag (default 30m) to cap total fetch duration; `SIGINT`/`SIGTERM` cancels in-flight requests cleanly.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| "error: fetch profile: graphql: Could not resolve to a User" | Username typo |
| "http 401" | Token expired or lacks `read:user` |
| "rate limit resets in 42m (>5m0s max wait)" | Client refused to sleep through a long window; reschedule the Action |
| "http 403" on non-rate-limit path | PAT scope too narrow |
| Blank contribution chart | User has 0 contributions in their window; expected |
| Private repo data missing | `-include-private=true` not set, or PAT lacks `repo` |
| Nothing committed by the Action | Check `permissions: contents: write` in the workflow |
