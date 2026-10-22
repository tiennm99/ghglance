package github

import (
	"context"
	"testing"
	"time"
)

func TestAttributeCommit(t *testing.T) {
	cases := []struct {
		name      string
		repo      RepoInfo
		preset    map[string]string
		wantValue map[string]int64
		wantColor map[string]string
	}{
		{
			name: "splits by byte share",
			repo: RepoInfo{Languages: []LangEdge{
				{Name: "Go", Color: "#go", Bytes: 3000},
				{Name: "JavaScript", Color: "#js", Bytes: 1000},
			}},
			wantValue: map[string]int64{"Go": 7500, "JavaScript": 2500},
			wantColor: map[string]string{"Go": "#go", "JavaScript": "#js"},
		},
		{
			name:      "falls back to the primary language without a byte breakdown",
			repo:      RepoInfo{PrimaryLanguage: "Rust", PrimaryColor: "#rs"},
			wantValue: map[string]int64{"Rust": scaleFactor},
			wantColor: map[string]string{"Rust": "#rs"},
		},
		{
			name:      "credits nothing for an empty repo",
			repo:      RepoInfo{},
			wantValue: map[string]int64{},
			wantColor: map[string]string{},
		},
		{
			name: "skips a share that rounds to zero",
			repo: RepoInfo{Languages: []LangEdge{
				{Name: "Go", Color: "#go", Bytes: 1_000_000},
				{Name: "Tiny", Color: "#tiny", Bytes: 1},
			}},
			// Integer division leaves Go just under a whole commit.
			wantValue: map[string]int64{"Go": scaleFactor - 1},
			wantColor: map[string]string{"Go": "#go"},
		},
		{
			name:      "keeps the first color seen for a language",
			repo:      RepoInfo{Languages: []LangEdge{{Name: "Go", Color: "#later", Bytes: 1}}},
			preset:    map[string]string{"Go": "#first"},
			wantValue: map[string]int64{"Go": scaleFactor},
			wantColor: map[string]string{"Go": "#first"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var total int64
			for _, l := range tc.repo.Languages {
				total += l.Bytes
			}
			values := map[string]int64{}
			colors := map[string]string{}
			for k, v := range tc.preset {
				colors[k] = v
			}
			attributeCommit(tc.repo, total, values, colors)
			if len(values) != len(tc.wantValue) {
				t.Errorf("values = %v, want %v", values, tc.wantValue)
			}
			for k, v := range tc.wantValue {
				if values[k] != v {
					t.Errorf("values[%s] = %d, want %d", k, values[k], v)
				}
			}
			for k, v := range tc.wantColor {
				if colors[k] != v {
					t.Errorf("colors[%s] = %q, want %q", k, colors[k], v)
				}
			}
		})
	}
}

// historyReply builds a commitHistoryQuery response.
func historyReply(t *testing.T, dates []time.Time, hasNext bool, cursor string) fakeReply {
	t.Helper()
	nodes := make([]any, 0, len(dates))
	for _, d := range dates {
		nodes = append(nodes, map[string]any{"committedDate": d.UTC().Format(time.RFC3339)})
	}
	return dataReply(t, map[string]any{"repository": map[string]any{
		"defaultBranchRef": map[string]any{"target": map[string]any{"history": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
			"nodes":    nodes,
		}}},
	}})
}

