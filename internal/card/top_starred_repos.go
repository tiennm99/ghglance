package card

import (
	"fmt"
	"strings"

	"github.com/tiennm99/ghglance/internal/github"
	"github.com/tiennm99/ghglance/internal/theme"
)

type topStarredReposCard struct{}

func (topStarredReposCard) Filename() string { return "top-starred-repos.svg" }

// maxTopRepoRows is how many repos we show. Matches the row count the stats
// and records cards fit in the shared 340x200 frame.
const maxTopRepoRows = 7

func (topStarredReposCard) SVG(p *github.Profile, t theme.Theme) ([]byte, error) {
	const (
		width   = 340
		height  = 200
		rowX    = 20
		rowY0   = 55
		rowDY   = 20
		barX    = 150
		barW    = 120 // max bar width; the top repo fills this
		barH    = 10
		valueX  = 334 // right-aligned anchor for the star count
		nameMax = 17  // truncate long repo names at this many characters
	)

	repos := ownedNonForkRepos(p.TopRepos)
	if len(repos) > maxTopRepoRows {
		repos = repos[:maxTopRepoRows]
	}

	var b strings.Builder
	b.WriteString(header(width, height, t.Background, t.Stroke, t.StrokeOpacity, t.Title, "Top Starred Repos"))

	if len(repos) == 0 {
		fmt.Fprintf(&b, `
  <text x="25" y="100" font-size="13" fill="%s">No starred repos yet.</text>`, t.Muted)
		b.WriteString(footer)
		return []byte(b.String()), nil
	}

	maxStars := repos[0].Stars
	if maxStars <= 0 {
		maxStars = 1
	}

	// Row layout: language swatch + name on the left, a proportional bar in
	// the middle, star count right-anchored at valueX. The card title already
	// says "Top Starred Repos", so the per-row star icon would just be noise.
	for i, r := range repos {
		y := rowY0 + i*rowDY
		langColor := r.PrimaryColor
		if langColor == "" {
			langColor = t.Accent
		}
		name := truncate(r.Name, nameMax)

		fmt.Fprintf(&b, `
  <circle cx="%d" cy="%d" r="4" fill="%s"/>
  <text x="%d" y="%d" font-size="%d" fill="%s">%s</text>`,
			rowX+4, y-4, langColor,
			rowX+14, y, fontBody, t.Text, escapeXML(name))

		bw := float64(barW) * float64(r.Stars) / float64(maxStars)
		fmt.Fprintf(&b, `
  <rect x="%d" y="%d" width="%d" height="%d" rx="3" fill="%s" fill-opacity="0.15"/>
  <rect x="%d" y="%d" width="%.2f" height="%d" rx="3" fill="%s"/>`,
			barX, y-barH+2, barW, barH, t.Accent,
			barX, y-barH+2, bw, barH, t.Accent)

		fmt.Fprintf(&b, `
  <text x="%d" y="%d" font-size="%d" font-weight="600" fill="%s" text-anchor="end">%s ★</text>`,
			valueX, y, fontBody, t.Accent, escapeXML(formatInt(r.Stars)))
	}

	b.WriteString(footer)
	return []byte(b.String()), nil
}

// ownedNonForkRepos filters out forks so the card highlights the user's own
// work. TopRepos is already sorted by stargazer count desc at fetch time, so
// we just skim the prefix.
func ownedNonForkRepos(repos []github.RepoInfo) []github.RepoInfo {
	out := make([]github.RepoInfo, 0, len(repos))
	for _, r := range repos {
		if r.IsFork {
			continue
		}
		if r.Stars <= 0 {
			continue
		}
		out = append(out, r)
	}
	return out
}
