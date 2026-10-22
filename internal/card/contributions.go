package card

import (
	"fmt"
	"strings"
	"time"

	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

type contributionsCard struct{}

func (contributionsCard) Filename() string { return "contributions.svg" }

func (contributionsCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	return renderContributions("Contributions (last year)", trimToLastYear(p.DailyContributions), t), nil
}

// trimToLastYear keeps only the trailing 13-month window ending at the
// series' last day. GitHub's contributionCalendar is week-aligned, so a
// "last year" query bleeds into the calendar month before today minus one
// year (e.g. asking on 2026-05-02 returns days back to ~2025-04-27). That
// extra partial month inflates the bucket count to 14 and forces the x-axis
// stride logic to place the final two month labels right next to each other.
// Trimming to start-of-(today.Month - 1y) restores a clean 13-bucket span.
func trimToLastYear(days []github.DailyContribution) []github.DailyContribution {
	if len(days) == 0 {
		return days
	}
	last := days[len(days)-1].Date
	cutoff := time.Date(last.Year()-1, last.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i, d := range days {
		if !d.Date.Before(cutoff) {
			return days[i:]
		}
	}
	return nil
}

type contributionsAllTimeCard struct{}

func (contributionsAllTimeCard) Filename() string { return "contributions-all-time.svg" }

func (contributionsAllTimeCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	return renderContributions("Contributions (all time)", p.DailyContributionsAllTime, t), nil
}

// monthBucket holds a calendar month's aggregate contribution count.
type monthBucket struct {
	Year  int
	Month time.Month
	Count int
}

// renderContributions draws a smooth filled area chart with y-axis labels on
// both sides and YY/MM labels on the x-axis. Shared by last-year and all-time
// contribution cards; only title + data differ.
func renderContributions(title string, days []github.DailyContribution, t theme.Theme) []byte {
	const (
		width    = 340
		height   = 200
		leftPad  = 28
		rightPad = 28
		topPad   = 45
		chartH   = 105
	)
	chartW := width - leftPad - rightPad

	var b strings.Builder
	b.WriteString(header(width, height, t.Background, t.Stroke, t.StrokeOpacity, t.Title, title))

	buckets := aggregateByMonth(days)
	if len(buckets) < 2 {
		fmt.Fprintf(&b, `
  <text x="25" y="90" font-size="13" fill="%s">No contribution data available.</text>`, t.Muted)
		b.WriteString(footer)
		return []byte(b.String())
	}

	// Y scale based on max monthly count; nice ticks for labels.
	var maxVal int
	for _, bk := range buckets {
		maxVal = max(maxVal, bk.Count)
	}
	yMax, ticks := yScale(maxVal)

	// Map each bucket to an (x, y) point on the chart grid.
	pts := make([][2]float64, len(buckets))
	for i, bk := range buckets {
		x := float64(leftPad) + float64(chartW)*float64(i)/float64(len(buckets)-1)
		y := float64(topPad+chartH) - float64(chartH)*float64(bk.Count)/yMax
		pts[i] = [2]float64{x, y}
	}

	// Y axis mirrored on both sides with matching nice ticks.
	writeYAxis(&b, axisLeft, leftPad, topPad, chartH, ticks, yMax, t.Muted)
	writeYAxis(&b, axisRight, leftPad+chartW, topPad, chartH, ticks, yMax, t.Muted)

	// X axis baseline + month labels. Stride is chosen so roughly xLabelTarget
	// labels span the full width regardless of bucket count — keeps the axis
	// readable whether we plot 12 months (last year) or 100+ months (all time).
	fmt.Fprintf(&b, `
  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>`,
		leftPad, topPad+chartH, leftPad+chartW, topPad+chartH, t.Muted)
	for i, bk := range buckets {
		if !xAxisLabelVisible(i, len(buckets), chartW) {
			continue
		}
		x := int(pts[i][0])
		label := fmt.Sprintf("%02d/%02d", int(bk.Month), bk.Year%100)
		fmt.Fprintf(&b, `
  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>
  <text x="%d" y="%d" font-size="%d" fill="%s" text-anchor="middle">%s</text>`,
			x, topPad+chartH, x, topPad+chartH+4, t.Muted,
			x, topPad+chartH+16, fontAxis, t.Muted, label)
	}

	// Smooth filled area using Catmull-Rom → cubic Bezier segments.
	path := catmullRomAreaPath(pts, float64(topPad+chartH))
	fmt.Fprintf(&b, `
  <path d="%s" fill="%s" fill-opacity="0.25" stroke="none"/>
  <path d="%s" fill="none" stroke="%s" stroke-width="2"/>`,
		path, t.Accent,
		catmullRomLinePath(pts), t.Accent)

	// Axis caption — documents the tick label format.
	fmt.Fprintf(&b, `
  <text x="%d" y="%d" font-size="%d" fill="%s" text-anchor="middle">mm/yy</text>`,
		leftPad+chartW/2, topPad+chartH+34, fontLabel, t.Muted)

	b.WriteString(footer)
	return []byte(b.String())
}

// xAxisLabelVisible returns true when the bucket at position i of n, on an
// axis chartW px wide, should get a printed month label. Targets
// ~xLabelTarget labels evenly distributed across the axis, always pinning
// the first and last so the span is obvious.
const xLabelTarget = 6

// minMonthLabelPitch is the closest two "mm/yy" labels may sit, center to
// center: each label is about 30 px wide at the axis font.
const minMonthLabelPitch = 34

func xAxisLabelVisible(i, n, chartW int) bool {
	if n <= xLabelTarget {
		return true // few enough points — label all of them
	}
	if i == 0 || i == n-1 {
		return true
	}
	stride := (n - 1) / (xLabelTarget - 1)
	if i%stride != 0 {
		return false
	}
	// Skip a label that would overlap the pinned last label.
	return (n-1-i)*chartW >= minMonthLabelPitch*(n-1)
}

// aggregateByMonth bins the daily series into consecutive month buckets
// sorted oldest→newest. Empty months between first and last are kept as
// zero-count rows so the area chart remains time-continuous.
func aggregateByMonth(days []github.DailyContribution) []monthBucket {
	if len(days) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, d := range days {
		key := fmt.Sprintf("%04d-%02d", d.Date.Year(), int(d.Date.Month()))
		counts[key] += d.Count
	}

	// Walk from the first to last month of the calendar inclusively.
	first := days[0].Date
	last := days[len(days)-1].Date
	cur := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(last.Year(), last.Month(), 1, 0, 0, 0, 0, time.UTC)

	var out []monthBucket
	for !cur.After(end) {
		key := fmt.Sprintf("%04d-%02d", cur.Year(), int(cur.Month()))
		out = append(out, monthBucket{
			Year:  cur.Year(),
			Month: cur.Month(),
			Count: counts[key],
		})
		cur = cur.AddDate(0, 1, 0)
	}
	return out
}

// catmullRomLinePath produces an SVG path string that passes through each
// point using cubic Bezier segments derived from the Catmull-Rom spline
// (tension = 0.5, the classic d3.curveCatmullRom default).
func catmullRomLinePath(pts [][2]float64) string {
	if len(pts) < 2 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "M%.2f,%.2f", pts[0][0], pts[0][1])
	for i := 0; i < len(pts)-1; i++ {
		p0 := pts[max(i-1, 0)]
		p1 := pts[i]
		p2 := pts[i+1]
		p3 := pts[min(i+2, len(pts)-1)]

		c1x := p1[0] + (p2[0]-p0[0])/6
		c1y := p1[1] + (p2[1]-p0[1])/6
		c2x := p2[0] - (p3[0]-p1[0])/6
		c2y := p2[1] - (p3[1]-p1[1])/6

		fmt.Fprintf(&b, " C%.2f,%.2f %.2f,%.2f %.2f,%.2f",
			c1x, c1y, c2x, c2y, p2[0], p2[1])
	}
	return b.String()
}

// catmullRomAreaPath closes the smooth line path down to the baseline so it
// can be filled as an area under the curve.
func catmullRomAreaPath(pts [][2]float64, baseline float64) string {
	line := catmullRomLinePath(pts)
	if line == "" {
		return ""
	}
	return fmt.Sprintf("%s L%.2f,%.2f L%.2f,%.2f Z",
		line, pts[len(pts)-1][0], baseline, pts[0][0], baseline)
}
