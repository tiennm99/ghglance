package card

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

func TestRenderAll(t *testing.T) {
	// Name is rendered in every card's title via cardTitle(). Putting
	// XML-significant chars here exercises escapeXML through the real
	// rendering pipeline, not just through the unit test below.
	p := &github.Profile{
		Login:      "tiennm99",
		Name:       `Alice & <bob> "quoted"`,
		Company:    "VNG & <Corp>",
		Followers:  12,
		Following:  7,
		RepoCount:  42,
		TotalStars: 1234,
		ReposByLanguage: []github.LangStat{
			{Name: "Go", Color: "#00ADD8", Value: 5},
			{Name: "TypeScript", Color: "#3178c6", Value: 3},
			{Name: "Python", Color: "", Value: 2},
		},
		CommitsByLanguage: []github.LangStat{
			{Name: "Go", Color: "#00ADD8", Value: 420},
			{Name: "Python", Color: "#3572A5", Value: 150},
		},
		TopRepos: []github.RepoInfo{
			{Owner: "tiennm99", Name: "ghglance", Stars: 42, PrimaryLanguage: "Go", PrimaryColor: "#00ADD8"},
			{Owner: "tiennm99", Name: "some-app & <tool>", Stars: 17, PrimaryLanguage: "TypeScript", PrimaryColor: "#3178c6"},
			{Owner: "tiennm99", Name: "fork-only", Stars: 99, IsFork: true},
		},
	}
	p.Productive[9] = 3
	p.Productive[14] = 7
	p.Weekday[time.Tuesday] = 12
	p.Weekday[time.Thursday] = 5
	p.WeekdayAllTime[time.Monday] = 30
	p.WeekdayAllTime[time.Friday] = 42

	// Last-year daily series — one contribution every Monday, plus a burst
	// covering a three-day streak so computeStreak has something to find.
	base := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 365; i++ {
		d := github.DailyContribution{Date: base.AddDate(0, 0, i)}
		if d.Date.Weekday() == time.Monday {
			d.Count = 2
		}
		p.DailyContributions = append(p.DailyContributions, d)
	}
	p.DailyContributions[100].Count = 7
	p.DailyContributions[101].Count = 5
	p.DailyContributions[102].Count = 3
	// All-time series covers 3 full years so the by-year card has ≥3 bars.
	allBase := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 365*3; i++ {
		d := github.DailyContribution{Date: allBase.AddDate(0, 0, i)}
		if i%5 == 0 {
			d.Count = 1
		}
		p.DailyContributionsAllTime = append(p.DailyContributionsAllTime, d)
	}

	th, ok := theme.Lookup("dracula")
	if !ok {
		t.Fatal("dracula theme missing")
	}
	dir := t.TempDir()
	if err := RenderAll(p, th, dir); err != nil {
		t.Fatalf("RenderAll: %v", err)
	}

	want := []string{
		"profile-details.svg",
		"repos-per-language.svg",
		"most-commit-language.svg",
		"stats.svg",
		"productive-time.svg",
		"productive-weekday.svg",
		"contributions.svg",
		"contributions-heatmap.svg",
		"top-starred-repos.svg",
		"streak.svg",
		"most-commit-language-all-time.svg",
		"productive-time-all-time.svg",
		"productive-weekday-all-time.svg",
		"contributions-all-time.svg",
		"contributions-by-year.svg",
		"records.svg",
	}
	for _, name := range want {
		data, err := os.ReadFile(filepath.Join(dir, "dracula", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		content := string(data)
		if !strings.HasPrefix(content, "<svg") {
			t.Errorf("%s: missing <svg prefix", name)
		}
		// The raw Name string contains `&`, `<`, `>`, `"`. None should
		// appear unescaped in the final markup. Presence of the escaped
		// forms also verifies the title actually got rendered.
		for _, leak := range []string{`Alice & <bob>`, `VNG & <Corp>`, `"quoted"`} {
			if strings.Contains(content, leak) {
				t.Errorf("%s: raw XML special characters leaked through escape (%q)", name, leak)
			}
		}
	}
}

// TestDonutSingleSlice verifies the donut renderer handles a single-slice
// case (100%) with visible geometry. Regression guard against the empty-arc
// bug where start == end degenerates the SVG A command.
func TestDonutSingleSlice(t *testing.T) {
	th, _ := theme.Lookup("dracula")
	stats := []github.LangStat{{Name: "Go", Color: "#00ADD8", Value: 100}}
	svg := string(renderDonutCard("Test", stats, th))

	if !strings.Contains(svg, "<circle") {
		t.Errorf("single-slice donut should use <circle> primitives; got:\n%s", svg)
	}
	if strings.Contains(svg, `A70.00,70.00 0 1 1 380.00,50.00`) {
		t.Error("single-slice donut still emits degenerate arc")
	}
}

// TestDonutEmpty verifies the zero-stats fallback path.
func TestDonutEmpty(t *testing.T) {
	th, _ := theme.Lookup("dracula")
	svg := string(renderDonutCard("Test", nil, th))
	if !strings.Contains(svg, "No data available") {
		t.Errorf("empty donut should render the no-data fallback; got:\n%s", svg)
	}
}

// TestDonutMaxSevenRows confirms the legend caps at 7 rows (Other inclusive):
// 9 input languages collapse to the top 6 named + "Other". Regression guard
// against anyone flipping the collapse semantic back to "7 named + Other".
func TestDonutMaxSevenRows(t *testing.T) {
	th, _ := theme.Lookup("dracula")
	stats := []github.LangStat{
		{Name: "Go", Color: "#00ADD8", Value: 100},
		{Name: "TypeScript", Color: "#3178c6", Value: 90},
		{Name: "Python", Color: "#3572A5", Value: 80},
		{Name: "Rust", Color: "#dea584", Value: 70},
		{Name: "JavaScript", Color: "#f1e05a", Value: 60},
		{Name: "HTML", Color: "#e34c26", Value: 50},
		{Name: "Shell", Color: "#89e051", Value: 40},
		{Name: "Kotlin", Color: "#A97BFF", Value: 30},
		{Name: "Java", Color: "#b07219", Value: 20},
	}
	svg := string(renderDonutCard("Test", stats, th))
	for _, want := range []string{"Go", "TypeScript", "Python", "Rust", "JavaScript", "HTML", "Other"} {
		if !strings.Contains(svg, ">"+want+" ") {
			t.Errorf("expected legend row for %q; not found in:\n%s", want, svg)
		}
	}
	// Shell, Kotlin, Java are outside the top 6 and roll into Other.
	for _, dropped := range []string{">Shell ", ">Kotlin ", ">Java "} {
		if strings.Contains(svg, dropped) {
			t.Errorf("expected %q to be collapsed into Other, but it renders:\n%s", dropped, svg)
		}
	}
}

func TestFormatInt(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		12:      "12",
		999:     "999",
		1000:    "1,000",
		12345:   "12,345",
		1234567: "1,234,567",
		-12345:  "-12,345",
	}
	for in, want := range cases {
		if got := formatInt(in); got != want {
			t.Errorf("formatInt(%d)=%q want %q", in, got, want)
		}
	}
}

// TestFitTitleFontSize locks in the title-sizing behavior for the set of
// realistic titles the renderers actually emit. Any regression that shrinks
// a short title below 15 px, or lets a 40-char title stay at 15 px when it
// would overflow, will fail this test.
func TestFitTitleFontSize(t *testing.T) {
	const width = 340
	cases := []struct {
		title string
		want  int
	}{
		{"", 15},
		{"Stats", 15},
		{"Streak", 15},
		{"Top Starred Repos", 15},
		{"Most Commit Language (all time)", 15}, // 31 chars
		{"Contributions by Year", 15},
		{"Commits by Hour (last year, UTC+7)", 15},    // 34 chars, common integer-zone case
		{"Commits by Hour (last year, UTC+5:45)", 14}, // 37 chars, quarter-hour zone (Kathmandu)
		{"Commits by Weekday (last year)", 15},        // 30 chars — weekday titles never include UTC
		{strings.Repeat("x", 200), 11},                // pathological
	}
	for _, c := range cases {
		got := fitTitleFontSize(c.title, width)
		if got != c.want {
			t.Errorf("fitTitleFontSize(%q)=%d want %d", c.title, got, c.want)
		}
	}
}

// TestNiceTicksCoversMax guards the key invariant: the last tick returned
// must be ≥ the requested max, or bar-chart cards render bars taller than
// the chart area and poke into the title. Regression case: max=625 step=100
// previously gave ticks stopping at 600 and bars at 625/600 = 104 % of
// chartH.
func TestNiceTicksCoversMax(t *testing.T) {
	cases := []float64{625, 99, 101, 1, 7, 49, 999, 1000, 1001}
	for _, m := range cases {
		ticks := niceTicks(m, 5)
		if len(ticks) == 0 {
			t.Errorf("niceTicks(%v): empty", m)
			continue
		}
		last := ticks[len(ticks)-1]
		if last < m {
			t.Errorf("niceTicks(%v): last tick %v < max — bars will overflow", m, last)
		}
	}
}

func TestEscapeXML(t *testing.T) {
	got := escapeXML(`<a & "b" 'c'>`)
	want := "&lt;a &amp; &quot;b&quot; &apos;c&apos;&gt;"
	if got != want {
		t.Errorf("escapeXML=%q want %q", got, want)
	}
}

// TestCardsFitFrame renders every card against an adversarial profile and
// asserts every positional attribute stays inside the 340×200 frame. This is
// the automated half of the "fit-the-frame invariant" from
// docs/design-guidelines.md — a guard against future card changes that would
// silently overflow only for non-author profiles.
func TestCardsFitFrame(t *testing.T) {
	p := adversarialProfile()
	th, _ := theme.Lookup("dracula")
	dir := t.TempDir()
	if err := RenderAll(p, th, dir); err != nil {
		t.Fatalf("RenderAll: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "dracula"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, "dracula", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		assertInFrame(t, e.Name(), string(data))
	}
}

// attrCoord captures the numeric value of any positional SVG attribute that
// could push content outside the frame. We ignore path `d` attributes — their
// coordinates are always clamped by the chart geometry, and the regex would
// be fragile against the Catmull-Rom Bezier output.
var attrCoord = regexp.MustCompile(`(?:x|y|x1|y1|x2|y2|cx|cy)="(-?\d+(?:\.\d+)?)"`)

// textBlock captures the opening <text …> tag attributes and the inner text.
// We parse individual attributes with separate regexes so attribute order
// doesn't matter.
var textBlock = regexp.MustCompile(`<text\s+([^>]*)>([^<]*)</text>`)
var attrX = regexp.MustCompile(`\bx="(-?\d+(?:\.\d+)?)"`)
var attrAnchor = regexp.MustCompile(`\btext-anchor="([^"]+)"`)
var attrFontSize = regexp.MustCompile(`\bfont-size="(\d+)"`)

func assertInFrame(t *testing.T, name, svg string) {
	t.Helper()
	const (
		maxX = 340
		maxY = 200
	)
	for _, m := range attrCoord.FindAllStringSubmatch(svg, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		isX := strings.HasPrefix(m[0], "x") || strings.HasPrefix(m[0], "cx")
		limit := float64(maxY)
		if isX {
			limit = float64(maxX)
		}
		if v < -1 || v > limit+0.5 {
			t.Errorf("%s: attribute %q value %v outside frame (limit %v)", name, m[0], v, limit)
		}
	}

	// <text> elements: the x attribute is the anchor point, but the string
	// actually extends outward from it. Right-anchored axis labels are the
	// classic overflow trap — the attr x stays inside the frame while the
	// rendered digits spill past x=0. Estimate width conservatively
	// (0.6 × font-size per char; Segoe UI avg is ~0.55).
	for _, m := range textBlock.FindAllStringSubmatch(svg, -1) {
		attrs := m[1]
		text := m[2]

		xm := attrX.FindStringSubmatch(attrs)
		if xm == nil {
			continue
		}
		x, err := strconv.ParseFloat(xm[1], 64)
		if err != nil {
			continue
		}
		anchor := ""
		if am := attrAnchor.FindStringSubmatch(attrs); am != nil {
			anchor = am[1]
		}
		fontSize := 12.0
		if fm := attrFontSize.FindStringSubmatch(attrs); fm != nil {
			if f, err := strconv.ParseFloat(fm[1], 64); err == nil {
				fontSize = f
			}
		}

		width := float64(runeLen(text)) * fontSize * 0.6
		var left, right float64
		switch anchor {
		case "end":
			left, right = x-width, x
		case "middle":
			left, right = x-width/2, x+width/2
		default:
			left, right = x, x+width
		}
		if left < -2 {
			t.Errorf("%s: <text>%q at x=%v anchor=%q extends to left=%.1f (outside frame)", name, text, x, anchor, left)
		}
		if right > maxX+2 {
			t.Errorf("%s: <text>%q at x=%v anchor=%q extends to right=%.1f (outside frame)", name, text, x, anchor, right)
		}
	}
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// adversarialProfile exercises every card against the worst-case inputs a
// real user might have: huge counts, long names, 20 active years, 53-week
// calendar. Kept alongside the stress test so updates stay colocated.
func adversarialProfile() *github.Profile {
	p := &github.Profile{
		Login:          "user-with-a-very-long-login-name",
		Name:           "A Very Long Display Name That Keeps Going",
		UTCOffsetLabel: "UTC+12:45", // quarter-hour zone — longest realistic UTC label

		Company:                    "A-Company-With-An-Unusually-Long-Name Pty Ltd",
		Location:                   "A Place With A Name That Is Way Too Long To Fit",
		Website:                    "https://example-with-a-very-long-domain.example.com/profile",
		Followers:                  1_234_567,
		Following:                  98_765,
		RepoCount:                  4_321,
		TotalStars:                 10_000_000,
		TotalCommits:               123_456,
		TotalCommitsAllTime:        9_876_543,
		TotalPRs:                   12_345,
		TotalIssues:                6_789,
		TotalReviews:               54_321,
		TotalContributedTo:         777,
		TotalContributionsLastYear: 200_000,
		CreatedAt:                  time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC),
		// Nine languages so the donut's "top 7 + Other" layout renders all
		// eight legend rows — catches any regression that breaks the tall
		// legend geometry at y≈195.
		ReposByLanguage: []github.LangStat{
			{Name: "JavaScript", Color: "#f1e05a", Value: 1234},
			{Name: "TypeScript", Color: "#3178c6", Value: 999},
			{Name: "Go", Color: "#00ADD8", Value: 500},
			{Name: "Rust", Color: "#dea584", Value: 321},
			{Name: "Python", Color: "#3572A5", Value: 200},
			{Name: "HTML", Color: "#e34c26", Value: 180},
			{Name: "Shell", Color: "#89e051", Value: 120},
			{Name: "Kotlin", Color: "#A97BFF", Value: 60},
			{Name: "Java", Color: "#b07219", Value: 30},
		},
		CommitsByLanguage: []github.LangStat{
			{Name: "JavaScript", Color: "#f1e05a", Value: 1_000_000},
			{Name: "Rust", Color: "#dea584", Value: 500_000},
		},
		CommitsByLanguageAllTime: []github.LangStat{
			{Name: "JavaScript", Color: "#f1e05a", Value: 5_000_000},
			{Name: "Java", Color: "#b07219", Value: 2_000_000},
		},
	}
	for i := range p.Productive {
		p.Productive[i] = 9999
		p.ProductiveAllTime[i] = 999_999
	}
	for i := range p.Weekday {
		p.Weekday[i] = 9999
		p.WeekdayAllTime[i] = 999_999
	}

	// Top repos with very long names — truncateName must kick in.
	for i := 0; i < 8; i++ {
		p.TopRepos = append(p.TopRepos, github.RepoInfo{
			Owner:           "user",
			Name:            "a-repository-with-an-absurdly-long-slug-" + strings.Repeat("x", 20),
			Stars:           1_000_000 - i*123_456,
			PrimaryLanguage: "TypeScript",
			PrimaryColor:    "#3178c6",
		})
	}

	// 20-year history ending today so the heatmap sees exactly 53 weeks and
	// the by-year card gets 20 bars.
	base := time.Date(time.Now().Year()-20, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 365*20+5; i++ {
		p.DailyContributionsAllTime = append(p.DailyContributionsAllTime,
			github.DailyContribution{Date: base.AddDate(0, 0, i), Count: i % 17})
	}
	yearStart := time.Now().AddDate(-1, 0, 0)
	for i := 0; i < 371; i++ {
		p.DailyContributions = append(p.DailyContributions,
			github.DailyContribution{Date: yearStart.AddDate(0, 0, i), Count: i % 23})
	}
	return p
}

// TestNiceTicksSmallMaxUsesWholeCounts covers the near-empty charts: every
// card forces an all-zero series to max 1, and small counts must still get
// whole-number labels that fit the 4-character axis gutter.
func TestNiceTicksSmallMaxUsesWholeCounts(t *testing.T) {
	cases := map[float64][]float64{
		1: {0, 1},
		2: {0, 1, 2},
		3: {0, 1, 2, 3},
	}
	for m, want := range cases {
		got := niceTicks(m, 5)
		if len(got) != len(want) {
			t.Errorf("niceTicks(%v)=%v want %v", m, got, want)
			continue
		}
		for i, v := range got {
			if v != want[i] || v != math.Trunc(v) {
				t.Errorf("niceTicks(%v)=%v want %v", m, got, want)
				break
			}
			if label := formatTick(v); len(label) > 4 {
				t.Errorf("niceTicks(%v): label %q longer than 4 characters", m, label)
			}
		}
	}
}

// TestNiceTicksHaveNoFloatDrift checks a spread of maxima for labels that
// outgrow the axis gutter, such as "0.6000000000000001".
func TestNiceTicksHaveNoFloatDrift(t *testing.T) {
	for m := 1; m <= 5000; m += 7 {
		for _, v := range niceTicks(float64(m), 5) {
			if label := formatTick(v); len(label) > 4 {
				t.Errorf("niceTicks(%d): label %q longer than 4 characters", m, label)
			}
		}
	}
}

// TestHeatmapRampDistinctForEveryTheme guards the heatmap against palettes
// whose colors the blender cannot parse (such as 8-digit "#rrggbbaa"), which
// collapse all five intensity levels into a single color.
func TestHeatmapRampDistinctForEveryTheme(t *testing.T) {
	for _, id := range theme.IDs() {
		th, _ := theme.Lookup(id)
		ramp := heatmapRamp(th)
		seen := map[string]int{}
		for i, c := range ramp {
			key := strings.ToLower(c)
			if j, dup := seen[key]; dup {
				t.Errorf("%s: heatmap levels %d and %d share color %s (ramp %v)", id, j, i, c, ramp)
			}
			seen[key] = i
		}
	}
}

func TestMixHexCarriesAlpha(t *testing.T) {
	cases := []struct {
		a, b  string
		ratio float64
		want  string
	}{
		{"#000000", "#ffffff", 0.5, "#7f7f7f"},
		{"#00000000", "#006AFF", 0, "#00000000"},
		{"#00000000", "#006AFF", 0.5, "#006aff80"},
		{"#00000000", "#006AFF", 1, "#006affff"},
		{"#31353a", "#19f9d899", 1, "#19f9d899"},
		{"#31353a", "#19f9d899", 0, "#31353aff"},
		{"not-a-color", "#123456", 0.5, "#123456"},
	}
	for _, c := range cases {
		if got := mixHex(c.a, c.b, c.ratio); got != c.want {
			t.Errorf("mixHex(%q, %q, %v)=%q want %q", c.a, c.b, c.ratio, got, c.want)
		}
	}
}

// TestBucketForScalesToBusiestDay checks that the busiest day always reaches
// the full accent, any active day is at least bucket 1, and light
// contributors are not painted at full intensity.
func TestBucketForScalesToBusiestDay(t *testing.T) {
	cases := []struct {
		count, max, want int
	}{
		{0, 0, 0},
		{0, 1, 0},
		{1, 1, 4},
		{1, 2, 2},
		{2, 2, 4},
		{1, 3, 2},
		{2, 3, 3},
		{3, 3, 4},
		{1, 4, 1},
		{2, 4, 2},
		{3, 4, 3},
		{4, 4, 4},
		{1, 8, 1},
		{2, 8, 1},
		{3, 8, 2},
		{8, 8, 4},
		{1, 100, 1},
		{25, 100, 1},
		{26, 100, 2},
		{50, 100, 2},
		{75, 100, 3},
		{76, 100, 4},
		{100, 100, 4},
	}
	for _, c := range cases {
		if got := bucketFor(c.count, c.max); got != c.want {
			t.Errorf("bucketFor(%d, %d)=%d want %d", c.count, c.max, got, c.want)
		}
	}
}

// TestMonthLabelsDoNotOverlap walks every month count the contributions
// cards can plot and checks consecutive printed labels never overlap: each
// "mm/yy" label is about 30 px wide, and the pinned last label keeps at
// least minMonthLabelPitch from the one before it.
func TestMonthLabelsDoNotOverlap(t *testing.T) {
	const (
		chartW     = 284 // contributions card: 340 − 28 − 28
		labelWidth = 30
	)
	for n := xLabelTarget + 1; n <= 300; n++ {
		prev := -1.0
		for i := 0; i < n; i++ {
			if !xAxisLabelVisible(i, n, chartW) {
				continue
			}
			x := float64(chartW) * float64(i) / float64(n-1)
			minGap := float64(labelWidth)
			if i == n-1 {
				minGap = minMonthLabelPitch
			}
			if prev >= 0 && x-prev < minGap {
				t.Errorf("n=%d: label %d sits %.1f px from the previous one", n, i, x-prev)
			}
			prev = x
		}
		if !xAxisLabelVisible(0, n, chartW) || !xAxisLabelVisible(n-1, n, chartW) {
			t.Errorf("n=%d: first and last month must always be labelled", n)
		}
	}
}

// TestYearLabelsDoNotOverlap covers every realistic history length (GitHub
// launched in 2008) using the contributions-by-year bar geometry.
func TestYearLabelsDoNotOverlap(t *testing.T) {
	const (
		chartW = 290 // 340 − 35 − 15
		barGap = 2
	)
	for n := 1; n <= 25; n++ {
		barW := float64(chartW-barGap*(n-1)) / float64(n)
		pitch := barW + barGap
		prev := -1.0
		for i := 0; i < n; i++ {
			if !yearLabelVisible(i, n, pitch) {
				continue
			}
			x := pitch * float64(i)
			if prev >= 0 && x-prev < minYearLabelPitch {
				t.Errorf("n=%d: year label %d sits %.1f px from the previous one", n, i, x-prev)
			}
			prev = x
		}
		if !yearLabelVisible(n-1, n, pitch) {
			t.Errorf("n=%d: last year must always be labelled", n)
		}
	}
}

// TestTopStarredReposListsPrivateRepos pins the current behavior: with
// include_private on, a starred private repo appears on the card like any
// other owned repo.
func TestTopStarredReposListsPrivateRepos(t *testing.T) {
	th, _ := theme.Lookup("dracula")
	p := &github.Profile{TopRepos: []github.RepoInfo{
		{Owner: "u", Name: "public-one", Stars: 9},
		{Owner: "u", Name: "secret-one", Stars: 5, IsPrivate: true},
	}}
	data, err := topStarredReposCard{}.SVG(p, th)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"public-one", "secret-one"} {
		if !strings.Contains(string(data), ">"+name+"<") {
			t.Errorf("expected %q on the card", name)
		}
	}
}

func TestTopStarredReposEmptyState(t *testing.T) {
	th, _ := theme.Lookup("dracula")
	data, err := topStarredReposCard{}.SVG(&github.Profile{}, th)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "No starred repos yet.") {
		t.Errorf("empty card should say there are no starred repos; got:\n%s", data)
	}
}
