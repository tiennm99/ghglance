package card

import (
	"fmt"
	"strings"

	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

type statsCard struct{}

func (statsCard) Filename() string { return "stats.svg" }

// statRow is one labeled-by-icon line in the stats card.
type statRow struct {
	icon  string
	label string
	value string
}

func (statsCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	const (
		width    = 340
		height   = 200
		rowX     = 20
		rowY0    = 55
		rowDY    = 20
		iconSize = 12
		valueX   = 320 // right-aligned x anchor
	)

	rows := []statRow{
		{iconStar, "Total Stars", formatInt(p.TotalStars)},
		{iconCommit, "Total Commits (all time)", formatInt(p.TotalCommitsAllTime)},
		{iconCommit, "Total Commits (last year)", formatInt(p.TotalCommits)},
		{iconPR, "Total PRs", formatInt(p.TotalPRs)},
		{iconIssue, "Total Issues", formatInt(p.TotalIssues)},
		{iconReview, "PR Reviews (last year)", formatInt(p.TotalReviews)},
		{iconRepos, "Contributed to", formatInt(p.TotalContributedTo)},
	}

	var b strings.Builder
	b.WriteString(header(width, height, t.Background, t.Stroke, t.StrokeOpacity, t.Title, "Stats"))

	scale := float64(iconSize) / 16.0
	for i, r := range rows {
		y := rowY0 + i*rowDY
		fmt.Fprintf(&b, `
  <g transform="translate(%d,%.2f) scale(%.3f)" fill="%s">%s</g>
  <text x="%d" y="%d" font-size="12" fill="%s">%s</text>
  <text x="%d" y="%d" font-size="12" font-weight="600" fill="%s" text-anchor="end">%s</text>`,
			rowX, float64(y-iconSize+2), scale, t.Muted, r.icon,
			rowX+iconSize+8, y, t.Text, escapeXML(r.label),
			valueX, y, t.Accent, escapeXML(r.value))
	}

	b.WriteString(footer)
	return []byte(b.String()), nil
}
