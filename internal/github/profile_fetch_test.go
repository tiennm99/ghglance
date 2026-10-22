package github

import (
	"context"
	"strings"
	"testing"
)

// profileUser builds the user object of a profileQuery response.
func profileUser(login, name string, followers int, repos []any, hasNext bool, cursor string) map[string]any {
	return map[string]any{
		"id":                        "U_1",
		"login":                     login,
		"name":                      name,
		"createdAt":                 "2015-06-01T00:00:00Z",
		"followers":                 map[string]any{"totalCount": followers},
		"following":                 map[string]any{"totalCount": 2},
		"pullRequests":              map[string]any{"totalCount": 7},
		"issues":                    map[string]any{"totalCount": 3},
		"repositoriesContributedTo": map[string]any{"totalCount": 4},
		"contributionsCollection": map[string]any{
			"contributionYears":                   []int{2026, 2025},
			"totalCommitContributions":            30,
			"totalPullRequestReviewContributions": 6,
			"restrictedContributionsCount":        5,
			"contributionCalendar": map[string]any{
				"totalContributions": 40,
				"weeks": []any{map[string]any{"contributionDays": []any{
					map[string]any{"contributionCount": 1, "date": "2026-01-01"},
					map[string]any{"contributionCount": 2, "date": "2026-01-02"},
				}}},
			},
		},
		"repositories": map[string]any{
			"totalCount": 99,
			"pageInfo":   map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
			"nodes":      repos,
		},
	}
}

func withRepoFields(r map[string]any, kv ...any) map[string]any {
	for i := 0; i+1 < len(kv); i += 2 {
		r[kv[i].(string)] = kv[i+1]
	}
	return r
}

func TestFetchProfileAggregatesFilteredReposAcrossPages(t *testing.T) {
	page0 := []any{
		withRepoFields(repoJSON("tiennm99", "own", "Go", 100), "stargazerCount", 5, "forkCount", 1),
		withRepoFields(repoJSON("tiennm99", "fork", "Go", 100), "isFork", true, "stargazerCount", 100),
		withRepoFields(repoJSON("tiennm99", "secret", "Go", 100), "isPrivate", true, "stargazerCount", 7),
		withRepoFields(repoJSON("miti99dev", "org-admin", "Go", 100), "viewerPermission", "ADMIN", "stargazerCount", 3),
		withRepoFields(repoJSON("miti99dev", "org-write", "Go", 100), "viewerPermission", "WRITE", "stargazerCount", 50),
	}
	page1 := []any{
		withRepoFields(repoJSON("tiennm99", "second", "Python", 100), "stargazerCount", 2, "forkCount", 2),
	}
	c, f := newFakeClient(t, func(call fakeCall) fakeReply {
		if call.str("after") == "" {
			return dataReply(t, map[string]any{"user": profileUser("tiennm99", "Tien", 10, page0, true, "cursor-1")})
		}
		return dataReply(t, map[string]any{"user": profileUser("tiennm99", "Overwritten", 999, page1, false, "")})
	})

	// The login is typed with different casing than the account's own.
	p, err := c.FetchProfile(context.Background(), "TienNM99", FetchOptions{IncludeOrgRepos: true})
	if err != nil {
		t.Fatalf("FetchProfile: %v", err)
	}

	if p.Login != "tiennm99" {
		t.Errorf("Login = %q, want the canonical tiennm99", p.Login)
	}
	if p.Name != "Tien" || p.Followers != 10 {
		t.Errorf("Name/Followers = %q/%d, want page-0 values Tien/10", p.Name, p.Followers)
	}
	if p.TotalContributionsLastYear != 45 {
		t.Errorf("TotalContributionsLastYear = %d, want calendar 40 + restricted 5", p.TotalContributionsLastYear)
	}
	if len(p.DailyContributions) != 2 || len(p.ContributionYears) != 2 {
		t.Errorf("daily = %d, years = %d; want 2 and 2", len(p.DailyContributions), len(p.ContributionYears))
	}
	// Kept: own, org-admin, second. Dropped: fork, private, org-write.
	if p.RepoCount != 3 || p.TotalStars != 10 || p.TotalForks != 3 {
		t.Errorf("repos/stars/forks = %d/%d/%d, want 3/10/3", p.RepoCount, p.TotalStars, p.TotalForks)
	}
	if len(p.ReposByLanguage) != 2 || p.ReposByLanguage[0].Name != "Go" || p.ReposByLanguage[0].Value != 2 {
		t.Errorf("ReposByLanguage = %+v, want Go 2 then Python 1", p.ReposByLanguage)
	}

	calls := f.callsFor(profileQuery)
	if len(calls) != 2 {
		t.Fatalf("profile requests = %d, want 2 pages", len(calls))
	}
	if calls[0].str("after") != "" || calls[1].str("after") != "cursor-1" {
		t.Errorf("after cursors = %q, %q; want none then cursor-1", calls[0].str("after"), calls[1].str("after"))
	}
}

func TestFetchProfileKeepsOwnReposWhenLoginCasingDiffers(t *testing.T) {
	repos := []any{withRepoFields(repoJSON("tiennm99", "own", "Go", 100), "stargazerCount", 4)}
	c, _ := newFakeClient(t, func(fakeCall) fakeReply {
		return dataReply(t, map[string]any{"user": profileUser("tiennm99", "Tien", 1, repos, false, "")})
	})
	p, err := c.FetchProfile(context.Background(), "TIENNM99", FetchOptions{})
	if err != nil {
		t.Fatalf("FetchProfile: %v", err)
	}
	if p.RepoCount != 1 || p.TotalStars != 4 {
		t.Errorf("repos/stars = %d/%d, want 1/4", p.RepoCount, p.TotalStars)
	}
	if len(p.TopRepos) != 1 || p.TopRepos[0].Owner != "tiennm99" {
		t.Errorf("TopRepos = %+v, want one repo owned by tiennm99", p.TopRepos)
	}
}

func TestFetchProfileReportsMissingUser(t *testing.T) {
	c, _ := newFakeClient(t, func(fakeCall) fakeReply {
		return dataReply(t, map[string]any{"user": nil})
	})
	_, err := c.FetchProfile(context.Background(), "nobody", FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "user not found") {
		t.Fatalf("err = %v, want \"user not found\"", err)
	}
}
