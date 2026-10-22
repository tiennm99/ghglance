package github

import (
	"context"
	"time"
)

// productiveGQL is the response shape for commitHistoryQuery.
type productiveGQL struct {
	Repository *struct {
		DefaultBranchRef *struct {
			Target *struct {
				History struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						CommittedDate string `json:"committedDate"`
					} `json:"nodes"`
				} `json:"history"`
			} `json:"target"`
		} `json:"defaultBranchRef"`
	} `json:"repository"`
}

// scaleFactor is the fixed-point multiplier used when distributing a single
// commit across several languages by byte share. Stored in LangStat.Value
// (int64) so the existing sort + percentage math keeps working; the absolute
// magnitude is irrelevant because the card renders percentages.
const scaleFactor = 10_000

// reachedCommitCap reports whether a repo has given up enough commits to stop
// paginating. A cap of zero or less means no cap — keep going until the
// history runs out — matching how -top-repos treats zero. Without the guard a
// zero cap would stop before the first page and quietly empty every
// commit-derived card.
func reachedCommitCap(seen, maxPerRepo int) bool {
	if maxPerRepo <= 0 {
		return false
	}
	return seen >= maxPerRepo
}

// FetchProductive paginates the default-branch commit history (authored by
// the target user) for each repo up to maxPerRepo commits (0 = all), and
// fills two parallel sets of aggregates on the Profile:
//
//   - Last-year: p.Productive (24h histogram) and p.CommitsByLanguage
//   - All-time:  p.ProductiveAllTime and p.CommitsByLanguageAllTime
//
// One pagination pass populates both, so the all-time cards come at no extra
// API cost beyond the pages already required for the last-year bucket.
// loc is applied to CommittedDate so the heatmap reflects the user's tz.
func (c *Client) FetchProductive(ctx context.Context, p *Profile, repos []RepoInfo, loc *time.Location, maxPerRepo int) error {
	if loc == nil {
		loc = time.UTC
	}
	yearAgo := time.Now().AddDate(-1, 0, 0)

	lastYearLang := map[string]int64{}
	allTimeLang := map[string]int64{}
	langColor := map[string]string{}

	// Build the language aggregates on every exit path, so an error part-way
	// through leaves them as partially filled as the hour and weekday ones.
	defer func() {
		p.CommitsByLanguage = sortLangStats(lastYearLang, langColor)
		p.CommitsByLanguageAllTime = sortLangStats(allTimeLang, langColor)
	}()

	for _, repo := range repos {
		// Precompute the repo's total language bytes once; attributeCommit
		// reuses it for every commit instead of re-summing ~10 edges per
		// call in the inner loop.
		var repoTotal int64
		for _, l := range repo.Languages {
			repoTotal += l.Bytes
		}

		var cursor *string
		seen := 0
		for !reachedCommitCap(seen, maxPerRepo) {
			owner := repo.Owner
			if owner == "" {
				owner = p.Login
			}
			vars := map[string]any{
				"owner":  owner,
				"repo":   repo.Name,
				"userId": p.ID,
			}
			if cursor != nil {
				vars["after"] = *cursor
			}

			var resp productiveGQL
			if err := c.query(ctx, commitHistoryQuery, vars, &resp); err != nil {
				return err
			}
			if resp.Repository == nil || resp.Repository.DefaultBranchRef == nil ||
				resp.Repository.DefaultBranchRef.Target == nil {
				break
			}
			h := resp.Repository.DefaultBranchRef.Target.History
			for _, n := range h.Nodes {
				t, err := time.Parse(time.RFC3339, n.CommittedDate)
				if err != nil {
					continue
				}
				tl := t.In(loc)
				p.ProductiveAllTime[tl.Hour()]++
				p.WeekdayAllTime[tl.Weekday()]++
				attributeCommit(repo, repoTotal, allTimeLang, langColor)
				if tl.After(yearAgo) {
					p.Productive[tl.Hour()]++
					p.Weekday[tl.Weekday()]++
					attributeCommit(repo, repoTotal, lastYearLang, langColor)
				}
				seen++
			}
			if !h.PageInfo.HasNextPage {
				break
			}
			end := h.PageInfo.EndCursor
			cursor = &end
		}
	}
	return nil
}

// attributeCommit distributes a single commit across the repo's languages
// proportional to byte share. Callers pass in the precomputed per-repo byte
// total so this hot-path loop doesn't re-sum language edges every commit.
// Falls back to the primary language when no byte breakdown is available
// (empty repo or linguist-free repo).
func attributeCommit(repo RepoInfo, repoTotal int64, commitsByLang map[string]int64, langColor map[string]string) {
	if repoTotal == 0 {
		if repo.PrimaryLanguage != "" {
			commitsByLang[repo.PrimaryLanguage] += scaleFactor
			if _, ok := langColor[repo.PrimaryLanguage]; !ok {
				langColor[repo.PrimaryLanguage] = repo.PrimaryColor
			}
		}
		return
	}
	for _, l := range repo.Languages {
		share := int64(scaleFactor) * l.Bytes / repoTotal
		if share == 0 {
			continue
		}
		commitsByLang[l.Name] += share
		if _, ok := langColor[l.Name]; !ok {
			langColor[l.Name] = l.Color
		}
	}
}