func TestFetchProductiveBucketsPaginatesAndStopsAtCap(t *testing.T) {
	saigon := time.FixedZone("UTC+7", 7*3600)
	// 23:30 UTC is 06:30 the next day in UTC+7.
	d := time.Now().UTC().AddDate(0, 0, -30)
	recent := time.Date(d.Year(), d.Month(), d.Day(), 23, 30, 0, 0, time.UTC)
	o := time.Now().UTC().AddDate(-2, 0, 0)
	old := time.Date(o.Year(), o.Month(), o.Day(), 12, 0, 0, 0, time.UTC) // 19:00 in UTC+7

	c, f := newFakeClient(t, func(call fakeCall) fakeReply {
		switch call.str("after") {
		case "":
			return historyReply(t, []time.Time{recent, old}, true, "page-2")
		case "page-2":
			return historyReply(t, []time.Time{recent}, true, "page-3")
		default:
			t.Errorf("fetched past the commit cap (after=%q)", call.str("after"))
			return historyReply(t, nil, false, "")
		}
	})

	p := &Profile{Login: "tiennm99", ID: "U_1"}
	repo := RepoInfo{Name: "ghglance", Languages: []LangEdge{{Name: "Go", Color: "#go", Bytes: 10}}}
	if err := c.FetchProductive(context.Background(), p, []RepoInfo{repo}, saigon, 3); err != nil {
		t.Fatalf("FetchProductive: %v", err)
	}

	calls := f.callsFor(commitHistoryQuery)
	if len(calls) != 2 {
		t.Fatalf("history requests = %d, want 2 (cap of 3 reached on page 2)", len(calls))
	}
	if calls[1].str("after") != "page-2" {
		t.Errorf("page 2 cursor = %q, want page-2", calls[1].str("after"))
	}
	if calls[0].str("owner") != "tiennm99" || calls[0].str("userId") != "U_1" {
		t.Errorf("owner/userId = %q/%q, want the profile's login and ID", calls[0].str("owner"), calls[0].str("userId"))
	}

	recentDay := recent.In(saigon).Weekday()
	if p.Productive[6] != 2 || p.ProductiveAllTime[6] != 2 || p.ProductiveAllTime[19] != 1 || p.Productive[19] != 0 {
		t.Errorf("hour buckets: last-year[6]=%d all[6]=%d all[19]=%d last-year[19]=%d; want 2,2,1,0",
			p.Productive[6], p.ProductiveAllTime[6], p.ProductiveAllTime[19], p.Productive[19])
	}
	if p.Weekday[recentDay] != 2 {
		t.Errorf("Weekday[%s] = %d, want 2", recentDay, p.Weekday[recentDay])
	}
	if len(p.CommitsByLanguage) != 1 || p.CommitsByLanguage[0].Value != 2*scaleFactor {
		t.Errorf("CommitsByLanguage = %+v, want Go with 2 commits", p.CommitsByLanguage)
	}
	if len(p.CommitsByLanguageAllTime) != 1 || p.CommitsByLanguageAllTime[0].Value != 3*scaleFactor {
		t.Errorf("CommitsByLanguageAllTime = %+v, want Go with 3 commits", p.CommitsByLanguageAllTime)
	}
}

func TestFetchProductiveKeepsLanguageTotalsWhenALaterRepoFails(t *testing.T) {
	recent := time.Now().UTC().AddDate(0, 0, -1)
	c, _ := newFakeClient(t, func(call fakeCall) fakeReply {
		if call.str("repo") == "broken" {
			return fakeReply{Status: 502, Body: "bad gateway"}
		}
		return historyReply(t, []time.Time{recent}, false, "")
	})

	p := &Profile{Login: "tiennm99", ID: "U_1"}
	repos := []RepoInfo{
		{Name: "ok", Languages: []LangEdge{{Name: "Go", Color: "#go", Bytes: 10}}},
		{Name: "broken", Languages: []LangEdge{{Name: "Go", Color: "#go", Bytes: 10}}},
	}
	if err := c.FetchProductive(context.Background(), p, repos, time.UTC, 0); err == nil {
		t.Fatal("want the failing repo's error")
	}
	if p.ProductiveAllTime[recent.Hour()] != 1 {
		t.Errorf("hour bucket = %d, want the first repo's commit", p.ProductiveAllTime[recent.Hour()])
	}
	if len(p.CommitsByLanguage) != 1 || len(p.CommitsByLanguageAllTime) != 1 {
		t.Errorf("language stats = %+v / %+v, want the first repo's Go commit in both",
			p.CommitsByLanguage, p.CommitsByLanguageAllTime)
	}
}
