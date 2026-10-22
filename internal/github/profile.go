package github

import (
	"context"
	"errors"
	"sort"
	"time"
)

// profileGQL mirrors the GraphQL response for profileQuery.
type profileGQL struct {
	User *struct {
		ID        string `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Bio       string `json:"bio"`
		AvatarURL string `json:"avatarUrl"`
		Company   string `json:"company"`
		Location  string `json:"location"`
		Website   string `json:"websiteUrl"`
		CreatedAt string `json:"createdAt"`

		Followers struct{ TotalCount int } `json:"followers"`
		Following struct{ TotalCount int } `json:"following"`

		PullRequests struct{ TotalCount int } `json:"pullRequests"`
		Issues       struct{ TotalCount int } `json:"issues"`

		RepositoriesContributedTo struct{ TotalCount int } `json:"repositoriesContributedTo"`

		ContributionsCollection struct {
			ContributionYears                   []int `json:"contributionYears"`
			TotalCommitContributions            int   `json:"totalCommitContributions"`
			TotalIssueContributions             int   `json:"totalIssueContributions"`
			TotalPullRequestContributions       int   `json:"totalPullRequestContributions"`
			TotalPullRequestReviewContributions int   `json:"totalPullRequestReviewContributions"`
			TotalRepositoryContributions        int   `json:"totalRepositoryContributions"`
			RestrictedContributionsCount        int   `json:"restrictedContributionsCount"`
			ContributionCalendar                struct {
				TotalContributions int `json:"totalContributions"`
				Weeks              []struct {
					ContributionDays []struct {
						ContributionCount int    `json:"contributionCount"`
						Date              string `json:"date"`
					} `json:"contributionDays"`
				} `json:"weeks"`
			} `json:"contributionCalendar"`
		} `json:"contributionsCollection"`

		Repositories struct {
			TotalCount int `json:"totalCount"`
			PageInfo   struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []repoNode `json:"nodes"`
		} `json:"repositories"`
	} `json:"user"`
}

// FetchOptions tunes which repos contribute to the profile's aggregates.
// Zero value excludes both forks and private repos; the CLI flips both to
// true by default (private is a no-op when the token lacks repo scope, so
// it's safe to opt in). Callers using FetchOptions{} literal get the
// conservative behavior regardless of CLI defaults.
type FetchOptions struct {
	IncludeForks   bool
	IncludePrivate bool
	// IncludeOrgRepos widens the repo-owned aggregates (stars, forks, repo
	// count, repos-per-language, top-starred) beyond the user's own namespace
	// to org-owned repos they administer. Off by default: turning it on
	// changes every one of those numbers, so it stays an explicit opt-in.
	IncludeOrgRepos bool
}

// adminPermission is the viewerPermission value that marks an org repo as
// effectively the user's own. Members with WRITE/READ on a company repo they
// never created would otherwise inflate every owned-repo aggregate.
const adminPermission = "ADMIN"

// repoAffiliations maps opts onto the ownerAffiliations argument. OWNER alone
// is GitHub's "repos in your own namespace"; ORGANIZATION_MEMBER adds repos
// owned by orgs the user belongs to, which viewerPermission then narrows.
func repoAffiliations(opts FetchOptions) []string {
	if opts.IncludeOrgRepos {
		return []string{"OWNER", "ORGANIZATION_MEMBER"}
	}
	return []string{"OWNER"}
}

// ownedByUser reports whether a repo node counts as the user's own for the
// repo-derived cards: anything in their namespace, plus org repos they
// administer when opts allows it.
func ownedByUser(r repoNode, login string, opts FetchOptions) bool {
	if r.Owner == nil || r.Owner.Login == login {
		return true
	}
	return opts.IncludeOrgRepos && r.ViewerPermission == adminPermission
}

// FetchProfile collects profile, stats and repos-per-language data for the
// given user. Owned repos are paginated up to 10 pages (1000 repos) as a
// safety cap. Forks and private repos are filtered client-side per opts.
func (c *Client) FetchProfile(ctx context.Context, login string, opts FetchOptions) (*Profile, error) {
	if login == "" {
		return nil, errors.New("empty user")
	}

	p := &Profile{Login: login}
	reposPerLang := map[string]int64{}
	langColor := map[string]string{}
	repoCount := 0

	var cursor *string
	const maxPages = 10
	for page := 0; page < maxPages; page++ {
		vars := map[string]any{
			"login":        login,
			"affiliations": repoAffiliations(opts),
		}
		if cursor != nil {
			vars["after"] = *cursor
		}

		var resp profileGQL
		if err := c.query(ctx, profileQuery, vars, &resp); err != nil {
			return nil, err
		}
		if resp.User == nil {
			return nil, errors.New("user not found")
		}
		u := resp.User

		if page == 0 {
			// GitHub resolves the login case-insensitively; adopt its
			// canonical casing so owner comparisons, the card name and the
			// later contribution queries all match the account exactly.
			if u.Login != "" {
				p.Login = u.Login
			}
			p.ID = u.ID
			p.Name = u.Name
			p.Bio = u.Bio
			p.AvatarURL = u.AvatarURL
			p.Company = u.Company
			p.Location = u.Location
			p.Website = u.Website
			if t, err := time.Parse(time.RFC3339, u.CreatedAt); err == nil {
				p.CreatedAt = t
			}
			p.Followers = u.Followers.TotalCount
			p.Following = u.Following.TotalCount
			p.TotalPRs = u.PullRequests.TotalCount
			p.TotalIssues = u.Issues.TotalCount
			p.TotalContributedTo = u.RepositoriesContributedTo.TotalCount

			cc := u.ContributionsCollection
			p.TotalCommits = cc.TotalCommitContributions
			p.TotalReviews = cc.TotalPullRequestReviewContributions
			p.TotalContributionsLastYear = cc.ContributionCalendar.TotalContributions + cc.RestrictedContributionsCount
			p.ContributionYears = append([]int(nil), cc.ContributionYears...)

			// Flatten week → day into a linear daily series sorted by date.
			for _, w := range cc.ContributionCalendar.Weeks {
				for _, d := range w.ContributionDays {
					t, err := time.Parse("2006-01-02", d.Date)
					if err != nil {
						continue
					}
					p.DailyContributions = append(p.DailyContributions, DailyContribution{
						Date:  t,
						Count: d.ContributionCount,
					})
				}
			}
		}

		for _, r := range u.Repositories.Nodes {
			if !ownedByUser(r, p.Login, opts) {
				continue
			}
			if r.IsFork && !opts.IncludeForks {
				continue
			}
			if r.IsPrivate && !opts.IncludePrivate {
				continue
			}
			repoCount++
			p.TotalStars += r.StargazerCount
			p.TotalForks += r.ForkCount

			info := r.toRepoInfo(p.Login)
			if r.PrimaryLanguage != nil {
				reposPerLang[r.PrimaryLanguage.Name]++
				if _, ok := langColor[r.PrimaryLanguage.Name]; !ok {
					langColor[r.PrimaryLanguage.Name] = r.PrimaryLanguage.Color
				}
			}
			for _, e := range r.Languages.Edges {
				if _, ok := langColor[e.Node.Name]; !ok {
					langColor[e.Node.Name] = e.Node.Color
				}
			}
			p.TopRepos = append(p.TopRepos, info)
		}

		if !u.Repositories.PageInfo.HasNextPage {
			break
		}
		end := u.Repositories.PageInfo.EndCursor
		cursor = &end
	}

	p.ReposByLanguage = sortLangStats(reposPerLang, langColor)
	p.RepoCount = repoCount
	return p, nil
}

// sortLangStats returns a slice sorted desc by value; ties break alphabetically.
func sortLangStats(values map[string]int64, color map[string]string) []LangStat {
	out := make([]LangStat, 0, len(values))
	for name, v := range values {
		out = append(out, LangStat{Name: name, Color: color[name], Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].Name < out[j].Name
	})
	return out
}
