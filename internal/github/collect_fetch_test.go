package github

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// collectFake answers the three queries Collect runs: a profile active in
// 2024, an "old" repo committed to in Q1 and a "new" one in Q4. aprilNoUser
// makes the Q2 window come back without a user; historyStatus, when non-zero,
// fails every commit-history request with that HTTP status.
func collectFake(t *testing.T, aprilNoUser bool, historyStatus int) func(fakeCall) fakeReply {
	return func(call fakeCall) fakeReply {
		switch call.Query {
		case profileQuery:
			u := profileUser("tiennm99", "Tien", 1, nil, false, "")
			u["contributionsCollection"].(map[string]any)["contributionYears"] = []int{2024}
			return dataReply(t, map[string]any{"user": u})
		case contributionYearQuery:
			from, _ := windowOf(t, call)
			switch from.Month() {
			case time.January:
				return contributionWindowReply(t, 1, nil, []any{repoJSON("tiennm99", "old", "Go", 10)})
			case time.April:
				if aprilNoUser {
					return dataReply(t, map[string]any{"user": nil})
				}
			case time.October:
				return contributionWindowReply(t, 1, nil, []any{repoJSON("tiennm99", "new", "Go", 10)})
			}
			return contributionWindowReply(t, 0, nil, nil)
		case commitHistoryQuery:
			if historyStatus != 0 {
				return fakeReply{Status: historyStatus, Body: "upstream failure"}
			}
			return historyReply(t, []time.Time{time.Now().AddDate(0, 0, -1)}, false, "")
		}
		t.Errorf("unexpected query %.40q", call.Query)
		return fakeReply{Status: 500}
	}
}

func TestCollectTopReposKeepsMostRecentlyActive(t *testing.T) {
	c, f := newFakeClient(t, collectFake(t, false, 0))
	p, err := Collect(context.Background(), c, "tiennm99", CollectConfig{TopRepos: 1})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	calls := f.callsFor(commitHistoryQuery)
	if len(calls) != 1 || calls[0].str("repo") != "new" {
		t.Errorf("probed repos = %v, want only the recently active \"new\"", calls)
	}
	if len(p.SeedRepos) != 2 {
		t.Errorf("seeds = %d, want both repos listed before the cap", len(p.SeedRepos))
	}
}

func TestCollectStrictTurnsStageFailuresIntoErrors(t *testing.T) {
	cases := map[string]struct {
		aprilNoUser   bool
		historyStatus int
		wantErr       string
	}{
		"window without user data": {aprilNoUser: true, wantErr: "fetch all-time contributions"},
		"commit history failure":   {historyStatus: 502, wantErr: "fetch commit history"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := newFakeClient(t, collectFake(t, tc.aprilNoUser, tc.historyStatus))
			var warnings []string
			_, err := Collect(context.Background(), c, "tiennm99", CollectConfig{
				Strict: true,
				Warnf:  func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) },
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
			if len(warnings) != 0 {
				t.Errorf("strict run also warned: %q", warnings)
			}
		})
	}
}

func TestCollectWarnsAndReturnsPartialProfileWhenNotStrict(t *testing.T) {
	c, _ := newFakeClient(t, collectFake(t, true, 502))
	var warnings []string
	p, err := Collect(context.Background(), c, "tiennm99", CollectConfig{
		Warnf: func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) },
	})
	if err != nil || p == nil {
		t.Fatalf("Collect = %v, %v; want a partial profile and no error", p, err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "no user data") || !strings.Contains(joined, "productive-time") {
		t.Errorf("warnings = %q, want both stage failures reported", warnings)
	}
	if c.warnf != nil {
		t.Error("Collect replaced the caller's client warn sink")
	}
}
