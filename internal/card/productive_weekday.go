package card

import (
	"fmt"
	"strings"
	"time"

	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

type productiveWeekdayCard struct{}

func (productiveWeekdayCard) Filename() string { return "productive-weekday.svg" }

func (productiveWeekdayCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	return renderWeekday(weekdayTitle("last year"), p.Weekday, p.WeekStart, t), nil
}

type productiveWeekdayAllTimeCard struct{}

func (productiveWeekdayAllTimeCard) Filename() string { return "productive-weekday-all-time.svg" }

func (productiveWeekdayAllTimeCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	return renderWeekday(weekdayTitle("all time"), p.WeekdayAllTime, p.WeekStart, t), nil
}

// weekdayTitle skips the UTC offset — the data aggregates into day-of-week
// buckets, so exact clock precision isn't informative and dropping it keeps
// the title short enough to render at the full 15 px.
func weekdayTitle(window string) string {
	return "Commits by Weekday (" + window + ")"
}

// renderWeekday draws a 7-bar chart: one bar per weekday. Reuses the same
// axis math as the hour-of-day card so the two feel like a matched pair.
// data is always indexed 0=Sun..6=Sat (raw time.Weekday); bar order is
// rotated so position 0 corresponds to weekStart.
func renderWeekday(title string, data [7]int, weekStart time.Weekday, t theme.Theme) []byte {
	const (
		width    = 340
		height   = 200
		leftAxis = 35
		rightPad = 15
		topPad   = 45
		chartH   = 110
		barGap   = 6
	)
	chartW := width - leftAxis - rightPad
	barW := float64(chartW-barGap*6) / 7.0

	var b strings.Builder
	b.WriteString(header(width, height, t.Background, t.Stroke, t.StrokeOpacity, t.Title, title))

	// rotated[i] is the weekday index (0..6 time.Weekday) rendered at
	// position i along the x-axis. Peak is tracked in position space so the
	// highlight lines up with the actual drawn bar.
	var rotated [7]int
	for i := 0; i < 7; i++ {
		rotated[i] = (int(weekStart) + i) % 7
	}
	maxVal := 0
	peak := 0
	for i := 0; i < 7; i++ {
		v := data[rotated[i]]
		if v > maxVal {
			maxVal = v
			peak = i
		}
	}
	yMax, ticks := yScale(maxVal)
	writeYAxis(&b, axisLeft, leftAxis, topPad, chartH, ticks, yMax, t.Muted)

	// X axis baseline + weekday labels.
	fmt.Fprintf(&b, `
  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>`,
		leftAxis, topPad+chartH, leftAxis+chartW, topPad+chartH, t.Muted)

	// Bars. Peak weekday gets full Accent; others the dimmed variant so the
	// busiest day reads at a glance.
	dim := mixHex(t.Background, t.Accent, 0.55)
	for i := 0; i < 7; i++ {
		wd := rotated[i]
		count := data[wd]
		label := weekdayShort[wd]
		barH := float64(chartH) * float64(count) / yMax
		x := float64(leftAxis) + (barW+float64(barGap))*float64(i)
		y := float64(topPad+chartH) - barH
		fill := dim
		if i == peak && maxVal > 0 {
			fill = t.Accent
		}
		fmt.Fprintf(&b, `
  <rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="2" fill="%s"><title>%s — %d commits</title></rect>
  <text x="%.2f" y="%d" font-size="%d" fill="%s" text-anchor="middle">%s</text>`,
			x, y, barW, barH, fill, label, count,
			x+barW/2, topPad+chartH+14, fontAxis, t.Muted, label)
	}

	b.WriteString(footer)
	return []byte(b.String())
}
