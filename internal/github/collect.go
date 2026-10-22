package github

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CollectConfig tunes Collect beyond the repo filters in FetchOptions.
type CollectConfig struct {
	Options FetchOptions
	// Location buckets commit timestamps for the productive-time cards and
	// names the UTC offset in their titles. Nil means UTC.
	Location *time.Location
	// WeekStart is the first heatmap row and weekday bar.
	WeekStart time.Weekday
	// TopRepos caps the seed repos probed for commit history, keeping the
	// most recently active ones (0 = unlimited).
	TopRepos int
	// CommitsPerRepo caps commits sampled per seed repo (0 = every commit).
	CommitsPerRepo int
	// Warnf reports non-fatal fetch failures; partial data still renders.
	// Nil discards them.
	Warnf func(format string, args ...any)
	// Strict turns those stage failures into an error, for callers that
	// would rather keep an older complete result than publish a partial one.
	Strict bool
}

// Collect runs the full fetch sequence every card needs: FetchProfile, then
// FetchContributionsAllTime (which yields the seed repos), then
// FetchProductive over those seeds. Only the profile fetch is fatal unless
// cfg.Strict is set; otherwise the later stages warn and leave their
// aggregates partially filled. Notices raised inside the fetchers (rate-limit
// sleeps, API ceilings) also go to cfg.Warnf.
func Collect(ctx context.Context, c *Client, login string, cfg CollectConfig) (*Profile, error) {
	warnf := cfg.Warnf
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	// Work on a copy so the caller's client keeps its own warn sink.
	cc := *c
	cc.warnf = warnf
	c = &cc
	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}

	profile, err := c.FetchProfile(ctx, login, cfg.Options)
	if err != nil {
		return nil, fmt.Errorf("fetch profile: %w", err)
	}
	profile.UTCOffsetLabel = UTCOffsetLabel(loc)
	profile.WeekStart = cfg.WeekStart

	// Year-loop fetch populates SeedRepos from commitContributionsByRepository
	// plus the all-time contribution calendar; must precede FetchProductive so
	// commit-history probes land on repos where the user actually committed.
	if len(profile.ContributionYears) > 0 {
		if err := c.FetchContributionsAllTime(ctx, profile, cfg.Options); err != nil {
			if cfg.Strict {
				return nil, fmt.Errorf("fetch all-time contributions: %w", err)
			}
			warnf("all-time contributions fetch: %v", err)
		}
	}

	if profile.ID != "" && len(profile.SeedRepos) > 0 {
		repos := profile.SeedRepos
		if cfg.TopRepos > 0 && len(repos) > cfg.TopRepos {
			repos = repos[:cfg.TopRepos]
		}
		if err := c.FetchProductive(ctx, profile, repos, loc, cfg.CommitsPerRepo); err != nil {
			if cfg.Strict {
				return nil, fmt.Errorf("fetch commit history: %w", err)
			}
			warnf("productive-time + commits-per-language fetch: %v", err)
		}
	}
	return profile, nil
}

// TokenInfo describes the account behind a token and how far it reaches.
type TokenInfo struct {
	Login string
	// CanReadPrivate is true when the token can read private repositories.
	// GitHub then counts contributions to those repos in every total and
	// calendar it returns, not only in the repo lists.
	CanReadPrivate bool
}

// TokenInfo identifies the client's token. A classic token is private-capable
// when its X-OAuth-Scopes include "repo"; any token is when it can list a
// private repository the viewer owns, collaborates on or reaches through an
// org (the only signal a fine-grained token gives).
func (c *Client) TokenInfo(ctx context.Context) (TokenInfo, error) {
	var resp struct {
		Viewer struct {
			Login        string `json:"login"`
			Repositories struct {
				TotalCount int `json:"totalCount"`
			} `json:"repositories"`
		} `json:"viewer"`
	}
	h, err := c.queryHeader(ctx, viewerQuery, nil, &resp)
	if err != nil {
		return TokenInfo{}, err
	}
	return TokenInfo{
		Login:          resp.Viewer.Login,
		CanReadPrivate: resp.Viewer.Repositories.TotalCount > 0 || scopesIncludeRepo(h.Get("X-OAuth-Scopes")),
	}, nil
}

// scopesIncludeRepo reports whether a classic token's scope list grants
// full repo access, which covers every private repo its owner can see.
func scopesIncludeRepo(scopes string) bool {
	for _, s := range strings.Split(scopes, ",") {
		if strings.TrimSpace(s) == "repo" {
			return true
		}
	}
	return false
}

// UTCOffsetLabel formats the location's current offset from UTC compactly:
//
//	integer hours  → "UTC+7"    (no ".00" padding — 3 chars shorter than
//	                             the old "UTC+7.00" format, keeps the
//	                             productive-time title at 15 px)
//	half-hour zone → "UTC+5:30" (India)
//	quarter-hour   → "UTC+5:45" (Nepal)
//	negative zone  → "UTC-3"    / "UTC-3:30"
func UTCOffsetLabel(loc *time.Location) string {
	_, offsetSec := time.Now().In(loc).Zone()
	sign := "+"
	if offsetSec < 0 {
		sign = "-"
		offsetSec = -offsetSec
	}
	hours := offsetSec / 3600
	minutes := (offsetSec % 3600) / 60
	if minutes == 0 {
		return fmt.Sprintf("UTC%s%d", sign, hours)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, hours, minutes)
}

// ParseWeekday maps a case-insensitive English weekday name (full or 3-letter)
// to time.Weekday. Empty input → Sunday so a blank action input still works.
func ParseWeekday(s string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return time.Sunday, fmt.Errorf("unknown start-of-week %q", s)
	}
}
