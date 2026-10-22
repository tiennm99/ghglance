package card

import (
	"fmt"
	"strings"
	"time"

	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

type streakCard struct{}

func (streakCard) Filename() string { return "streak.svg" }

func (streakCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	const (
		width  = 340
		height = 200
	)

	stats := computeStreak(p.DailyContributionsAllTime)

	var b strings.Builder
	b.WriteString(header(width, height, t.Background, t.Stroke, t.StrokeOpacity, t.Title, "Streak"))

	// Three large stat columns (current / longest / active-days) side by side,
	// each with a big number on top, a label underneath, and a small detail
	// line. Keeping the big number to a single formatted integer per column
	// means no column can overflow regardless of magnitude (formatInt adds
	// thousands separators and even 10-digit counts fit ≤113 px at 28 px).
	cols := []struct {
		value  string
		label  string
		detail string
	}{
		{formatInt(stats.Current), "Current streak", streakRange(stats.CurrentStart, stats.CurrentEnd)},
		{formatInt(stats.Longest), "Longest streak", streakRange(stats.LongestStart, stats.LongestEnd)},
		{formatInt(stats.Active), "Active days", activeDaysDetail(stats.Active, stats.Total)},
	}
	colW := width / len(cols)
	for i, c := range cols {
		cx := colW*i + colW/2
		fmt.Fprintf(&b, `
  <text x="%d" y="%d" font-size="%d" font-weight="700" fill="%s" text-anchor="middle">%s</text>
  <text x="%d" y="%d" font-size="%d" fill="%s" text-anchor="middle">%s</text>`,
			cx, 95, fontBigNum, t.Accent, escapeXML(c.value),
			cx, 120, fontBody, t.Text, escapeXML(c.label))
		if c.detail != "" {
			fmt.Fprintf(&b, `
  <text x="%d" y="%d" font-size="%d" fill="%s" text-anchor="middle">%s</text>`,
				cx, 140, fontAxis, t.Muted, escapeXML(c.detail))
		}
	}

	b.WriteString(footer)
	return []byte(b.String()), nil
}

// streakStats is the post-processed daily series summarised for the card.
type streakStats struct {
	Current                  int
	CurrentStart, CurrentEnd time.Time
	Longest                  int
	LongestStart, LongestEnd time.Time
	Active                   int // days with ≥1 contribution
	Total                    int // total days observed
}

// computeStreak walks the daily series once. The "current streak" runs
// backwards from the most recent day; if today has 0 contributions we still
// count yesterday as current (a single-day grace) so the card doesn't reset
// the moment a user hasn't pushed yet today.
func computeStreak(days []github.DailyContribution) streakStats {
	var s streakStats
	if len(days) == 0 {
		return s
	}
	s.Total = len(days)

	// Longest streak + active day count: single forward pass.
	var run int
	var runStart time.Time
	for _, d := range days {
		if d.Count > 0 {
			s.Active++
			if run == 0 {
				runStart = d.Date
			}
			run++
			if run > s.Longest {
				s.Longest = run
				s.LongestStart = runStart
				s.LongestEnd = d.Date
			}
		} else {
			run = 0
		}
	}

	// Current streak: walk backwards from the end. Skip at most one trailing
	// zero-day (today-not-pushed-yet) before aborting.
	tail := len(days) - 1
	if days[tail].Count == 0 && tail > 0 {
		tail--
	}
	for i := tail; i >= 0; i-- {
		if days[i].Count == 0 {
			break
		}
		s.Current++
		s.CurrentEnd = days[tail].Date
		s.CurrentStart = days[i].Date
	}
	return s
}

// activeDaysDetail renders the "/ total" denominator as a small sub-line so
// the big number in the column stays a single formatted integer — that way
// a user with 10,000+ active days never squeezes against the column edges.
func activeDaysDetail(active, total int) string {
	if total <= 0 {
		return ""
	}
	pct := 100 * active / total
	return fmt.Sprintf("of %s total (%d%%)", formatInt(total), pct)
}

// streakRange formats the open/close dates of a streak for the small detail
// line. Each column is ~113 px wide at font-size 10 (≤ ~18 chars), so we
// aggressively drop year and redundant parts when they'd blow the budget.
//
//	same day     -> "Jan 2 2025"
//	same year    -> "Jan 2 — Dec 31"
//	different    -> "2024 — 2026"
func streakRange(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return ""
	}
	if start.Equal(end) {
		return start.Format("Jan 2 2006")
	}
	if start.Year() != end.Year() {
		return fmt.Sprintf("%d — %d", start.Year(), end.Year())
	}
	return start.Format("Jan 2") + " — " + end.Format("Jan 2")
}
