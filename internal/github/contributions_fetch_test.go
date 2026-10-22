package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// windowOf parses the from/to variables of a contributionYearQuery call.
func windowOf(t *testing.T, call fakeCall) (from, to time.Time) {
	t.Helper()
	from, err := time.Parse(time.RFC3339, call.str("from"))
	if err != nil {
		t.Fatalf("bad from %q: %v", call.str("from"), err)
	}
	to, err = time.Parse(time.RFC3339, call.str("to"))
	if err != nil {
		t.Fatalf("bad to %q: %v", call.str("to"), err)
	}
	return from, to
}

// isMonthWindow tells a month re-query apart from a quarter window.
func isMonthWindow(from, to time.Time) bool {
	return to.Sub(from) < 40*24*time.Hour
}

// contributionWindowReply builds a contributionYearQuery response.
func contributionWindowReply(t *testing.T, commits int, days map[string]int, repos []any) fakeReply {
	t.Helper()
	dayList := []any{}
	for d, n := range days {
		dayList = append(dayList, map[string]any{"date": d, "contributionCount": n})
	}
	byRepo := make([]any, 0, len(repos))
	for _, r := range repos {
		byRepo = append(byRepo, map[string]any{
			"contributions": map[string]any{"totalCount": 1},
			"repository":    r,
		})
	}
	return dataReply(t, map[string]any{"user": map[string]any{
		"contributionsCollection": map[string]any{
			"totalCommitContributions": commits,
			"contributionCalendar": map[string]any{
				"weeks": []any{map[string]any{"contributionDays": dayList}},
			},
			"commitContributionsByRepository": byRepo,
		},
	}})
}

func TestFetchContributionsAllTimeRecoversSaturatedQuarterByMonth(t *testing.T) {
	c, f := newFakeClient(t, func(call fakeCall) fakeReply {
		from, to := windowOf(t, call)
		switch {
		case isMonthWindow(from, to):
			// Month re-queries: one new repo each, a repo the quarter already
			// listed, plus a fork and a private repo that the default
			// options filter out. Their totals and days must be ignored.
			m := from.Format("01")
			return contributionWindowReply(t, 999, map[string]int{from.Format("2006-01-02"): 50}, []any{
				repoJSON("tiennm99", "month-"+m, "Go", 10),
				repoJSON("tiennm99", "q0", "Go", 10),
				withRepoFields(repoJSON("tiennm99", "fork-"+m, "Go", 10), "isFork", true),
				withRepoFields(repoJSON("tiennm99", "private-"+m, "Go", 10), "isPrivate", true),
			})
		case from.Month() == time.January:
			repos := make([]any, maxRepositoriesPerWindow)
			for i := range repos {
				repos[i] = repoJSON("tiennm99", fmt.Sprintf("q%d", i), "Go", 10)
			}
			return contributionWindowReply(t, 500, map[string]int{
				"2024-01-01": 1, "2024-02-01": 1, "2024-03-01": 1,
			}, repos)
		default:
			return contributionWindowReply(t, 7, map[string]int{from.Format("2006-01-02"): 7},
				[]any{repoJSON("tiennm99", "late", "Go", 10)})
		}
	})

	p := &Profile{Login: "tiennm99", ContributionYears: []int{2024}}
	if err := c.FetchContributionsAllTime(context.Background(), p, FetchOptions{}); err != nil {
		t.Fatalf("FetchContributionsAllTime: %v", err)
	}

	if got := len(f.callsFor(contributionYearQuery)); got != 4+3 {
		t.Errorf("requests = %d, want 4 quarters + 3 month re-queries", got)
	}
	if p.TotalCommitsAllTime != 500+3*7 {
		t.Errorf("TotalCommitsAllTime = %d, want %d from the quarters alone", p.TotalCommitsAllTime, 500+3*7)
	}
	if len(p.DailyContributionsAllTime) != 3+3 {
		t.Errorf("all-time days = %d, want 6 from the quarters alone", len(p.DailyContributionsAllTime))
	}

	seen := map[string]int{}
	for _, r := range p.SeedRepos {
		seen[r.Name]++
		if strings.HasPrefix(r.Name, "fork-") || strings.HasPrefix(r.Name, "private-") {
			t.Errorf("seed %s should have been filtered out", r.Name)
		}
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("seed %s listed %d times, want once", name, n)
		}
	}
	if len(p.SeedRepos) != maxRepositoriesPerWindow+3+1 {
		t.Errorf("seeds = %d, want 100 quarter repos + 3 month-only + late", len(p.SeedRepos))
	}

	// Most recently active first: late (Q4), then the repos last seen at the
	// end of March in first-seen order, then February's and January's.
	if p.SeedRepos[0].Name != "late" {
		t.Errorf("first seed = %s, want late", p.SeedRepos[0].Name)
	}
	if p.SeedRepos[1].Name != "q0" {
		t.Errorf("second seed = %s, want q0", p.SeedRepos[1].Name)
	}
	n := len(p.SeedRepos)
	if p.SeedRepos[n-2].Name != "month-02" || p.SeedRepos[n-1].Name != "month-01" {
		t.Errorf("last seeds = %s, %s; want month-02, month-01", p.SeedRepos[n-2].Name, p.SeedRepos[n-1].Name)
	}
}

func TestFetchContributionsAllTimeWarnsAtMonthCeiling(t *testing.T) {
	full := make([]any, maxRepositoriesPerWindow)
	for i := range full {
		full[i] = repoJSON("tiennm99", fmt.Sprintf("r%d", i), "Go", 10)
	}
	c, _ := newFakeClient(t, func(fakeCall) fakeReply {
		return contributionWindowReply(t, 1, nil, full)
	})
	var warnings []string
	c.warnf = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	p := &Profile{Login: "tiennm99", ContributionYears: []int{2024}}
	if err := c.FetchContributionsAllTime(context.Background(), p, FetchOptions{}); err != nil {
		t.Fatalf("a saturated month must stay a warning, got error %v", err)
	}
	if len(warnings) != 12 || !strings.Contains(warnings[0], "ceiling") {
		t.Errorf("warnings = %d (%q...), want one ceiling notice per month", len(warnings), warnings)
	}
}

func TestFetchContributionsAllTimeReportsWindowsWithoutUser(t *testing.T) {
	c, _ := newFakeClient(t, func(call fakeCall) fakeReply {
		from, _ := windowOf(t, call)
		if from.Month() == time.April {
			return dataReply(t, map[string]any{"user": nil})
		}
		return contributionWindowReply(t, 7, nil, []any{repoJSON("tiennm99", "r", "Go", 10)})
	})

	p := &Profile{Login: "tiennm99", ContributionYears: []int{2024}}
	err := c.FetchContributionsAllTime(context.Background(), p, FetchOptions{})
	if !errors.Is(err, errNoUserData) || !strings.Contains(err.Error(), "2024-04-01..2024-06-30") {
		t.Fatalf("err = %v, want a no-user-data error naming the April window", err)
	}
	if p.TotalCommitsAllTime != 3*7 {
		t.Errorf("TotalCommitsAllTime = %d, want the other three quarters (21)", p.TotalCommitsAllTime)
	}
}
