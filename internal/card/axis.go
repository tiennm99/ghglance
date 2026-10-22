package card

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// niceTicks returns evenly-spaced tick values starting at 0 with a step of
// 1/2/5/10 × 10^k and a tick count roughly targetTicks. The last tick is the
// smallest step multiple ≥ max, so callers can safely use it as yMax without
// data points ever exceeding the chart's height.
//
// Mirrors d3.scaleLinear().nice() / d3.axisLeft().ticks(n) so charts built on
// top look visually consistent with the d3 reference.
//
// Example: niceTicks(625, 5) returns [0, 100, 200, 300, 400, 500, 600, 700]
// — note 700 > 625, so a bar of 625 fits at 625/700 ≈ 89% of chart height
// with a clean gap above it.
//
// The step never drops below 1: every chart plots whole counts, so a
// fractional tick such as 0.2 or 2.5 would label a value no bar can have.
func niceTicks(max float64, targetTicks int) []float64 {
	if max <= 0 || targetTicks <= 0 {
		return []float64{0}
	}
	rough := max / float64(targetTicks)
	exp := math.Pow(10, math.Floor(math.Log10(rough)))
	frac := rough / exp
	var step float64
	switch {
	case frac < 1.5:
		step = 1 * exp
	case frac < 3:
		step = 2 * exp
	case frac < 7:
		step = 5 * exp
	default:
		step = 10 * exp
	}
	if step < 1 {
		step = 1
	}

	// Round the top tick up to the next step multiple so the chart's yMax
	// always strictly covers the data. Without this, a data point of 625
	// against a step of 100 would yield a last tick of 600 — bars for 625
	// would render at 104% of chart height and poke into the title area.
	last := math.Ceil(max/step) * step

	// Multiply instead of accumulating so float drift never produces a
	// label like "0.6000000000000001".
	n := int(math.Round(last / step))
	out := make([]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		out = append(out, float64(i)*step)
	}
	return out
}

// yScale returns the y-axis ticks for a chart whose tallest value is maxVal,
// and the top tick as yMax. An all-zero chart still gets a 0..1 axis.
func yScale(maxVal int) (yMax float64, ticks []float64) {
	yMax = float64(maxVal)
	if yMax <= 0 {
		yMax = 1
	}
	ticks = niceTicks(yMax, 5)
	return ticks[len(ticks)-1], ticks
}

// axisSide says which way a y-axis faces: tick marks and labels sit outside
// the chart, to the left of a left axis and to the right of a right axis.
type axisSide int

const (
	axisLeft axisSide = iota
	axisRight
)

// writeYAxis draws a vertical axis line at x spanning the chart height, plus
// a tick mark and label for each tick, scaled so yMax lands on topPad.
func writeYAxis(b *strings.Builder, side axisSide, x, topPad, chartH int, ticks []float64, yMax float64, color string) {
	fmt.Fprintf(b, `
  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>`,
		x, topPad, x, topPad+chartH, color)
	markX, labelX, anchor := x-4, x-6, "end"
	if side == axisRight {
		markX, labelX, anchor = x+4, x+6, "start"
	}
	for _, v := range ticks {
		y := topPad + chartH - int(float64(chartH)*v/yMax)
		fmt.Fprintf(b, `
  <line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s"/>
  <text x="%d" y="%d" font-size="%d" fill="%s" text-anchor="%s">%s</text>`,
			min(markX, x), y, max(markX, x), y, color,
			labelX, y+3, fontAxis, color, anchor, escapeXML(formatTick(v)))
	}
}

// formatTick renders a float tick label, abbreviating thousands / millions /
// billions so every possible y-axis label fits within ≤4 characters. The
// leftPad gutter of every chart card is sized for ≤4 chars at 10 px font,
// so anything wider would overflow past the card frame for busy profiles
// (1000+ monthly commits, 10k+ yearly contributions, etc).
//
// Examples:
//
//	999       -> "999"
//	1_000     -> "1k"
//	1_500     -> "1.5k"
//	12_345    -> "12k"
//	1_234_567 -> "1.2M"
func formatTick(v float64) string {
	if v == 0 {
		return "0"
	}
	abs := math.Abs(v)
	if abs < 1000 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	var div float64
	var suffix string
	switch {
	case abs < 1_000_000:
		div, suffix = 1000, "k"
	case abs < 1_000_000_000:
		div, suffix = 1_000_000, "M"
	default:
		div, suffix = 1_000_000_000, "B"
	}
	n := v / div
	// One decimal place only when it matters. 1.5k stays "1.5k", 10k stays
	// "10k" not "10.0k", 500k stays "500k".
	if math.Abs(n) >= 10 || n == math.Trunc(n) {
		return strconv.FormatFloat(n, 'f', 0, 64) + suffix
	}
	return fmt.Sprintf("%.1f%s", n, suffix)
}
