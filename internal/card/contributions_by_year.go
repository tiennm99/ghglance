package card

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

type contributionsByYearCard struct{}

func (contributionsByYearCard) Filename() string { return "contributions-by-year.svg" }

func (contributionsByYearCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	const (
		width    = 340
		height   = 200
		leftAxis = 35
		rightPad = 15
		topPad   = 45
		chartH   = 110
		barGap   = 2
	)

	buckets := aggregateByYear(p.DailyContributionsAllTime)

	var b strings.Builder
	b.WriteString(header(width, height, t.Background, t.Stroke, t.StrokeOpacity, t.Title, "Contributions by Year"))

	if len(buckets) == 0 {
		fmt.Fprintf(&b, `
  <text x="25" y="100" font-size="13" fill="%s">No contribution data available.</text>`, t.Muted)
		b.WriteString(footer)
		return []byte(b.String()), nil
	}

	chartW := width - leftAxis - rightPad
	barW := float64(chartW-barGap*(len(buckets)-1)) / float64(len(buckets))

	var maxVal int
	peakIdx := 0
	for i, bk := range buckets {
		if bk.Count > maxVal {
			maxVal = bk.Count
			peakIdx = i
		}
	}
	yMax, ticks := yScale(maxVal)
	writeYAxis(&b, axisLeft, leftAxis, topPad, chartH, ticks, yMax, t.Muted)

	// X axis baseline.
	fmt.Fprintf(&b, `
  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>`,
		leftAxis, topPad+chartH, leftAxis+chartW, topPad+chartH, t.Muted)

	// Bars + year labels. Peak year uses Accent; others use a muted Accent
	// mix so the eye snaps to the year that matters most.
	dim := mixHex(t.Background, t.Accent, 0.55)
	for i, bk := range buckets {
		barH := float64(chartH) * float64(bk.Count) / yMax
		x := float64(leftAxis) + (barW+float64(barGap))*float64(i)
		y := float64(topPad+chartH) - barH
		fill := dim
		if i == peakIdx {
			fill = t.Accent
		}
		fmt.Fprintf(&b, `
  <rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="2" fill="%s"><title>%d — %d commits</title></rect>`,
			x, y, barW, barH, fill, bk.Year, bk.Count)

		if yearLabelVisible(i, len(buckets), barW+float64(barGap)) {
			cx := x + barW/2
			fmt.Fprintf(&b, `
  <text x="%.2f" y="%d" font-size="%d" fill="%s" text-anchor="middle">%d</text>`,
				cx, topPad+chartH+14, fontAxis, t.Muted, bk.Year)
		}
	}

	b.WriteString(footer)
	return []byte(b.String()), nil
}

// yearBucket holds a calendar-year aggregate count.
type yearBucket struct {
	Year  int
	Count int
}

// aggregateByYear bins the daily series into year totals, ascending. Missing
// years between first and last (rare but possible) become zero rows so the
// x-axis stays chronologically continuous.
func aggregateByYear(days []github.DailyContribution) []yearBucket {
	if len(days) == 0 {
		return nil
	}
	counts := map[int]int{}
	var minY, maxY int
	minY, maxY = days[0].Date.Year(), days[0].Date.Year()
	for _, d := range days {
		y := d.Date.Year()
		counts[y] += d.Count
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	out := make([]yearBucket, 0, maxY-minY+1)
	for y := minY; y <= maxY; y++ {
		out = append(out, yearBucket{Year: y, Count: counts[y]})
	}
	// Defensive sort in case caller ever passes an unordered slice.
	sort.Slice(out, func(i, j int) bool { return out[i].Year < out[j].Year })
	return out
}

// minYearLabelPitch is the closest two year labels may sit, center to
// center: a four-digit year at the axis font is about 24 px wide.
const minYearLabelPitch = 28

// yearLabelVisible reports whether bar i of n, spaced pitch px apart, gets a
// year label. Labels follow yearLabelStride and the last year is always
// labelled; a stride label that would sit closer than minYearLabelPitch to
// that pinned last label is dropped so the two never overlap.
func yearLabelVisible(i, n int, pitch float64) bool {
	if i == n-1 {
		return true
	}
	if i%yearLabelStride(n) != 0 {
		return false
	}
	return float64(n-1-i)*pitch >= minYearLabelPitch
}

// yearLabelStride picks how many years between printed x-axis labels so the
// axis stays legible when the user has a long GitHub history.
func yearLabelStride(n int) int {
	switch {
	case n <= 8:
		return 1
	case n <= 16:
		return 2
	default:
		return 3
	}
}
